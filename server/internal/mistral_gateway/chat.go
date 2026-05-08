/*
SPDX-FileCopyrightText: 2026 M5Stack Technology CO LTD
SPDX-License-Identifier: MIT
*/

package mistral_gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// mistralChatEndpoint is the standard chat completions endpoint.
// See https://docs.mistral.ai/api/endpoint/chat — the request shape
// is OpenAI-compatible enough for our basic conversational use.
const mistralChatEndpoint = "https://api.mistral.ai/v1/chat/completions"

// ChatRole is the OpenAI-compatible message role enum.
type ChatRole string

const (
	RoleSystem    ChatRole = "system"
	RoleUser      ChatRole = "user"
	RoleAssistant ChatRole = "assistant"
	RoleTool      ChatRole = "tool"
)

// ChatMessage is one turn in a conversation. M8b adds tool-related
// fields: ToolCalls (assistant decided to call N tools), ToolCallID
// (this is a 'tool' role result), Name (tool name on results).
//
// JSON omitempty rules ensure plain user/assistant turns marshal to
// the same compact shape they did pre-M8b.
type ChatMessage struct {
	Role       ChatRole         `json:"role"`
	Content    string           `json:"content"`
	ToolCalls  []ChatToolCall   `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
	Name       string           `json:"name,omitempty"`
}

// ChatToolCall is the assistant's request to invoke one tool. Mistral
// returns these in the response when the model decides a function
// call is appropriate. We loop on these in GenerateReplyWithTools.
type ChatToolCall struct {
	ID       string             `json:"id"`
	Type     string             `json:"type"` // always "function"
	Function ChatToolCallFunc   `json:"function"`
}

type ChatToolCallFunc struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // JSON-encoded string of args
}

type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []ChatMessage `json:"messages"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
	Temperature float32       `json:"temperature,omitempty"`
	Stream      bool          `json:"stream"`
	Tools       []MistralTool `json:"tools,omitempty"`
	ToolChoice  string        `json:"tool_choice,omitempty"` // "auto" | "none" | "any"
}

