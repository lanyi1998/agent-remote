package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"pi-remote/internal/buildinfo"
	remoteclient "pi-remote/internal/client"
)

type globalOptions struct {
	url            string
	token          string
	target         string
	raw            bool
	requestTimeout time.Duration
}

type streams struct {
	in  io.Reader
	out io.Writer
	err io.Writer
}

type exitError struct {
	code int
}

type bashResult struct {
	Output   string `json:"output"`
	ExitCode int    `json:"exit_code"`
}

func (e *exitError) Error() string { return fmt.Sprintf("remote command exited with code %d", e.code) }

func main() {
	ioStreams := streams{in: os.Stdin, out: os.Stdout, err: os.Stderr}
	code := exitCode(run(os.Args[1:], ioStreams), ioStreams.err)
	if code != 0 {
		os.Exit(code)
	}
}

func run(arguments []string, ioStreams streams) error {
	options, command, commandArguments, err := parseGlobalOptions(arguments, ioStreams.err)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if command == "" || command == "help" {
		printUsage(ioStreams.out)
		return nil
	}
	if command == "version" {
		_, err := fmt.Fprintln(ioStreams.out, buildinfo.Version)
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if options.requestTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, options.requestTimeout)
		defer cancel()
	}
	if command == "mcp" {
		return runMCP(ctx, options, commandArguments, ioStreams)
	}
	client, err := remoteclient.New(remoteclient.Config{BaseURL: options.url, Token: options.token})
	if err != nil {
		return err
	}
	err = runCommand(ctx, client, options, command, commandArguments, ioStreams)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	return err
}

func parseGlobalOptions(arguments []string, errorOutput io.Writer) (globalOptions, string, []string, error) {
	flags := flag.NewFlagSet("pi-remote-cli", flag.ContinueOnError)
	flags.SetOutput(errorOutput)
	options := globalOptions{}
	flags.StringVar(&options.url, "url", os.Getenv("PI_REMOTE_URL"), "Gateway URL (or PI_REMOTE_URL)")
	flags.StringVar(&options.token, "token", os.Getenv("PI_REMOTE_TOKEN"), "shared token (or PI_REMOTE_TOKEN)")
	flags.StringVar(&options.target, "target", envOrDefault("PI_REMOTE_TARGET", "local"), "target Worker ID (or PI_REMOTE_TARGET)")
	flags.BoolVar(&options.raw, "raw", false, "print read/bash content instead of JSON")
	flags.DurationVar(&options.requestTimeout, "request-timeout", 0, "whole-request timeout, for example 2m (default: none)")
	flags.Usage = func() { printUsage(errorOutput) }
	if err := flags.Parse(arguments); err != nil {
		return globalOptions{}, "", nil, err
	}
	if options.requestTimeout < 0 {
		return globalOptions{}, "", nil, errors.New("--request-timeout must not be negative")
	}
	remaining := flags.Args()
	if len(remaining) == 0 {
		return options, "", nil, nil
	}
	return options, remaining[0], remaining[1:], nil
}

func runCommand(ctx context.Context, client *remoteclient.Client, options globalOptions, command string, arguments []string, ioStreams streams) error {
	switch command {
	case "targets":
		return runTargets(ctx, client, arguments, ioStreams.out)
	case "read":
		return runRead(ctx, client, options, arguments, ioStreams)
	case "find":
		return runFind(ctx, client, options.target, arguments, ioStreams.out, ioStreams.err)
	case "bash", "shell":
		return runBash(ctx, client, options, arguments, ioStreams)
	case "write":
		return runWrite(ctx, client, options.target, arguments, ioStreams)
	case "edit":
		return runEdit(ctx, client, options.target, arguments, ioStreams)
	case "rpc":
		return runRPC(ctx, client, options.target, arguments, ioStreams)
	default:
		return fmt.Errorf("unknown command %q", command)
	}
}

