package protocol

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
)

const Version = 1

const (
	MessageHello    = "hello"
	MessageRequest  = "request"
	MessageChunk    = "chunk"
	MessageResponse = "response"
	MessageCancel   = "cancel"
)

type RPCRequest struct {
	ID     string          `json:"id,omitempty"`
	Target string          `json:"target,omitempty"`
	Tool   string          `json:"tool"`
	Input  json.RawMessage `json:"input"`
}

type RPCResponse struct {
	ID     string          `json:"id"`
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *RPCError       `json:"error,omitempty"`
}

type RPCError struct {
	Code    string          `json:"code"`
	Message string          `json:"message"`
	Details json.RawMessage `json:"details,omitempty"`
}

type Hello struct {
	ProtocolVersion int      `json:"protocol_version"`
	WorkerID        string   `json:"worker_id"`
	OS              string   `json:"os"`
	Arch            string   `json:"arch"`
	Hostname        string   `json:"hostname"`
	Root            string   `json:"root"`
	Tools           []string `json:"tools"`
	ShellProfile    string   `json:"shell_profile"`
}

type WireMessage struct {
	Type    string        `json:"type"`
	ID      string        `json:"id,omitempty"`
	Hello   *Hello        `json:"hello,omitempty"`
	Request *WireRequest  `json:"request,omitempty"`
	Chunk   *WireChunk    `json:"chunk,omitempty"`
	Result  *WireResponse `json:"response,omitempty"`
}

type WireRequest struct {
	Tool       string `json:"tool"`
	PayloadB64 string `json:"payload_b64"`
}

type WireChunk struct {
	DataB64 string `json:"data_b64"`
}

type WireResponse struct {
	ResultB64 string    `json:"result_b64,omitempty"`
	Error     *RPCError `json:"error,omitempty"`
}

func EncodePayload(data []byte) string {
	return base64.StdEncoding.EncodeToString(data)
}

func DecodePayload(value string) ([]byte, error) {
	data, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return nil, fmt.Errorf("decode base64 payload: %w", err)
	}
	return data, nil
}
