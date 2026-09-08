package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"agent-remote/internal/buildinfo"
	remoteclient "agent-remote/internal/client"
)

const mcpProtocolVersion = "2025-11-25"

var supportedMCPProtocolVersions = map[string]struct{}{
	"2024-11-05":       {},
	"2025-03-26":       {},
	"2025-06-18":       {},
	mcpProtocolVersion: {},
}

type mcpRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type mcpResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  interface{}     `json:"result,omitempty"`
	Error   *mcpError       `json:"error,omitempty"`
}

type mcpError struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

type mcpTool struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema interface{} `json:"inputSchema"`
}

type mcpTextContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type mcpToolResult struct {
	Content           []mcpTextContent `json:"content"`
	StructuredContent interface{}      `json:"structuredContent,omitempty"`
	IsError           bool             `json:"isError,omitempty"`
}

type mcpServer struct {
	url           string
	token         string
	defaultTarget string
	initialized   bool
}

func runMCP(ctx context.Context, options globalOptions, arguments []string, ioStreams streams) error {
	if len(arguments) != 0 {
		return errors.New("usage: aremote [global flags] mcp")
	}
	server := mcpServer{url: options.url, token: options.token, defaultTarget: options.target}
	return server.serve(ctx, ioStreams.in, ioStreams.out)
}

func (s *mcpServer) serve(ctx context.Context, input io.Reader, output io.Writer) error {
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 64*1024), 64*1024*1024)
	encoder := json.NewEncoder(output)
	for scanner.Scan() {
		messages := s.handleMessage(ctx, scanner.Bytes())
		for _, message := range messages {
			if err := encoder.Encode(message); err != nil {
				return fmt.Errorf("write MCP response: %w", err)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read MCP request: %w", err)
	}
	return nil
}

func (s *mcpServer) handleMessage(ctx context.Context, raw []byte) []interface{} {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return nil
	}
	if strings.HasPrefix(trimmed, "[") {
		responses := s.handleBatch(ctx, []byte(trimmed))
		if len(responses) == 0 {
			return nil
		}
		return []interface{}{responses}
	}
	response, ok := s.handleRequest(ctx, []byte(trimmed))
	if !ok {
		return nil
	}
	return []interface{}{response}
}

func (s *mcpServer) handleBatch(ctx context.Context, raw []byte) []mcpResponse {
	var requests []json.RawMessage
	if err := json.Unmarshal(raw, &requests); err != nil || len(requests) == 0 {
		return []mcpResponse{mcpInvalidRequest(nil, "invalid JSON-RPC batch")}
	}
	responses := make([]mcpResponse, 0, len(requests))
	for _, request := range requests {
		response, ok := s.handleRequest(ctx, request)
		if ok {
			responses = append(responses, response)
		}
	}
	return responses
}

func (s *mcpServer) handleRequest(ctx context.Context, raw []byte) (mcpResponse, bool) {
	var request mcpRequest
	if err := json.Unmarshal(raw, &request); err != nil || request.JSONRPC != "2.0" || request.Method == "" {
		return mcpInvalidRequest(nil, "invalid JSON-RPC request"), true
	}
	if len(request.ID) == 0 {
		s.handleNotification(request)
		return mcpResponse{}, false
	}
	if string(request.ID) == "null" {
		return mcpInvalidRequest(request.ID, "request id must not be null"), true
	}
	return s.handleCall(ctx, request), true
}

func (s *mcpServer) handleNotification(request mcpRequest) {
	if request.Method == "notifications/initialized" {
		return
	}
}

func (s *mcpServer) handleCall(ctx context.Context, request mcpRequest) mcpResponse {
	switch request.Method {
	case "initialize":
		return s.initialize(request)
	case "ping":
		return mcpSuccess(request.ID, map[string]interface{}{})
	case "tools/list":
		if !s.initialized {
			return mcpNotInitialized(request.ID)
		}
		return mcpSuccess(request.ID, map[string]interface{}{"tools": mcpTools()})
	case "tools/call":
		if !s.initialized {
			return mcpNotInitialized(request.ID)
		}
		return s.callTool(ctx, request)
	default:
		return mcpFailure(request.ID, -32601, "method not found", nil)
	}
}

