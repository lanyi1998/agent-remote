package transport

import (
	"bytes"
	"encoding/base64"
	"testing"
)

func TestEnvelopeRoundTrip(t *testing.T) {
	token := "a-strong-random-token-for-tests-only"
	plaintext := []byte("remote command output")
	envelope, err := EncryptEnvelope(token, PurposeHTTPClient, "request-aad", plaintext)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecryptEnvelope(token, PurposeHTTPClient, "request-aad", envelope)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decoded, plaintext) {
		t.Fatalf("decoded payload = %q, want %q", decoded, plaintext)
	}
}

func TestEnvelopeRejectsTampering(t *testing.T) {
	token := "a-strong-random-token-for-tests-only"
	envelope, err := EncryptEnvelope(token, PurposeHTTPClient, "request-aad", []byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	wrongAADEnvelope := envelope
	ciphertext, err := base64.RawURLEncoding.DecodeString(envelope.Ciphertext)
	if err != nil {
		t.Fatal(err)
	}
	ciphertext[0] ^= 1
	envelope.Ciphertext = base64.RawURLEncoding.EncodeToString(ciphertext)
	if _, err := DecryptEnvelope(token, PurposeHTTPClient, "request-aad", envelope); err == nil {
		t.Fatal("tampered ciphertext was accepted")
	}
	if _, err := DecryptEnvelope(token, PurposeHTTPClient, "different-aad", wrongAADEnvelope); err == nil {
		t.Fatal("wrong associated data was accepted")
	}
}
