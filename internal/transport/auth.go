package transport

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"
)

const (
	AuthNonceHeader     = "X-Agent-Remote-Nonce"
	AuthTimestampHeader = "X-Agent-Remote-Timestamp"
	AuthProofHeader     = "X-Agent-Remote-Proof"

	authNonceBytes = 16
	authWindow     = 5 * time.Minute
	maxReplayItems = 10000
)

type RequestAuth struct {
	Nonce string
}

type ReplayCache struct {
	mu     sync.Mutex
	values map[string]time.Time
}

func NewReplayCache() *ReplayCache {
	return &ReplayCache{values: make(map[string]time.Time)}
}

func (c *ReplayCache) Accept(nonce string, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	for value, expiresAt := range c.values {
		if !expiresAt.After(now) {
			delete(c.values, value)
		}
	}
	if _, exists := c.values[nonce]; exists || len(c.values) >= maxReplayItems {
		return false
	}
	c.values[nonce] = now.Add(authWindow)
	return true
}

func ClientAuthHeaders(token, method, path string, body []byte, now time.Time) (http.Header, error) {
	nonce, err := NewRequestNonce()
	if err != nil {
		return nil, err
	}
	return ClientAuthHeadersWithNonce(token, method, path, body, nonce, now)
}

func NewRequestNonce() (string, error) {
	nonceBytes := make([]byte, authNonceBytes)
	if _, err := rand.Read(nonceBytes); err != nil {
		return "", fmt.Errorf("generate auth nonce: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(nonceBytes), nil
}

func ClientAuthHeadersWithNonce(token, method, path string, body []byte, nonce string, now time.Time) (http.Header, error) {
	nonceBytes, err := base64.RawURLEncoding.DecodeString(nonce)
	if err != nil || len(nonceBytes) != authNonceBytes {
		return nil, errors.New("invalid auth nonce")
	}
	timestamp := strconv.FormatInt(now.Unix(), 10)
	proof := requestProof(token, method, path, timestamp, nonce, body)
	return http.Header{
		AuthNonceHeader:     []string{nonce},
		AuthTimestampHeader: []string{timestamp},
		AuthProofHeader:     []string{proof},
	}, nil
}

func VerifyRequest(request *http.Request, expectedToken string, body []byte, replay *ReplayCache, now time.Time) (*RequestAuth, error) {
	nonce := request.Header.Get(AuthNonceHeader)
	timestampValue := request.Header.Get(AuthTimestampHeader)
	providedProof := request.Header.Get(AuthProofHeader)
	if nonce == "" || timestampValue == "" || providedProof == "" {
		return nil, errors.New("missing request authentication headers")
	}
	nonceBytes, err := base64.RawURLEncoding.DecodeString(nonce)
	if err != nil || len(nonceBytes) != authNonceBytes {
		return nil, errors.New("invalid request nonce")
	}
	timestamp, err := strconv.ParseInt(timestampValue, 10, 64)
	if err != nil || absDuration(now.Sub(time.Unix(timestamp, 0))) > authWindow {
		return nil, errors.New("request authentication timestamp is outside the valid window")
	}
	provided, err := base64.RawURLEncoding.DecodeString(providedProof)
	if err != nil || len(provided) != sha256.Size {
		return nil, errors.New("invalid request proof")
	}
	expected := requestProofBytes(expectedToken, request.Method, request.URL.EscapedPath(), timestampValue, nonce, body)
	if subtle.ConstantTimeCompare(provided, expected) != 1 {
		return nil, errors.New("invalid request proof")
	}
	if replay != nil && !replay.Accept(nonce, now) {
		return nil, errors.New("request authentication nonce was already used")
	}
	return &RequestAuth{Nonce: nonce}, nil
}

func requestProof(token, method, path, timestamp, nonce string, body []byte) string {
	return base64.RawURLEncoding.EncodeToString(requestProofBytes(token, method, path, timestamp, nonce, body))
}

func requestProofBytes(token, method, path, timestamp, nonce string, body []byte) []byte {
	mac := hmac.New(sha256.New, []byte(token))
	mac.Write([]byte(method))
	mac.Write([]byte{'\n'})
	mac.Write([]byte(path))
	mac.Write([]byte{'\n'})
	mac.Write([]byte(timestamp))
	mac.Write([]byte{'\n'})
	mac.Write([]byte(nonce))
	mac.Write([]byte{'\n'})
	mac.Write([]byte(base64.RawURLEncoding.EncodeToString(body)))
	return mac.Sum(nil)
}

func absDuration(value time.Duration) time.Duration {
	if value < 0 {
		return -value
	}
	return value
}

func deriveKey(token, purpose string) []byte {
	hash := sha256.New()
	hash.Write([]byte("agent-remote/secure/v1/"))
	hash.Write([]byte(purpose))
	hash.Write([]byte{0})
	hash.Write([]byte(token))
	return hash.Sum(nil)
}
