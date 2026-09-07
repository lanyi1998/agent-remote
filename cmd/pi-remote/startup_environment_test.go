package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestBuildClientURLReplacesWildcardHost(t *testing.T) {
	got, err := buildClientURL("0.0.0.0:8787", func() string { return "192.168.3.219" })
	if err != nil {
		t.Fatal(err)
	}
	if got != "http://192.168.3.219:8787" {
		t.Fatalf("client URL = %q", got)
	}
}

func TestPrintClientEnvironmentUsesShellQuoting(t *testing.T) {
	var output bytes.Buffer
	printClientEnvironment(&output, "gateway.example:8787", "token with 'quote'")
	want := "# ________________________ Gateway ________________________\n" +
		"\n" +
		"# Local CLI\n" +
		"export PI_REMOTE_URL='http://gateway.example:8787'\n" +
		"export PI_REMOTE_TOKEN='token with '\"'\"'quote'\"'\"''\n" +
		"\n" +
		"# Pi\n" +
		"/remote connect http://gateway.example:8787 token with 'quote'\n"
	if !strings.Contains(output.String(), want) {
		t.Fatalf("startup environment = %q, want substring %q", output.String(), want)
	}
}