func runTargets(ctx context.Context, client *remoteclient.Client, arguments []string, output io.Writer) error {
	if len(arguments) != 0 {
		return errors.New("targets does not accept arguments")
	}
	result, err := client.Targets(ctx)
	if err != nil {
		return err
	}
	return writeJSON(output, result)
}

func runRead(ctx context.Context, client *remoteclient.Client, options globalOptions, arguments []string, ioStreams streams) error {
	flags := newCommandFlags("read", ioStreams.err)
	offset := flags.Int("offset", 0, "first line to read (1-indexed)")
	limit := flags.Int("limit", 0, "maximum lines to read")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("usage: pi-remote-cli [global flags] read [--offset N] [--limit N] PATH")
	}
	input := map[string]interface{}{"path": flags.Arg(0)}
	if *offset != 0 {
		input["offset"] = *offset
	}
	if *limit != 0 {
		input["limit"] = *limit
	}
	result, err := client.Call(ctx, options.target, "read", input)
	if err != nil {
		return err
	}
	if !options.raw {
		return writeRawJSON(ioStreams.out, result)
	}
	return writeReadContent(ioStreams.out, result)
}

func runFind(ctx context.Context, client *remoteclient.Client, target string, arguments []string, output, errorOutput io.Writer) error {
	flags := newCommandFlags("find", errorOutput)
	maxResults := flags.Int("max-results", 20, "maximum number of results")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() > 1 {
		return errors.New("usage: pi-remote-cli [global flags] find [--max-results N] [QUERY]")
	}
	query := ""
	if flags.NArg() == 1 {
		query = flags.Arg(0)
	}
	result, err := client.Call(ctx, target, "find", map[string]interface{}{"query": query, "max_results": *maxResults})
	if err != nil {
		return err
	}
	return writeRawJSON(output, result)
}

func runBash(ctx context.Context, client *remoteclient.Client, options globalOptions, arguments []string, ioStreams streams) error {
	flags := newCommandFlags("bash", ioStreams.err)
	timeout := flags.Float64("timeout", 0, "remote shell timeout in seconds")
	tty := flags.Bool("tty", false, "run through a remote pseudo-terminal and connect stdin/stdout")
	pty := flags.Bool("pty", false, "alias for --tty")
	encoding := flags.String("encoding", "", "remote command output encoding, for example utf-8, gb18030, or shift-jis")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if *tty && *pty {
		return errors.New("--tty and --pty cannot be used together")
	}
	interactive := *tty || *pty
	if interactive && flags.NArg() == 0 {
		return errors.New("interactive bash requires a command argument")
	}
	if interactive {
		command := strings.Join(flags.Args(), " ")
		restore, err := makeTerminalRaw(ioStreams.in)
		if err != nil {
			return err
		}
		defer restore()
		exitCode, err := client.OpenTerminal(ctx, remoteclient.TerminalOptions{
			Target:   options.target,
			Tool:     "bash",
			Command:  command,
			Timeout:  optionalTimeout(*timeout),
			Encoding: *encoding,
			Input:    ioStreams.in,
			Output:   ioStreams.out,
		})
		if err != nil {
			return err
		}
		if exitCode != 0 {
			return &exitError{code: exitCode}
		}
		return nil
	}
	command, err := argumentsOrStdin(flags.Args(), ioStreams.in)
	if err != nil {
		return err
	}
	if strings.TrimSpace(command) == "" {
		return errors.New("bash command is required as arguments or stdin")
	}
	input := map[string]interface{}{"command": command}
	if *timeout != 0 {
		input["timeout"] = *timeout
	}
	if *encoding != "" {
		input["encoding"] = *encoding
	}
	result, err := client.Call(ctx, options.target, "bash", input)
	if err != nil {
		return err
	}
	bashOutput, err := decodeBashResult(result)
	if err != nil {
		return err
	}
	if options.raw {
		err = writeBashOutput(ioStreams.out, bashOutput)
	} else {
		err = writeRawJSON(ioStreams.out, result)
	}
	if err != nil {
		return err
	}
	if bashOutput.ExitCode != 0 {
		return &exitError{code: bashOutput.ExitCode}
	}
	return nil
}

