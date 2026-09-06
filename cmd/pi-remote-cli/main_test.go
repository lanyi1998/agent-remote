package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestParseGlobalOptionsUsesEnvironmentAndFlags(t *testing.T) {
	t.Setenv("PI_REMOTE_URL", "https://env.example")
	t.Setenv("PI_REMOTE_TOKEN", "environment-token")
	t.Setenv("PI_REMOTE_TARGET", "environment-worker")
	options, command, arguments, err := parseGlobalOptions([]string{
		"--url", "https://flag.example",
		"--token", "flag-token",
		"--target", "flag-worker",
		"read", "hello.txt",
	}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if options.url != "https://flag.example" || options.token != "flag-token" || options.target != "flag-worker" {
		t.Fatalf("unexpected options: %+v", options)
	}
	if command != "read" || len(arguments) != 1 || arguments[0] != "hello.txt" {
		t.Fatalf("command = %q, arguments = %#v", command, arguments)
	}
}

func TestParseGlobalOptionsDefaultsToEnvironment(t *testing.T) {
	t.Setenv("PI_REMOTE_URL", "https://env.example")
	t.Setenv("PI_REMOTE_TOKEN", "environment-token")
	t.Setenv("PI_REMOTE_TARGET", "environment-worker")
	options, command, _, err := parseGlobalOptions([]string{"targets"}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if options.url != "https://env.example" || options.token != "environment-token" || options.target != "environment-worker" || command != "targets" {
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
