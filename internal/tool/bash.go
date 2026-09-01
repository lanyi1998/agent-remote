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
	command := exec.Command(runtimePaths.Bash, "--noprofile", "--norc", "-s")
	command.Dir = s.paths.Root()
	command.Env = shellEnvironment(runtimePaths.Root, runtimePaths.BusyBox, s.paths.Root())
	command.Stdout = collector
	command.Stderr = collector
	script := buildScript(input.Command, runtimePaths.BusyBox != "")
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

func shellEnvironment(runtimeRoot, busyBox, workingDirectory string) []string {
	environment := append([]string(nil), os.Environ()...)
	environment = setEnvironment(environment, "PWD", shellPath(workingDirectory))
	if runtimeRoot != "" {
		pathValue := strings.Join([]string{
			filepath.Join(runtimeRoot, "usr", "bin"),
			filepath.Join(runtimeRoot, "bin"),
			filepath.Join(runtimeRoot, "mingw64", "bin"),
			os.Getenv("PATH"),
		}, string(os.PathListSeparator))
		environment = setEnvironment(environment, "PATH", pathValue)
	}
	if busyBox != "" {
		environment = setEnvironment(environment, "PI_BUSYBOX", shellPath(busyBox))
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
	if runtime.GOARCH == "386" {
		return "MINGW32"
	}
	return "MINGW64"
}

func shellPath(name string) string {
	value := filepath.ToSlash(name)
	if runtime.GOOS == "windows" && len(value) >= 3 && value[1] == ':' && value[2] == '/' {
		drive := strings.ToLower(value[:1])
		return "/" + drive + value[2:]
	}
	return value
}

func buildScript(command string, withBusyBox bool) string {
	var script strings.Builder
	if withBusyBox {
		script.WriteString(busyBoxPrologue)
		script.WriteByte('\n')
	}
	script.WriteString(command)
	if !strings.HasSuffix(command, "\n") {
		script.WriteByte('\n')
	}
	return script.String()
}

const busyBoxPrologue = `
if [ -n "${PI_BUSYBOX:-}" ]; then
  awk()      { "$PI_BUSYBOX" awk "$@"; }
  base64()   { "$PI_BUSYBOX" base64 "$@"; }
  basename() { "$PI_BUSYBOX" basename "$@"; }
  cat()      { "$PI_BUSYBOX" cat "$@"; }
  cp()       { "$PI_BUSYBOX" cp "$@"; }
  cut()      { "$PI_BUSYBOX" cut "$@"; }
  date()     { "$PI_BUSYBOX" date "$@"; }
  diff()     { "$PI_BUSYBOX" diff "$@"; }
  dirname()  { "$PI_BUSYBOX" dirname "$@"; }
  find()     { "$PI_BUSYBOX" find "$@"; }
  grep()     { "$PI_BUSYBOX" grep "$@"; }
  gzip()     { "$PI_BUSYBOX" gzip "$@"; }
  gunzip()   { "$PI_BUSYBOX" gunzip "$@"; }
  head()     { "$PI_BUSYBOX" head "$@"; }
  ls()       { "$PI_BUSYBOX" ls "$@"; }
  md5sum()   { "$PI_BUSYBOX" md5sum "$@"; }
  mkdir()    { "$PI_BUSYBOX" mkdir "$@"; }
  mv()       { "$PI_BUSYBOX" mv "$@"; }
  patch()    { "$PI_BUSYBOX" patch "$@"; }
  readlink() { "$PI_BUSYBOX" readlink "$@"; }
  realpath() { "$PI_BUSYBOX" realpath "$@"; }
  rm()       { "$PI_BUSYBOX" rm "$@"; }
  rmdir()    { "$PI_BUSYBOX" rmdir "$@"; }
  sed()      { "$PI_BUSYBOX" sed "$@"; }
  sha256sum(){ "$PI_BUSYBOX" sha256sum "$@"; }
  sleep()    { "$PI_BUSYBOX" sleep "$@"; }
  sort()     { "$PI_BUSYBOX" sort "$@"; }
  stat()     { "$PI_BUSYBOX" stat "$@"; }
  tail()     { "$PI_BUSYBOX" tail "$@"; }
  tar()      { "$PI_BUSYBOX" tar "$@"; }
  tee()      { "$PI_BUSYBOX" tee "$@"; }
  touch()    { "$PI_BUSYBOX" touch "$@"; }
  tr()       { "$PI_BUSYBOX" tr "$@"; }
  uname()    { "$PI_BUSYBOX" uname "$@"; }
  uniq()     { "$PI_BUSYBOX" uniq "$@"; }
  unzip()    { "$PI_BUSYBOX" unzip "$@"; }
  wc()       { "$PI_BUSYBOX" wc "$@"; }
  which()    { "$PI_BUSYBOX" which "$@"; }
  xargs()    { "$PI_BUSYBOX" xargs "$@"; }
  export -f awk base64 basename cat cp cut date diff dirname find grep gzip gunzip
  export -f head ls md5sum mkdir mv patch readlink realpath rm rmdir sed sha256sum
  export -f sleep sort stat tail tar tee touch tr uname uniq unzip wc which xargs
fi`

type outputCollector struct {
	mu       sync.Mutex
	data     []byte
	maxBytes int
	total    int64
	onChunk  ChunkWriter
}

func newOutputCollector(maxBytes int, onChunk ChunkWriter) *outputCollector {
	return &outputCollector{maxBytes: maxBytes, onChunk: onChunk}
}

func (w *outputCollector) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
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
	return len(data), nil
}

func (w *outputCollector) Snapshot() (string, string, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
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
