package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"pi-remote/internal/protocol"
	"pi-remote/internal/transport"
)

const maxResponseBytes = 64 * 1024 * 1024

type Config struct {
	BaseURL    string
	Token      string
	HTTPClient *http.Client
}

type Client struct {
	baseURL    *url.URL
	token      string
	httpClient *http.Client
}

type Targets struct {
	Remote     bool             `json:"remote"`
	RemoteInfo protocol.Hello   `json:"remote_info"`
	Workers    []protocol.Hello `json:"workers"`
}

type RPCError struct {
	StatusCode int
	Code       string
	Message    string
	Details    json.RawMessage
}

func (e *RPCError) Error() string {
	if e.Code == "" {
		return e.Message
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func New(config Config) (*Client, error) {
	if strings.TrimSpace(config.BaseURL) == "" {
		return nil, errors.New("remote URL is required")
	}
	if len(config.Token) < 8 {
		return nil, errors.New("a shared token of at least 8 characters is required")
	}
	baseURL, err := url.Parse(strings.TrimRight(config.BaseURL, "/"))
	if err != nil {
		return nil, fmt.Errorf("parse remote URL: %w", err)
	}
	if baseURL.Scheme != "http" {
		return nil, errors.New("remote URL must use http")
	}
	if baseURL.Host == "" {
		return nil, errors.New("remote URL must include a host")
	}
	if baseURL.Path != "" && baseURL.Path != "/" {
		return nil, errors.New("remote URL must not include a path")
	}
	if baseURL.RawQuery != "" || baseURL.Fragment != "" {
		return nil, errors.New("remote URL must not include a query or fragment")
	}
	httpClient := config.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 0}
	}
	return &Client{baseURL: baseURL, token: config.Token, httpClient: httpClient}, nil
}

func (c *Client) Targets(ctx context.Context) (Targets, error) {
	var targets Targets
	if err := c.request(ctx, http.MethodGet, "/v1/targets", nil, &targets); err != nil {
		return Targets{}, err
	}
	return targets, nil
}

func (c *Client) Call(ctx context.Context, target, tool string, input interface{}) (json.RawMessage, error) {
	inputJSON, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("encode tool input: %w", err)
	}
	request := protocol.RPCRequest{Target: target, Tool: tool, Input: inputJSON}
	var response protocol.RPCResponse
	if err := c.request(ctx, http.MethodPost, "/v1/rpc", request, &response); err != nil {
		return nil, err
	}
	if !response.OK || response.Error != nil {
		return nil, newRPCError(http.StatusOK, response.Error)
	}
	return response.Result, nil
}

func (c *Client) request(ctx context.Context, method, path string, input, output interface{}) error {
	plaintext, err := encodeRequestBody(input)
	if err != nil {
		return err
	}
	requestNonce, err := transport.NewRequestNonce()
	if err != nil {
		return err
	}
	requestURL := *c.baseURL
	requestURL.Path = path
	requestURL.RawPath = ""
	requestPath := requestURL.EscapedPath()
	wireBody, err := c.encryptRequest(method, requestPath, requestNonce, plaintext)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, method, requestURL.String(), bytes.NewReader(wireBody))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	headers, err := transport.ClientAuthHeadersWithNonce(c.token, method, requestPath, wireBody, requestNonce, time.Now())
	if err != nil {
		return err
	}
	request.Header = headers
	if len(wireBody) > 0 {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("request gateway: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return fmt.Errorf("read gateway response: %w", err)
	}
	if len(body) > maxResponseBytes {
		return errors.New("gateway response is too large")
	}
	body, err = c.decryptResponse(method, requestPath, requestNonce, response, body)
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return decodeHTTPError(response.StatusCode, body)
	}
	if err := json.Unmarshal(body, output); err != nil {
		return fmt.Errorf("decode gateway response: %w", err)
	}
	return nil
}

func encodeRequestBody(input interface{}) ([]byte, error) {
	if input == nil {
		return nil, nil
	}
	body, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}
	return body, nil
}

func (c *Client) encryptRequest(method, path, requestNonce string, plaintext []byte) ([]byte, error) {
	if len(plaintext) == 0 {
		return nil, nil
	}
	envelope, err := transport.EncryptEnvelope(c.token, transport.PurposeHTTPClient, httpAAD("request", method, path, requestNonce), plaintext)
	if err != nil {
		return nil, err
	}
	return transport.MarshalEnvelope(envelope)
}

func (c *Client) decryptResponse(method, path, requestNonce string, response *http.Response, body []byte) ([]byte, error) {
	if response.Header.Get("X-Pi-Remote-Encrypted") != "1" {
		return body, nil
	}
	envelope, err := transport.UnmarshalEnvelope(body)
	if err != nil {
		return nil, fmt.Errorf("decode secure response: %w", err)
	}
	plaintext, err := transport.DecryptEnvelope(c.token, transport.PurposeHTTPServer, httpAAD("response", method, path, requestNonce), envelope)
	if err != nil {
		return nil, fmt.Errorf("decrypt secure response: %w", err)
	}
	return plaintext, nil
}

func decodeHTTPError(statusCode int, body []byte) error {
	var response protocol.RPCResponse
	if err := json.Unmarshal(body, &response); err == nil && response.Error != nil {
		return newRPCError(statusCode, response.Error)
	}
	var generic struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &generic); err == nil && generic.Error != "" {
		return &RPCError{StatusCode: statusCode, Message: generic.Error}
	}
	return &RPCError{StatusCode: statusCode, Message: fmt.Sprintf("gateway returned HTTP %d", statusCode)}
}

func newRPCError(statusCode int, rpcError *protocol.RPCError) error {
	if rpcError == nil {
		return &RPCError{StatusCode: statusCode, Message: "RPC returned no result"}
	}
	return &RPCError{StatusCode: statusCode, Code: rpcError.Code, Message: rpcError.Message, Details: rpcError.Details}
}

func httpAAD(direction, method, path, requestNonce string) string {
	return "http/v1\n" + direction + "\n" + method + "\n" + path + "\n" + requestNonce
}