func optionalTimeout(value float64) *float64 {
	if value == 0 {
		return nil
	}
	return &value
}

func makeTerminalRaw(input io.Reader) (func(), error) {
	file, ok := input.(*os.File)
	if !ok || !term.IsTerminal(int(file.Fd())) {
		return func() {}, nil
	}
	state, err := term.MakeRaw(int(file.Fd()))
	if err != nil {
		return nil, fmt.Errorf("enable local terminal raw mode: %w", err)
	}
	return func() { _ = term.Restore(int(file.Fd()), state) }, nil
}

func runWrite(ctx context.Context, client *remoteclient.Client, target string, arguments []string, ioStreams streams) error {
	flags := newCommandFlags("write", ioStreams.err)
	content := flags.String("content", "", "complete UTF-8 content (default: read stdin)")
	contentFile := flags.String("content-file", "", "read complete content from a local file")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("usage: pi-remote-cli [global flags] write [--content TEXT|--content-file FILE] PATH")
	}
	value, err := readOptionValue(*content, *contentFile, ioStreams.in)
	if err != nil {
		return err
	}
	result, err := client.Call(ctx, target, "write", map[string]interface{}{"path": flags.Arg(0), "content": value})
	if err != nil {
		return err
	}
	return writeRawJSON(ioStreams.out, result)
}

func runEdit(ctx context.Context, client *remoteclient.Client, target string, arguments []string, ioStreams streams) error {
	flags := newCommandFlags("edit", ioStreams.err)
	editsJSON := flags.String("edits", "", "JSON array of {oldText,newText} replacements (default: read stdin)")
	editsFile := flags.String("edits-file", "", "read replacements JSON from a local file")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("usage: pi-remote-cli [global flags] edit [--edits JSON|--edits-file FILE] PATH")
	}
	value, err := readOptionValue(*editsJSON, *editsFile, ioStreams.in)
	if err != nil {
		return err
	}
	var edits []map[string]string
	if err := json.Unmarshal([]byte(value), &edits); err != nil {
		return fmt.Errorf("decode edits JSON: %w", err)
	}
	result, err := client.Call(ctx, target, "edit", map[string]interface{}{"path": flags.Arg(0), "edits": edits})
	if err != nil {
		return err
	}
	return writeRawJSON(ioStreams.out, result)
}

func runRPC(ctx context.Context, client *remoteclient.Client, target string, arguments []string, ioStreams streams) error {
	flags := newCommandFlags("rpc", ioStreams.err)
	inputJSON := flags.String("input", "", "tool input as JSON (default: read stdin)")
	inputFile := flags.String("input-file", "", "read tool input JSON from a local file")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("usage: pi-remote-cli [global flags] rpc [--input JSON|--input-file FILE] TOOL")
	}
	value, err := readOptionValue(*inputJSON, *inputFile, ioStreams.in)
	if err != nil {
		return err
	}
	var input json.RawMessage
	if err := json.Unmarshal([]byte(value), &input); err != nil {
		return fmt.Errorf("decode input JSON: %w", err)
	}
	result, err := client.Call(ctx, target, flags.Arg(0), input)
	if err != nil {
		return err
	}
	return writeRawJSON(ioStreams.out, result)
}

func newCommandFlags(name string, errorOutput io.Writer) *flag.FlagSet {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(errorOutput)
	return flags
}

func argumentsOrStdin(arguments []string, input io.Reader) (string, error) {
	if len(arguments) > 0 {
		return strings.Join(arguments, " "), nil
	}
	data, err := io.ReadAll(input)
	if err != nil {
		return "", fmt.Errorf("read stdin: %w", err)
	}
	return string(data), nil
}

