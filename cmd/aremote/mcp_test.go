package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestMCPServesInitializeAndToolsList(t *testing.T) {
	input := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}
{"jsonrpc":"2.0","method":"notifications/initialized"}
{"jsonrpc":"2.0","id":2,"method":"tools/list"}
`)
	var output bytes.Buffer
	server := mcpServer{}
	if err := server.serve(context.Background(), input, &output); err != nil {
		t.Fatal(err)
	}
	responses := decodeMCPResponses(t, output.String())
	if len(responses) != 2 {
		t.Fatalf("response count = %d, output = %s", len(responses), output.String())
	}
	if responses[0]["error"] != nil {
		t.Fatalf("initialize error = %#v", responses[0]["error"])
	}
	result, ok := responses[1]["result"].(map[string]interface{})
	if !ok {
		t.Fatalf("tools/list result = %#v", responses[1]["result"])
	}
	tools, ok := result["tools"].([]interface{})
	if !ok || len(tools) != 7 {
		t.Fatalf("tools = %#v", result["tools"])
	}
	if !containsMCPTool(tools, "remote_targets") || !containsMCPTool(tools, "remote_workers") || !containsMCPTool(tools, "remote_bash") || containsMCPTool(tools, "bash") {
		t.Fatalf("unexpected tool names: %#v", tools)
	}
	remoteBash := mcpToolByName(t, tools, "remote_bash")
	schema, ok := remoteBash["inputSchema"].(map[string]interface{})
	if !ok {
		t.Fatalf("remote_bash schema = %#v", remoteBash["inputSchema"])
	}
	properties, ok := schema["properties"].(map[string]interface{})
	if !ok || properties["target"] == nil || properties["worker"] == nil || properties["connection"] != nil {
		t.Fatalf("remote_bash properties = %#v", properties)
	}
}

func TestMCPRejectsCallsBeforeInitialize(t *testing.T) {
	server := mcpServer{}
	response, ok := server.handleRequest(context.Background(), json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	if !ok || response.Error == nil || response.Error.Code != -32002 {
		t.Fatalf("response = %#v, ok = %v", response, ok)
	}
}

func TestMCPStartsWithoutRemoteConnection(t *testing.T) {
	server := mcpServer{}
	response := server.initialize(mcpRequest{ID: json.RawMessage("1"), Params: json.RawMessage(`{"protocolVersion":"2025-11-25"}`)})
	if response.Error != nil {
		t.Fatalf("initialize error = %#v", response.Error)
	}
	result, err := server.executeTool(context.Background(), "remote_workers", nil)
	if err == nil || result != nil || !strings.Contains(err.Error(), "remote URL is required") {
		t.Fatalf("result = %#v, err = %v", result, err)
	}
}

func TestMCPToolInputRemovesTargetRouting(t *testing.T) {
	input, err := mcpToolInput("bash", map[string]interface{}{"command": "id", "target": "target-1", "worker": "worker-1"})
	if err != nil {
		t.Fatal(err)
	}
	if input["command"] != "id" || input["target"] != nil || input["worker"] != nil {
		t.Fatalf("input = %#v", input)
	}
}

func TestMCPDecodeBashResultRemovesOutputBase64(t *testing.T) {
	value, err := mcpDecodeResult("bash", json.RawMessage(`{"output":"ok\n","output_base64":"b2sK","exit_code":0}`))
	if err != nil {
		t.Fatal(err)
	}
	result, ok := value.(map[string]interface{})
	if !ok {
		t.Fatalf("decoded result type = %T", value)
	}
	if _, exists := result["output_base64"]; exists {
		t.Fatal("output_base64 must not be returned by MCP")
	}
	if result["output"] != "ok\n" {
		t.Fatalf("output = %#v", result["output"])
	}
}

func TestMCPWritesBatchResponsesAsOneJSONMessage(t *testing.T) {
	input := strings.NewReader(`[{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25"}},{"jsonrpc":"2.0","id":2,"method":"tools/list"}]
`)
	var output bytes.Buffer
	server := mcpServer{}
	if err := server.serve(context.Background(), input, &output); err != nil {
		t.Fatal(err)
	}
	var responses []map[string]interface{}
	if err := json.Unmarshal([]byte(strings.TrimSpace(output.String())), &responses); err != nil {
		t.Fatalf("decode batch response: %v; output = %s", err, output.String())
	}
	if len(responses) != 2 {
		t.Fatalf("response count = %d", len(responses))
	}
}

func decodeMCPResponses(t *testing.T, output string) []map[string]interface{} {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(output), "\n")
	responses := make([]map[string]interface{}, 0, len(lines))
	for _, line := range lines {
		var response map[string]interface{}
		if err := json.Unmarshal([]byte(line), &response); err != nil {
			t.Fatalf("decode response %q: %v", line, err)
		}
		responses = append(responses, response)
	}
	return responses
}

func containsMCPTool(tools []interface{}, name string) bool {
	for _, tool := range tools {
		value, ok := tool.(map[string]interface{})
		if ok && value["name"] == name {
			return true
		}
	}
	return false
}

func mcpToolByName(t *testing.T, tools []interface{}, name string) map[string]interface{} {
	t.Helper()
	for _, tool := range tools {
		value, ok := tool.(map[string]interface{})
		if ok && value["name"] == name {
			return value
		}
	}
	t.Fatalf("tool %q not found", name)
	return nil
}
