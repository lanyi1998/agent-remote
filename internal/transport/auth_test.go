package transport

import (
	"bytes"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRequestAuthenticationDoesNotTransmitToken(t *testing.T) {
	token := "a-strong-random-token-for-tests-only"
	body := []byte(`{"encrypted":"payload"}`)
	now := time.Unix(1_700_000_000, 0)
	t.Log(requestProof(token, "GET", "/v1/targets", "1700000000", "BwcHBwcHBwcHBwcHBwcHBw", nil))
	header, err := ClientAuthHeaders(token, "POST", "/v1/rpc", body, now)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("POST", "http://gateway.test/v1/rpc", bytes.NewReader(body))
	request.Header = header
	if request.Header.Get("Authorization") != "" {
		t.Fatal("raw Authorization token must not be sent")
	}

	replay := NewReplayCache()
	if _, err := VerifyRequest(request, token, body, replay, now); err != nil {
		t.Fatalf("verify request: %v", err)
	}
	if _, err := VerifyRequest(request, token, body, replay, now); err == nil {
		t.Fatal("replayed request was accepted")
	}
}

func TestRequestAuthenticationBindsBodyAndPath(t *testing.T) {
	token := "a-strong-random-token-for-tests-only"
	now := time.Unix(1_700_000_000, 0)
	body := []byte("ciphertext")
	header, err := ClientAuthHeaders(token, "POST", "/v1/rpc", body, now)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("POST", "http://gateway.test/v1/rpc", nil)
	request.Header = header
	if _, err := VerifyRequest(request, token, []byte("modified"), NewReplayCache(), now); err == nil {
		t.Fatal("modified body was accepted")
	}
}
