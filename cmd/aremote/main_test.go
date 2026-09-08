package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestParseGlobalOptionsParsesClientFlags(t *testing.T) {
	options, command, arguments, err := parseGlobalOptions([]string{
		"--raw",
		"--request-timeout", "2m",
		"read", "hello.txt",
	}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if !options.raw || options.requestTimeout.String() != "2m0s" {
		t.Fatalf("unexpected options: %+v", options)
	}
	if command != "read" || len(arguments) != 1 || arguments[0] != "hello.txt" {
		t.Fatalf("command = %q, arguments = %#v", command, arguments)
	}
}

func TestParseGlobalOptionsIgnoresConnectionEnvironment(t *testing.T) {
	t.Setenv("AGENT_REMOTE_URL", "http://env.example")
	t.Setenv("AGENT_REMOTE_TOKEN", "environment-token")
	t.Setenv("AGENT_REMOTE_TARGET", "environment-worker")
	options, command, _, err := parseGlobalOptions([]string{"status"}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if options.url != "" || options.token != "" || options.worker != "" || command != "status" {
		t.Fatalf("unexpected parse result: %+v, %q", options, command)
	}
}

func TestArgumentsOrStdin(t *testing.T) {
	value, err := argumentsOrStdin(nil, strings.NewReader("echo from stdin\n"))
	if err != nil || value != "echo from stdin\n" {
		t.Fatalf("value = %q, err = %v", value, err)
	}
	value, err = argumentsOrStdin([]string{"echo", "from", "args"}, strings.NewReader("ignored"))
	if err != nil || value != "echo from args" {
		t.Fatalf("value = %q, err = %v", value, err)
	}
}

func TestExitCodePreservesRemoteCommandStatus(t *testing.T) {
	if code := exitCode(&exitError{code: 23}, &bytes.Buffer{}); code != 23 {
		t.Fatalf("exit code = %d", code)
	}
	if code := exitCode(&exitError{code: 256}, &bytes.Buffer{}); code != 1 {
		t.Fatalf("normalized exit code = %d", code)
	}
}

func TestExitCodeWritesJSONError(t *testing.T) {
	var output bytes.Buffer
	if code := exitCode(errors.New("boom"), &output); code != 1 {
		t.Fatalf("exit code = %d", code)
	}
	if !strings.Contains(output.String(), `"ok": false`) || !strings.Contains(output.String(), `"error": "boom"`) {
		t.Fatalf("error output = %s", output.String())
	}
}

func TestRunDispatchesServerSubcommand(t *testing.T) {
	err := run([]string{"server"}, streams{in: strings.NewReader(""), out: &bytes.Buffer{}, err: &bytes.Buffer{}})
	if err == nil || !strings.Contains(err.Error(), "shared token") {
		t.Fatalf("server error = %v", err)
	}
}

func TestRunServerHelpSucceeds(t *testing.T) {
	if err := run([]string{"server", "--help"}, streams{in: strings.NewReader(""), out: &bytes.Buffer{}, err: &bytes.Buffer{}}); err != nil {
		t.Fatalf("server help error = %v", err)
	}
}
