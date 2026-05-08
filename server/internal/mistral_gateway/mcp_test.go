/*
SPDX-FileCopyrightText: 2026 M5Stack Technology CO LTD
SPDX-License-Identifier: MIT
*/

package mistral_gateway

import (
	"encoding/json"
	"testing"
)

// TestParseToolsListResultPaginated verifies we can decode the device
// shape of a `tools/list` response with a nextCursor (multi-page).
func TestParseToolsListResultPaginated(t *testing.T) {
	raw := `{
		"tools": [
			{"name":"self.robot.set_head_angles","description":"Adjust head","inputSchema":{"type":"object"}}
		],
		"nextCursor": "self.robot.set_led_color"
	}`
	var r toolsListResult
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(r.Tools) != 1 {
		t.Fatalf("tools len = %d, want 1", len(r.Tools))
	}
	if r.Tools[0].Name != "self.robot.set_head_angles" {
		t.Errorf("name = %q, want self.robot.set_head_angles", r.Tools[0].Name)
	}
	if r.NextCursor != "self.robot.set_led_color" {
		t.Errorf("nextCursor = %q, want set_led_color", r.NextCursor)
	}
	// inputSchema must round-trip as raw JSON without re-marshaling.
	if string(r.Tools[0].InputSchema) != `{"type":"object"}` {
		t.Errorf("inputSchema = %q, want {\"type\":\"object\"}",
			string(r.Tools[0].InputSchema))
	}
}

// TestParseToolsListFinalPage covers the last page of pagination —
// no nextCursor means we stop iterating.
func TestParseToolsListFinalPage(t *testing.T) {
	raw := `{"tools":[{"name":"a"},{"name":"b"}]}`
	var r toolsListResult
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(r.Tools) != 2 {
		t.Fatalf("tools len = %d, want 2", len(r.Tools))
	}
	if r.NextCursor != "" {
		t.Errorf("nextCursor = %q, want empty (final page)", r.NextCursor)
	}
}

// TestParseJSONRPCError exercises the spec-compliant error envelope
// the device returns when a tool name is unknown or args are bad.
func TestParseJSONRPCError(t *testing.T) {
	raw := `{
		"jsonrpc": "2.0",
		"id": 7,
		"error": {"code": -32602, "message": "Unknown tool: foo.bar"}
	}`
	var r jsonrpcResponse
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if r.ID != 7 {
		t.Errorf("id = %d, want 7", r.ID)
	}
	if r.Error == nil {
		t.Fatal("error is nil")
	}
	if r.Error.Code != -32602 {
		t.Errorf("code = %d, want -32602", r.Error.Code)
	}
	if len(r.Result) != 0 {
		t.Errorf("result = %s, want empty (error response)", string(r.Result))
	}
}

// TestMCPClientRouteByID drives HandleResponse with two outstanding
// requests to confirm the dispatcher routes to the right channel.
// We synthesize JSON-RPC payloads directly — no WS needed.
func TestMCPClientRouteByID(t *testing.T) {
	c := &MCPClient{
		nextID:  1,
		pending: make(map[int]chan *jsonrpcResponse),
	}

	ch1 := make(chan *jsonrpcResponse, 1)
	ch7 := make(chan *jsonrpcResponse, 1)
	c.pending[1] = ch1
	c.pending[7] = ch7

	// Deliver response for ID 7 first; ID 1 must remain pending.
	if !c.HandleResponse([]byte(`{"jsonrpc":"2.0","id":7,"result":{"ok":true}}`)) {
		t.Error("expected matched=true for id=7")
	}
	select {
	case r := <-ch7:
		if r.ID != 7 {
			t.Errorf("ch7 got id=%d, want 7", r.ID)
		}
	default:
		t.Error("ch7 not delivered")
	}
	select {
	case <-ch1:
		t.Error("ch1 should not have received anything yet")
	default:
	}

	// Now deliver ID 1.
	if !c.HandleResponse([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`)) {
		t.Error("expected matched=true for id=1")
	}
	select {
	case r := <-ch1:
		if r.ID != 1 {
			t.Errorf("ch1 got id=%d, want 1", r.ID)
		}
	default:
		t.Error("ch1 not delivered")
	}
}

// TestMCPClientUnmatchedID confirms unknown IDs are dropped (not
// crashing the gateway when the device sends a stray response).
func TestMCPClientUnmatchedID(t *testing.T) {
	c := &MCPClient{
		nextID:  1,
		pending: make(map[int]chan *jsonrpcResponse),
	}
	if c.HandleResponse([]byte(`{"jsonrpc":"2.0","id":999,"result":{}}`)) {
		t.Error("expected matched=false for unknown id")
	}
}