func readOptionValue(value, filename string, input io.Reader) (string, error) {
	if value != "" && filename != "" {
		return "", errors.New("inline value and file cannot be used together")
	}
	if filename != "" {
		data, err := os.ReadFile(filename)
		if err != nil {
			return "", fmt.Errorf("read %s: %w", filename, err)
		}
		return string(data), nil
	}
	if value != "" {
		return value, nil
	}
	data, err := io.ReadAll(input)
	if err != nil {
		return "", fmt.Errorf("read stdin: %w", err)
	}
	return string(data), nil
}

func writeReadContent(output io.Writer, raw json.RawMessage) error {
	var result struct {
		Content       string `json:"content"`
		ContentBase64 string `json:"content_base64"`
		Encoding      string `json:"encoding"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return fmt.Errorf("decode read result: %w", err)
	}
	data := []byte(result.Content)
	if result.Encoding == "base64" {
		var err error
		data, err = base64.StdEncoding.DecodeString(result.ContentBase64)
		if err != nil {
			return fmt.Errorf("decode file content: %w", err)
		}
	}
	_, err := output.Write(data)
	return err
}

func decodeBashResult(raw json.RawMessage) (bashResult, error) {
	var result bashResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return bashResult{}, fmt.Errorf("decode bash result: %w", err)
	}
	return result, nil
}

func writeBashOutput(output io.Writer, result bashResult) error {
	_, err := io.WriteString(output, result.Output)
	return err
}

func writeRawJSON(output io.Writer, raw json.RawMessage) error {
	var value interface{}
	if err := json.Unmarshal(raw, &value); err != nil {
		return fmt.Errorf("decode result JSON: %w", err)
	}
	return writeJSON(output, value)
}

func writeJSON(output io.Writer, value interface{}) error {
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}

func exitCode(err error, errorOutput io.Writer) int {
	if err == nil {
		return 0
	}
	var commandExit *exitError
	if errors.As(err, &commandExit) {
		return normalizeExitCode(commandExit.code)
	}
	writeError(errorOutput, err)
	return 1
}

func writeError(output io.Writer, err error) {
	value := map[string]interface{}{"ok": false, "error": err.Error()}
	var rpcError *remoteclient.RPCError
	if errors.As(err, &rpcError) {
		if rpcError.Code != "" {
			value["code"] = rpcError.Code
		}
		if rpcError.StatusCode != 0 {
			value["status"] = rpcError.StatusCode
		}
		if len(rpcError.Details) > 0 {
			var details interface{}
			if json.Unmarshal(rpcError.Details, &details) == nil {
				value["details"] = details
			}
		}
	}
	_ = writeJSON(output, value)
}

func normalizeExitCode(code int) int {
	if code < 1 || code > 255 {
		return 1
	}
	return code
}

func envOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func printUsage(output io.Writer) {
	fmt.Fprintln(output, `pi-remote-cli - control a pi-remote Gateway from scripts and other agents

Usage:
  pi-remote-cli [global flags] targets
  pi-remote-cli [global flags] read [--offset N] [--limit N] PATH
  pi-remote-cli [global flags] find [--max-results N] [QUERY]
  pi-remote-cli [global flags] bash [--timeout SEC] [--tty] [--encoding NAME] [COMMAND...]
  pi-remote-cli [global flags] shell [--timeout SEC] [--tty] [--encoding NAME] [COMMAND...]
  pi-remote-cli [global flags] write [--content TEXT|--content-file FILE] PATH
  pi-remote-cli [global flags] edit [--edits JSON|--edits-file FILE] PATH
  pi-remote-cli [global flags] rpc [--input JSON|--input-file FILE] TOOL
  pi-remote-cli [global flags] mcp

Global flags (must precede the command):
  --url URL              Gateway URL; defaults to PI_REMOTE_URL
  --token TOKEN          shared token; defaults to PI_REMOTE_TOKEN
  --target ID            local or Worker ID; defaults to PI_REMOTE_TARGET or local
  --raw                  print raw content for read and bash
  --request-timeout D    whole-request timeout such as 30s or 2m

When inline/file input is omitted, bash, write, edit, and rpc read stdin.
All normal output is JSON unless --raw is used with read or bash.
Interactive bash writes terminal output directly to stdout.`)
}