func (s *mcpServer) initialize(request mcpRequest) mcpResponse {
	if s.initialized {
		return mcpFailure(request.ID, -32600, "initialize may only be called once", nil)
	}
	var params struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := json.Unmarshal(request.Params, &params); err != nil || params.ProtocolVersion == "" {
		return mcpFailure(request.ID, -32602, "initialize requires protocolVersion", nil)
	}
	if _, ok := supportedMCPProtocolVersions[params.ProtocolVersion]; !ok {
		return mcpFailure(request.ID, -32602, "unsupported protocol version", map[string]interface{}{
			"supported": supportedMCPVersions(),
			"requested": params.ProtocolVersion,
		})
	}
	s.initialized = true
	return mcpSuccess(request.ID, map[string]interface{}{
		"protocolVersion": params.ProtocolVersion,
		"capabilities":    map[string]interface{}{"tools": map[string]interface{}{}},
		"serverInfo": map[string]string{
			"name":    "aremote",
			"version": buildinfo.Version,
		},
		"instructions": "Use these tools only for work on the selected agent-remote Gateway or Worker.",
	})
}

func supportedMCPVersions() []string {
	return []string{mcpProtocolVersion, "2025-06-18", "2025-03-26", "2024-11-05"}
}

func (s *mcpServer) callTool(ctx context.Context, request mcpRequest) mcpResponse {
	var params struct {
		Name      string                 `json:"name"`
		Arguments map[string]interface{} `json:"arguments"`
	}
	if err := json.Unmarshal(request.Params, &params); err != nil || params.Name == "" {
		return mcpFailure(request.ID, -32602, "tools/call requires a tool name", nil)
	}
	result, err := s.executeTool(ctx, params.Name, params.Arguments)
	if err != nil {
		return mcpSuccess(request.ID, mcpErrorResult(err))
	}
	return mcpSuccess(request.ID, mcpSuccessResult(result))
}

func (s *mcpServer) executeTool(ctx context.Context, name string, arguments map[string]interface{}) (interface{}, error) {
	if arguments == nil {
		arguments = map[string]interface{}{}
	}
	toolName, ok := mcpGatewayToolName(name)
	if !ok {
		return nil, fmt.Errorf("unknown tool %q", name)
	}
	switch toolName {
	case "targets":
		client, err := s.remoteClient()
		if err != nil {
			return nil, err
		}
		return client.Targets(ctx)
	case "read", "find", "bash", "write", "edit":
		client, err := s.remoteClient()
		if err != nil {
			return nil, err
		}
		target := mcpTarget(arguments, s.defaultTarget)
		input, err := mcpToolInput(toolName, arguments)
		if err != nil {
			return nil, err
		}
		result, err := client.Call(ctx, target, toolName, input)
		if err != nil {
			return nil, err
		}
		return mcpDecodeResult(toolName, result)
	}
	return nil, fmt.Errorf("unsupported tool %q", toolName)
}

func mcpGatewayToolName(name string) (string, bool) {
	switch name {
	case "remote_targets":
		return "targets", true
	case "remote_read":
		return "read", true
	case "remote_find":
		return "find", true
	case "remote_bash":
		return "bash", true
	case "remote_write":
		return "write", true
	case "remote_edit":
		return "edit", true
	default:
		return "", false
	}
}

func (s *mcpServer) remoteClient() (*remoteclient.Client, error) {
	return remoteclient.New(remoteclient.Config{BaseURL: s.url, Token: s.token})
}

func mcpTarget(arguments map[string]interface{}, fallback string) string {
	target, ok := arguments["target"].(string)
	if !ok || strings.TrimSpace(target) == "" {
		return fallback
	}
	return target
}

func mcpToolInput(name string, arguments map[string]interface{}) (map[string]interface{}, error) {
	input := make(map[string]interface{}, len(arguments))
	for key, value := range arguments {
		if key != "target" {
			input[key] = value
		}
	}
	if name == "bash" {
		delete(input, "tty")
	}
	if err := validateMCPToolInput(name, input); err != nil {
		return nil, err
	}
	return input, nil
}

func validateMCPToolInput(name string, input map[string]interface{}) error {
	switch name {
	case "read", "write", "edit":
		return requireMCPString(input, "path")
	case "bash":
		return requireMCPString(input, "command")
	case "find":
		return nil
	default:
		return fmt.Errorf("unknown tool %q", name)
	}
}

func requireMCPString(input map[string]interface{}, field string) error {
	value, ok := input[field].(string)
	if !ok || strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s must be a non-empty string", field)
	}
	return nil
}