type chatResponse struct {
	Choices []struct {
		Message      ChatMessage `json:"message"`
		FinishReason string      `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

// GenerateReply calls Mistral chat completions with a system prompt,
// optional history, and the user's latest transcript. Returns the
// assistant's text reply.
//
// History is truncated to the last ChatHistoryLimit messages BEFORE
// appending the current user turn — we never drop the system prompt
// or the in-flight user message.
//
// Non-streaming: the assistant reply is the full text up front, which
// we then hand to the TTS streaming path.
//
// This is the no-tools convenience wrapper used by the M7 path. For
// MCP function-calling, see GenerateReplyWithTools.
func GenerateReply(ctx context.Context, userText string, history []ChatMessage) (string, error) {
	c := Get()
	messages := buildInitialMessages(c.ChatSystemPrompt, history, userText)
	resp, err := callChatCompletion(ctx, messages, nil)
	if err != nil {
		return "", err
	}
	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("chat returned no choices")
	}
	return resp.Choices[0].Message.Content, nil
}

// buildInitialMessages assembles [system, ...history, user] in the
// order the API expects. Used by both GenerateReply and the
// tool-calling loop.
func buildInitialMessages(systemPrompt string, history []ChatMessage, userText string) []ChatMessage {
	messages := make([]ChatMessage, 0, len(history)+2)
	messages = append(messages, ChatMessage{
		Role:    RoleSystem,
		Content: systemPrompt,
	})
	messages = append(messages, history...)
	messages = append(messages, ChatMessage{
		Role:    RoleUser,
		Content: userText,
	})
	return messages
}

// callChatCompletion is the single HTTP call to /v1/chat/completions.
// Used by both the no-tools path and each iteration of the tool loop.
// Returns the parsed response on HTTP 200; non-200 status is wrapped
// with body excerpt for diagnosis.
func callChatCompletion(ctx context.Context, messages []ChatMessage, tools []MistralTool) (*chatResponse, error) {
	c := Get()
	if c.MistralAPIKey == "" {
		return nil, fmt.Errorf("MISTRAL_API_KEY not set")
	}

	reqShape := chatRequest{
		Model:       c.MistralChatModel,
		Messages:    messages,
		MaxTokens:   c.ChatMaxTokens,
		Temperature: 0.7,
		Stream:      false,
	}
	if len(tools) > 0 {
		reqShape.Tools = tools
		// "auto" lets the model decide between tool call and direct
		// reply; that's what we want for natural conversation. "any"
		// would force a tool call on every turn.
		reqShape.ToolChoice = "auto"
	}

	body, err := json.Marshal(reqShape)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		mistralChatEndpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.MistralAPIKey)
	req.Header.Set("Content-Type", "application/json")

	httpClient := &http.Client{Timeout: 30 * time.Second}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("chat http: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		if len(respBody) > 400 {
			respBody = respBody[:400]
		}
		return nil, fmt.Errorf("chat status=%d body=%s",
			resp.StatusCode, string(respBody))
	}

	var parsed chatResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("chat json: %w (first 200B: %s)",
			err, string(respBody[:min(200, len(respBody))]))
	}
	return &parsed, nil
}

// ToolExecutor abstracts how a tool call is dispatched. The chat loop
// calls Execute(name, args) and feeds the returned text back as a
// 'tool' role message. In production this is sess.MCP.CallTool; in
// tests it can be a stub.
type ToolExecutor interface {
	Execute(ctx context.Context, name string, args map[string]any) (text string, isError bool, err error)
}

// mcpToolExecutor adapts MCPClient.CallTool to the ToolExecutor
// interface. Trivial wrapper, but lets the loop logic stay testable.
type mcpToolExecutor struct {
	client *MCPClient
}

func (e *mcpToolExecutor) Execute(ctx context.Context, name string, args map[string]any) (string, bool, error) {
	return e.client.CallTool(ctx, name, args)
}

// GenerateReplyWithTools runs the chat → tool_calls → execute → chat
// loop until the model produces a text-only response (no tool_calls)
// or we hit maxIterations.
//
// Loop body per iteration:
//  1. POST chat with current message log + tool defs
//  2. If choice has no tool_calls, return its content (done)
//  3. For each tool_call: execute via executor, append a 'tool' role
//     message with the result + tool_call_id
//  4. Append the assistant's tool-call message to the log so the next
//     iteration sees the full chain
//  5. Loop
//
// Returns the final text reply plus a summary of which tools fired
// (for logging — chat history records the assistant message but the
// caller wants a flat list for the operator log).
func GenerateReplyWithTools(
	ctx context.Context,
	userText string,
	history []ChatMessage,
	tools []MistralTool,
	executor ToolExecutor,
	maxIterations int,
) (reply string, calledTools []string, err error) {
	c := Get()
	if userText == "" {
		return "", nil, fmt.Errorf("empty user text")
	}
	if maxIterations <= 0 {
		maxIterations = 1
	}

	messages := buildInitialMessages(c.ChatSystemPrompt, history, userText)

	for iter := 0; iter < maxIterations; iter++ {
		resp, err := callChatCompletion(ctx, messages, tools)
		if err != nil {
			return "", calledTools, err
		}
		if len(resp.Choices) == 0 {
			return "", calledTools, fmt.Errorf("chat returned no choices")
		}
		choice := resp.Choices[0].Message

		// Terminal: no tool calls → assistant produced a final reply.
		if len(choice.ToolCalls) == 0 {
			return choice.Content, calledTools, nil
		}

		// Tool-call iteration: append the assistant's tool-call
		// message (Mistral requires it in subsequent calls so the
		// tool_call_ids resolve), then execute each call and append
		// a tool-role message per result.
		//
		// Defensive normalization: Mistral's response sometimes omits
		// or empties the `type` field on tool_calls; the API then
		// rejects the same payload on the next iteration with a
		// 422 ("Input should be 'function'"). Force-set the spec
		// value before echoing back.
		for i := range choice.ToolCalls {
			if choice.ToolCalls[i].Type == "" {
				choice.ToolCalls[i].Type = "function"
			}
		}
		messages = append(messages, choice)
		for _, tc := range choice.ToolCalls {
			calledTools = append(calledTools, tc.Function.Name)
			args := map[string]any{}
			if tc.Function.Arguments != "" {
				if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
					// Bad args from the model — feed the parse error
					// back so it can self-correct on the next iter.
					messages = append(messages, ChatMessage{
						Role:       RoleTool,
						ToolCallID: tc.ID,
						Name:       tc.Function.Name,
						Content:    fmt.Sprintf("error: invalid JSON arguments: %v", err),
					})
					continue
				}
			}
			text, isErr, execErr := executor.Execute(ctx, tc.Function.Name, args)
			content := text
			switch {
			case execErr != nil:
				content = fmt.Sprintf("error: %v", execErr)
			case isErr:
				content = "error: " + text
			case content == "":
				// Many tools return success with no text; don't send
				// an empty content (the model will treat it as failure).
				content = "ok"
			}
			messages = append(messages, ChatMessage{
				Role:       RoleTool,
				ToolCallID: tc.ID,
				Name:       tc.Function.Name,
				Content:    content,
			})
		}
	}

	// Hit the iteration cap with the model still wanting more tools.
	// Force one final chat call without tools so it has to write text.
	finalResp, err := callChatCompletion(ctx, messages, nil)
	if err != nil {
		return "", calledTools, err
	}
	if len(finalResp.Choices) == 0 {
		return "", calledTools, fmt.Errorf("chat returned no choices on forced finish")
	}
	return finalResp.Choices[0].Message.Content, calledTools, nil
}

// truncateHistory returns the most recent `limit` messages from `hist`,
// preserving order. Used to bound prompt size on each request.
//
// We trim BEFORE appending the new user turn (and BEFORE prepending
// the system prompt), so `limit` directly controls the number of
// past turns we replay. limit=0 disables history (each turn is
// stateless apart from the system prompt).
func truncateHistory(hist []ChatMessage, limit int) []ChatMessage {
	if limit <= 0 || len(hist) == 0 {
		return nil
	}
	if len(hist) <= limit {
		return hist
	}
	return hist[len(hist)-limit:]
}
