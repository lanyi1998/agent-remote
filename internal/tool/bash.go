package tool

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"pi-remote/internal/runtimebundle"
)

type bashInput struct {
	Command string   `json:"command"`
	Timeout *float64 `json:"timeout,omitempty"`
}

type bashResult struct {
	Output       string `json:"output"`
	OutputBase64 string `json:"output_base64"`
	ExitCode     int    `json:"exit_code"`
	Truncated    bool   `json:"truncated,omitempty"`
	TimedOut     bool   `json:"timed_out,omitempty"`
	ShellProfile string `json:"shell_profile"`
}

func (s *Service) executeBash(parent context.Context, rawInput []byte, onChunk ChunkWriter) (bashResult, error) {
	var input bashInput
	if err := decodeInput(rawInput, &input); err != nil {
		return bashResult{}, err
	}
	if strings.TrimSpace(input.Command) == "" {
		return bashResult{}, NewError("invalid_input", "command must not be empty")
	}
	timeout, err := s.bashTimeout(input.Timeout)
	if err != nil {
		return bashResult{}, err
	}
	runtimePaths, err := s.runtime.Resolve()
	if err != nil {
		return bashResult{}, WrapError("shell_unavailable", "resolve bash runtime", err)
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	collector := newOutputCollector(s.maxOutputBytes, onChunk)
	command, err := newShellCommand(runtimePaths)
	if err != nil {
		return bashResult{}, WrapError("shell_unavailable", "select shell runtime", err)
	}
	command.Dir = s.paths.Root()
	command.Env = shellEnvironment(runtimePaths.Root, s.paths.Root())
	command.Stdout = collector
	command.Stderr = collector
	script := buildScript(input.Command)
	exitCode, runErr := runManaged(ctx, command, strings.NewReader(script))
	output, encoded, truncated := collector.Snapshot()
	result := bashResult{
		Output:       output,
		OutputBase64: encoded,
		ExitCode:     exitCode,
		Truncated:    truncated,
		TimedOut:     errors.Is(ctx.Err(), context.DeadlineExceeded),
		ShellProfile: runtimePaths.ShellProfile,
	}
	if ctx.Err() != nil {
		code := "aborted"
		message := "command aborted"
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			code = "timeout"
			message = fmt.Sprintf("command timed out after %s", timeout)
		}
		return bashResult{}, &Error{Code: code, Message: message, Details: result}
	}
	if runErr != nil && exitCode < 0 {
		code := "command_failed"
		message := runErr.Error()
		return bashResult{}, &Error{Code: code, Message: message, Details: result}
	}
	return result, nil
}

func newShellCommand(paths runtimebundle.Paths) (*exec.Cmd, error) {
	if runtime.GOOS == "windows" && paths.BusyBox != "" {
		return exec.Command(paths.BusyBox, "sh", "-s"), nil
	}
	if paths.Bash != "" {
		return exec.Command(paths.Bash, "--noprofile", "--norc", "-s"), nil
	}
	return nil, errors.New("no shell runtime is configured")
}

func (s *Service) bashTimeout(seconds *float64) (time.Duration, error) {
	value := float64(s.defaultTimeoutSec)
	if seconds != nil {
		value = *seconds
	}
	if value <= 0 {
		return 0, NewError("invalid_input", "timeout must be greater than zero")
	}
	if value > float64(s.maxTimeoutSec) {
		return 0, NewError("invalid_input", fmt.Sprintf("timeout must not exceed %d seconds", s.maxTimeoutSec))
	}
	return time.Duration(value * float64(time.Second)), nil
}

