package main

import (
	"encoding/json"
	"testing"
)

func TestInitializeResponseAdvertisesToolCapability(t *testing.T) {
	resp := map[string]any{
		"jsonrpc": "2.0",
		"id":      json.RawMessage("1"),
		"result": map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities": map[string]any{
				"tools": map[string]any{
					"listChanged": false,
				},
			},
			"serverInfo": map[string]any{
				"name":    "team-mcp",
				"version": "0.2.0",
			},
		},
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}

	var decoded struct {
		Result struct {
			Capabilities struct {
				Tools map[string]any `json:"tools"`
			} `json:"capabilities"`
		} `json:"result"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Result.Capabilities.Tools == nil {
		t.Fatal("tools capability missing")
	}
	v, ok := decoded.Result.Capabilities.Tools["listChanged"]
	if !ok {
		t.Fatal("tools.listChanged missing")
	}
	if got, ok := v.(bool); !ok || got {
		t.Fatalf("tools.listChanged = %#v, want false", v)
	}
}
