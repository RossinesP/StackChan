/*
SPDX-FileCopyrightText: 2026 M5Stack Technology CO LTD
SPDX-License-Identifier: MIT
*/

package mistral_gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// MCP wire protocol on this transport: messages are wrapped in the
// xiaozhi WebSocket envelope ({"type":"mcp", "payload": <JSON-RPC>})
// and the inner payload is plain JSON-RPC 2.0.
//
// The device implements MCP server-side: it exposes tools (head
// angles, LED, reminders, etc.) that we can list and call. We are
// the MCP client. The protocol is symmetric — either side could
// initiate, but for our purposes the gateway always initiates and
// the device responds.
//
// Reference:
//   docs/architecture/07-path-a-implementation.md (MCP message envelope)
//   firmware/xiaozhi-esp32/main/mcp_server.cc (device-side server)
//   firmware/main/hal/hal_mcp.cpp (StackChan tool registrations)

// jsonrpcRequest is the outbound MCP envelope's `payload`. We always
// set the JSON-RPC version to "2.0" and an integer ID; the device
// echoes the ID in its response so we can route it back to the
// waiting caller.
type jsonrpcRequest struct {
	JSONRPC string         `json:"jsonrpc"`
	ID      int            `json:"id"`
	Method  string         `json:"method"`
	Params  map[string]any `json:"params,omitempty"`
}

// jsonrpcResponse is the inbound MCP envelope's `payload`. Either
// `result` (success) or `error` (failure) is populated. The device's
// MCP server uses the spec-compliant error shape: code/message[/data].
type jsonrpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *jsonrpcError   `json:"error,omitempty"`
}

type jsonrpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Tool is a single discovered tool from `tools/list`. We keep the
// inputSchema as raw JSON because we'll forward it verbatim to
// Mistral (M8b) — re-marshaling would risk losing field order and
// schema-internal hints that the upstream tool author may have set.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`
}

// toolsListResult is the JSON shape inside a successful `tools/list`
// response. `nextCursor` paginates: the device returns it when it had
// more tools but ran out of payload budget (max 8 KiB per page —
// see GetToolsList in mcp_server.cc).
type toolsListResult struct {
	Tools      []Tool `json:"tools"`
	NextCursor string `json:"nextCursor,omitempty"`
}

// mcpEnvelope is the WebSocket-level wrapper. Outbound and inbound
// MCP traffic uses the same shape; `payload` is the JSON-RPC body.
type mcpEnvelope struct {
	Type      string          `json:"type"`
	SessionID string          `json:"session_id,omitempty"`
	Payload   json.RawMessage `json:"payload"`
}

// MCPClient is the per-session MCP coordinator. It owns:
//   - a monotonic request ID counter (so each call is uniquely tagged)
//   - a map of pending response channels keyed by request ID
//   - the WS connection to write requests on
//
// The WS read loop in ws.go calls HandleResponse() when it sees an
// inbound `type:"mcp"` frame; that delivers the response to whichever
// goroutine called Request() (and is currently blocked on its channel).
//
// We don't manage tools/list pagination here at the protocol level —
// ListTools handles that by issuing repeated requests with the
// device's nextCursor.
type MCPClient struct {
	conn      *websocket.Conn
	sessionID string

	mu      sync.Mutex
	nextID  int
	pending map[int]chan *jsonrpcResponse

	// writeMu serializes WriteMessage calls. The WS read loop runs
	// concurrently with our Request goroutines; gorilla/websocket
	// permits one concurrent reader and one concurrent writer, so
	// any goroutine that writes must hold this lock.
	writeMu *sync.Mutex
}

// NewMCPClient builds a client bound to a WebSocket connection and
// session ID. The writeMu must be the same mutex shared with all
// other code paths that write to the same conn (TTS playback, etc.)
// — see Session.WriteMu in session.go.
func NewMCPClient(conn *websocket.Conn, sessionID string, writeMu *sync.Mutex) *MCPClient {
	return &MCPClient{
		conn:      conn,
		sessionID: sessionID,
		nextID:    1,
		pending:   make(map[int]chan *jsonrpcResponse),
		writeMu:   writeMu,
	}
}

// Request sends a JSON-RPC request and waits for the matching
// response. The caller must NOT hold writeMu — Request acquires it
// internally for the write, then releases before blocking on the
// response channel.
//
// Times out via ctx; on timeout, the pending entry is cleaned up so
// a late response doesn't leak the slot.
func (c *MCPClient) Request(ctx context.Context, method string, params map[string]any) (*jsonrpcResponse, error) {
	c.mu.Lock()
	id := c.nextID
	c.nextID++
	respCh := make(chan *jsonrpcResponse, 1)
	c.pending[id] = respCh
	c.mu.Unlock()

	cleanup := func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}

	payload, err := json.Marshal(jsonrpcRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  params,
	})
	if err != nil {
		cleanup()
		return nil, err
	}
	envBytes, err := json.Marshal(mcpEnvelope{
		Type:      "mcp",
		SessionID: c.sessionID,
		Payload:   payload,
	})
	if err != nil {
		cleanup()
		return nil, err
	}

	c.writeMu.Lock()
	wErr := c.conn.WriteMessage(websocket.TextMessage, envBytes)
	c.writeMu.Unlock()
	if wErr != nil {
		cleanup()
		return nil, fmt.Errorf("mcp write: %w", wErr)
	}

	select {
	case resp := <-respCh:
		cleanup()
		if resp.Error != nil {
			return nil, fmt.Errorf("mcp rpc error %d: %s",
				resp.Error.Code, resp.Error.Message)
		}
		return resp, nil
	case <-ctx.Done():
		cleanup()
		return nil, fmt.Errorf("mcp %s: %w", method, ctx.Err())
	}
}