func mcpDecodeResult(toolName string, raw json.RawMessage) (interface{}, error) {
	var value interface{}
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, fmt.Errorf("decode remote result: %w", err)
	}
	if toolName == "bash" {
		if object, ok := value.(map[string]interface{}); ok {
			delete(object, "output_base64")
		}
	}
	return value, nil
}

func mcpSuccessResult(value interface{}) mcpToolResult {
	return mcpToolResult{
		Content:           []mcpTextContent{{Type: "text", Text: mcpResultText(value)}},
		StructuredContent: value,
	}
}

func mcpErrorResult(err error) mcpToolResult {
	return mcpToolResult{
		Content: []mcpTextContent{{Type: "text", Text: err.Error()}},
		IsError: true,
	}
}

func mcpResultText(value interface{}) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprintf("encode remote result: %v", err)
	}
	return string(encoded)
}

func mcpSuccess(id json.RawMessage, result interface{}) mcpResponse {
	return mcpResponse{JSONRPC: "2.0", ID: id, Result: result}
}

func mcpInvalidRequest(id json.RawMessage, message string) mcpResponse {
	return mcpFailure(id, -32600, message, nil)
}

func mcpNotInitialized(id json.RawMessage) mcpResponse {
	return mcpFailure(id, -32002, "server not initialized", nil)
}

func mcpFailure(id json.RawMessage, code int, message string, data interface{}) mcpResponse {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	return mcpResponse{JSONRPC: "2.0", ID: id, Error: &mcpError{Code: code, Message: message, Data: data}}
}

func mcpTools() []mcpTool {
	return []mcpTool{
		mcpToolDefinition("remote_targets", "List the remote Gateway and connected remote Workers. No arguments are required.", map[string]interface{}{"type": "object", "additionalProperties": false}),
		mcpToolDefinition("remote_read", "Read a text or binary file from the remote Gateway or Worker, never from the local machine.", objectSchema([]string{"path"}, map[string]interface{}{"path": stringSchema("Remote file path."), "target": stringSchema("Optional remote Worker ID; defaults to the configured target."), "offset": integerSchema("Optional first line, starting at 1."), "limit": integerSchema("Optional maximum number of lines.")})),
		mcpToolDefinition("remote_find", "Find files and directories beneath the remote workspace root, never on the local machine.", objectSchema(nil, map[string]interface{}{"query": stringSchema("Optional search text."), "max_results": integerSchema("Optional maximum number of matches."), "target": stringSchema("Optional remote Worker ID; defaults to the configured target.")})),
		mcpToolDefinition("remote_bash", "Execute a non-interactive shell command on the remote Gateway or Worker, never in the local workspace. Interactive terminals are available only through the CLI --tty mode.", objectSchema([]string{"command"}, map[string]interface{}{"command": stringSchema("Shell command to execute remotely."), "timeout": numberSchema("Optional remote timeout in seconds."), "encoding": stringSchema("Optional remote output encoding, for example gb18030."), "target": stringSchema("Optional remote Worker ID; defaults to the configured target.")})),
		mcpToolDefinition("remote_write", "Replace a file on the remote Gateway or Worker, never on the local machine.", objectSchema([]string{"path", "content"}, map[string]interface{}{"path": stringSchema("Remote file path."), "content": stringSchema("Complete UTF-8 file content."), "target": stringSchema("Optional remote Worker ID; defaults to the configured target.")})),
		mcpToolDefinition("remote_edit", "Apply exact text replacements to a file on the remote Gateway or Worker, never on the local machine.", objectSchema([]string{"path", "edits"}, map[string]interface{}{"path": stringSchema("Remote file path."), "edits": map[string]interface{}{"type": "array", "description": "Replacement objects with oldText and newText.", "items": objectSchema([]string{"oldText", "newText"}, map[string]interface{}{"oldText": stringSchema("Text to replace."), "newText": stringSchema("Replacement text.")})}, "target": stringSchema("Optional remote Worker ID; defaults to the configured target.")})),
	}
}

func mcpToolDefinition(name, description string, inputSchema interface{}) mcpTool {
	return mcpTool{Name: name, Description: description, InputSchema: inputSchema}
}

func objectSchema(required []string, properties map[string]interface{}) map[string]interface{} {
	schema := map[string]interface{}{"type": "object", "properties": properties, "additionalProperties": false}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func stringSchema(description string) map[string]string {
	return map[string]string{"type": "string", "description": description}
}

func integerSchema(description string) map[string]string {
	return map[string]string{"type": "integer", "description": description}
}

func numberSchema(description string) map[string]string {
	return map[string]string{"type": "number", "description": description}
}
