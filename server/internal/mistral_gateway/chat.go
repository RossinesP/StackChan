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
)

// ChatMessage is one turn in a conversation. We keep this minimal —
// no tool_calls, no name, no multi-modal content. M8 will extend it
// when MCP function-calling lands.
type ChatMessage struct {
	Role    ChatRole `json:"role"`
	Content string   `json:"content"`
}

type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []ChatMessage `json:"messages"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
	Temperature float32       `json:"temperature,omitempty"`
	Stream      bool          `json:"stream"`
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
// we then hand to the TTS streaming path. M9 could swap this for a
// streaming chat call to start TTS on the first sentence and shave
// another second off TTFA, at the cost of sentence-boundary detection.
func GenerateReply(ctx context.Context, userText string, history []ChatMessage) (string, error) {
	c := Get()
	if c.MistralAPIKey == "" {
		return "", fmt.Errorf("MISTRAL_API_KEY not set")
	}
	if userText == "" {
		return "", fmt.Errorf("empty user text")
	}

	messages := make([]ChatMessage, 0, len(history)+2)
	messages = append(messages, ChatMessage{
		Role:    RoleSystem,
		Content: c.ChatSystemPrompt,
	})
	messages = append(messages, history...)
	messages = append(messages, ChatMessage{
		Role:    RoleUser,
		Content: userText,
	})

	body, err := json.Marshal(chatRequest{
		Model:     c.MistralChatModel,
		Messages:  messages,
		MaxTokens: c.ChatMaxTokens,
		// Temperature 0.7 is Mistral's default-ish; gives some warmth
		// without being unpredictable. Could expose as env later.
		Temperature: 0.7,
		Stream:      false,
	})
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		mistralChatEndpoint, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.MistralAPIKey)
	req.Header.Set("Content-Type", "application/json")

	httpClient := &http.Client{Timeout: 30 * time.Second}
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("chat http: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		if len(respBody) > 400 {
			respBody = respBody[:400]
		}
		return "", fmt.Errorf("chat status=%d body=%s",
			resp.StatusCode, string(respBody))
	}

	var parsed chatResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", fmt.Errorf("chat json: %w (first 200B: %s)",
			err, string(respBody[:min(200, len(respBody))]))
	}
	if len(parsed.Choices) == 0 {
		return "", fmt.Errorf("chat returned no choices")
	}
	return parsed.Choices[0].Message.Content, nil
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