// HandleResponse is called by the WS read loop when a `type:"mcp"`
// frame arrives. Routes the response to the waiting Request caller
// based on the JSON-RPC ID. Unmatched IDs (no pending caller) are
// dropped with a debug-log entry — the device shouldn't send those
// but we don't want one rogue frame to crash the gateway.
//
// `payload` is the inner JSON-RPC body (the contents of the
// envelope's `payload` field).
func (c *MCPClient) HandleResponse(payload []byte) (matched bool) {
	var resp jsonrpcResponse
	if err := json.Unmarshal(payload, &resp); err != nil {
		return false
	}
	c.mu.Lock()
	ch, ok := c.pending[resp.ID]
	c.mu.Unlock()
	if !ok {
		return false
	}
	// Non-blocking send: channel is buffered with capacity 1, but
	// double-cleanup races could in theory close it elsewhere. Be
	// defensive.
	select {
	case ch <- &resp:
	default:
	}
	return true
}

// toolCallContent is one element of the MCP tools/call result's
// content[] array. We only consume text blocks — the spec also
// supports image/blob, but the StackChan tools don't return those.
type toolCallContent struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// toolCallResult is the unwrapped `result` payload of a successful
// tools/call response. `isError` distinguishes a legitimate "the tool
// ran and rejected this input" from "the tool succeeded but the value
// happens to look like an error" — important to feed back to the LLM
// so it can recover.
type toolCallResult struct {
	Content []toolCallContent `json:"content"`
	IsError bool              `json:"isError"`
}

// CallTool invokes one device-side MCP tool by name with the given
// JSON arguments object, and returns the concatenated text content
// the device produced. `isError` is true when the device set the
// MCP result's isError flag — the caller (chat loop) feeds this
// back to the LLM as part of the tool result so the model can
// react accordingly.
//
// Per-call timeout defaults to 10 s (head-angle moves can take
// 1-2 s; reminder creation is fast). Caller can shorten via ctx.
func (c *MCPClient) CallTool(ctx context.Context, name string, args map[string]any) (text string, isError bool, err error) {
	callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	if args == nil {
		args = map[string]any{}
	}
	resp, err := c.Request(callCtx, "tools/call", map[string]any{
		"name":      name,
		"arguments": args,
	})
	if err != nil {
		return "", false, err
	}

	var r toolCallResult
	if err := json.Unmarshal(resp.Result, &r); err != nil {
		return "", false, fmt.Errorf("tools/call result: %w", err)
	}
	// Concatenate every text block; non-text blocks become a
	// human-readable placeholder so the model still sees them in
	// the conversation log.
	var sb []byte
	for _, c := range r.Content {
		if c.Type == "text" {
			sb = append(sb, c.Text...)
		} else {
			sb = append(sb, "["+c.Type+"]"...)
		}
	}
	return string(sb), r.IsError, nil
}

// Initialize sends the MCP `initialize` request, which the device
// uses to receive the gateway's capabilities — most importantly the
// vision URL + token, which configures the camera's explain
// endpoint (see firmware mcp_server.cc::ParseCapabilities).
//
// Capabilities is passed verbatim as `params.capabilities` in the
// JSON-RPC request. We don't introspect the device's response
// (`protocolVersion`, `serverInfo`) — it's informational only and
// the device works with or without us reading it.
//
// Must be called BEFORE ListTools — the device may register
// camera-related tools only when it sees a vision URL configured.
// (StackChan doesn't gate today, but other boards might.)
func (c *MCPClient) Initialize(ctx context.Context, capabilities map[string]any) error {
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	_, err := c.Request(callCtx, "initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    capabilities,
		"clientInfo": map[string]any{
			"name":    "stackchan-mistral-gateway",
			"version": "0.9.0", // bump on protocol-affecting changes
		},
	})
	return err
}

// ListTools issues `tools/list` requests until the device stops
// returning a `nextCursor`, concatenating the pages. Pagination is
// driven by the device's max-payload budget (8 KiB) — StackChan
// today has ~6 tools that comfortably fit one page, but we honor
// the protocol so it stays correct as more tools are added.
//
// The default per-call timeout is 5 s; ListTools as a whole has the
// caller's ctx as its only bound, so a misbehaving device could
// stall it. Caller should ctx.WithTimeout for the entire discovery.
func (c *MCPClient) ListTools(ctx context.Context) ([]Tool, error) {
	var (
		all    []Tool
		cursor string
	)
	for {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		resp, err := c.Request(callCtx, "tools/list", params)
		cancel()
		if err != nil {
			return all, fmt.Errorf("tools/list: %w", err)
		}
		var page toolsListResult
		if err := json.Unmarshal(resp.Result, &page); err != nil {
			return all, fmt.Errorf("tools/list result: %w", err)
		}
		all = append(all, page.Tools...)
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	return all, nil
}
