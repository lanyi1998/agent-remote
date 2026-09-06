package transport

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
)

const (
	SecureVersion              = 1
	PurposeHTTPClient          = "http-client"
	PurposeHTTPServer          = "http-server"
	PurposeWorkerToGateway     = "worker-to-gateway"
	PurposeGatewayToWorker     = "gateway-to-worker"
	PurposeTerminalClient      = "terminal-client"
	PurposeTerminalGateway     = "terminal-gateway"
	WorkerToGatewayAAD         = "websocket/v1\nworker-to-gateway"
	GatewayToWorkerAAD         = "websocket/v1\ngateway-to-worker"
	TerminalClientToGatewayAAD = "websocket/v1\nterminal-client-to-gateway"
	TerminalGatewayToClientAAD = "websocket/v1\nterminal-gateway-to-client"
)

type Envelope struct {
	Version    int    `json:"v"`
	Nonce      string `json:"nonce"`
	Ciphertext string `json:"ciphertext"`
}

func EncryptEnvelope(token, purpose, aad string, plaintext []byte) (Envelope, error) {
	block, err := aes.NewCipher(deriveKey(token, purpose))
	if err != nil {
		return Envelope{}, fmt.Errorf("create secure cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return Envelope{}, fmt.Errorf("create secure AEAD: %w", err)
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return Envelope{}, fmt.Errorf("generate secure nonce: %w", err)
	}
	ciphertext := aead.Seal(nil, nonce, plaintext, []byte(aad))
	return Envelope{
		Version:    SecureVersion,
		Nonce:      base64.RawURLEncoding.EncodeToString(nonce),
		Ciphertext: base64.RawURLEncoding.EncodeToString(ciphertext),
	}, nil
}

func DecryptEnvelope(token, purpose, aad string, envelope Envelope) ([]byte, error) {
	if envelope.Version != SecureVersion {
		return nil, fmt.Errorf("unsupported secure envelope version %d", envelope.Version)
	}
	nonce, err := base64.RawURLEncoding.DecodeString(envelope.Nonce)
	if err != nil {
		return nil, fmt.Errorf("decode secure nonce: %w", err)
	}
	ciphertext, err := base64.RawURLEncoding.DecodeString(envelope.Ciphertext)
	if err != nil {
		return nil, fmt.Errorf("decode secure ciphertext: %w", err)
	}
	block, err := aes.NewCipher(deriveKey(token, purpose))
	if err != nil {
		return nil, fmt.Errorf("create secure cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create secure AEAD: %w", err)
	}
	if len(nonce) != aead.NonceSize() {
		return nil, errors.New("invalid secure nonce length")
	}
	plaintext, err := aead.Open(nil, nonce, ciphertext, []byte(aad))
	if err != nil {
		return nil, errors.New("secure envelope authentication failed")
	}
	return plaintext, nil
}

func MarshalEnvelope(envelope Envelope) ([]byte, error) {
	return json.Marshal(envelope)
}

func UnmarshalEnvelope(data []byte) (Envelope, error) {
	var envelope Envelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return Envelope{}, fmt.Errorf("decode secure envelope: %w", err)
	}
	return envelope, nil
}
