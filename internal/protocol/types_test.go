package protocol

import (
	"bytes"
	"testing"
)

func TestPayloadRoundTrip(t *testing.T) {
	original := []byte("echo '$PATH'\r\n中文\x00\xff")
	decoded, err := DecodePayload(EncodePayload(original))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decoded, original) {
		t.Fatalf("payload changed: %q", decoded)
	}
}