func shellEnvironment(runtimeRoot, workingDirectory string) []string {
	environment := append([]string(nil), os.Environ()...)
	environment = setEnvironment(environment, "PWD", shellPath(workingDirectory))
	if runtimeRoot != "" {
		pathValue := strings.Join([]string{
			filepath.Join(runtimeRoot, "bin"),
			filepath.Join(runtimeRoot, "usr", "bin"),
			filepath.Join(runtimeRoot, "mingw64", "bin"),
			os.Getenv("PATH"),
		}, string(os.PathListSeparator))
		environment = setEnvironment(environment, "PATH", pathValue)
	}
	if runtime.GOOS == "windows" {
		environment = setEnvironment(environment, "MSYSTEM", msystemForArchitecture())
		environment = setEnvironment(environment, "CHERE_INVOKING", "1")
	}
	return environment
}

func setEnvironment(environment []string, key, value string) []string {
	prefix := strings.ToUpper(key) + "="
	filtered := make([]string, 0, len(environment)+1)
	for _, item := range environment {
		if !strings.HasPrefix(strings.ToUpper(item), prefix) {
			filtered = append(filtered, item)
		}
	}
	return append(filtered, key+"="+value)
}

func msystemForArchitecture() string {
	return "MINGW64"
}

func shellPath(name string) string {
	value := filepath.ToSlash(name)
	if runtime.GOOS == "windows" && len(value) >= 3 && value[1] == ':' && value[2] == '/' {
		drive := strings.ToLower(value[:1])
		return "/cygdrive/" + drive + value[2:]
	}
	return value
}

func buildScript(command string) string {
	var script strings.Builder
	script.WriteString(command)
	if !strings.HasSuffix(command, "\n") {
		script.WriteByte('\n')
	}
	return script.String()
}

type outputCollector struct {
	mu       sync.Mutex
	data     []byte
	maxBytes int
	total    int64
	onChunk  ChunkWriter
	decoder  outputDecoder
}

func newOutputCollector(maxBytes int, onChunk ChunkWriter) *outputCollector {
	return &outputCollector{maxBytes: maxBytes, onChunk: onChunk, decoder: newOutputDecoder()}
}

func (w *outputCollector) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.appendDecoded(w.decoder.Decode(data))
	return len(data), nil
}

func (w *outputCollector) appendDecoded(data []byte) {
	if len(data) == 0 {
		return
	}
	if w.onChunk != nil {
		copyOfData := append([]byte(nil), data...)
		w.onChunk(copyOfData)
	}
	w.total += int64(len(data))
	w.data = append(w.data, data...)
	if len(w.data) > w.maxBytes {
		overflow := len(w.data) - w.maxBytes
		copy(w.data, w.data[overflow:])
		w.data = w.data[:w.maxBytes]
	}
}

func (w *outputCollector) Snapshot() (string, string, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.appendDecoded(w.decoder.Flush())
	data := append([]byte(nil), w.data...)
	truncated := w.total > int64(len(data))
	output := string(data)
	if truncated {
		output = fmt.Sprintf("[Output truncated; showing last %d bytes.]\n%s", len(data), output)
	}
	return output, base64.StdEncoding.EncodeToString(data), truncated
}

func runManaged(ctx context.Context, command *exec.Cmd, input io.Reader) (int, error) {
	stdinReader, stdinWriter := io.Pipe()
	command.Stdin = stdinReader
	guard, err := startManagedCommand(command)
	if err != nil {
		stdinReader.Close()
		stdinWriter.Close()
		return -1, err
	}
	defer guard.Close()
	defer stdinReader.Close()
	go copyInput(stdinWriter, input)
	wait := make(chan error, 1)
	go func() { wait <- command.Wait() }()
	select {
	case err := <-wait:
		return commandExitCode(command, err), err
	case <-ctx.Done():
		guard.Kill()
		err := <-wait
		return -1, err
	}
}

func copyInput(destination *io.PipeWriter, source io.Reader) {
	_, err := io.Copy(destination, source)
	if err != nil {
		destination.CloseWithError(err)
		return
	}
	destination.Close()
}

func commandExitCode(command *exec.Cmd, waitErr error) int {
	if command.ProcessState != nil {
		return command.ProcessState.ExitCode()
	}
	if waitErr == nil {
		return 0
	}
	return -1
}
