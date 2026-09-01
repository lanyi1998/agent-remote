package worker

import "testing"

func TestNormalizeServerURL(t *testing.T) {
	value, err := NormalizeServerURL("https://gateway.example")
	if err != nil {
		t.Fatal(err)
	}
	if value != "wss://gateway.example/v1/workers/connect" {
		t.Fatalf("unexpected URL %q", value)
	}
}
