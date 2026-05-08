/*
SPDX-FileCopyrightText: 2026 M5Stack Technology CO LTD
SPDX-License-Identifier: MIT
*/

package mistral_gateway

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Mistral's chat completions endpoint accepts multimodal content
// (text + image_url) on vision-capable models. We embed the image
// inline as a base64 data URL — works for the small JPEGs the
// ESP32 camera produces (~30-80 KB at 320×240), no upload-then-
// reference dance needed.
//
// Reference:
//   https://docs.mistral.ai/capabilities/vision

// visionContentPart is one element of a multimodal chat message's
// `content` array. Only `text` and `image_url` types are used here.
type visionContentPart struct {
	Type     string             `json:"type"`
	Text     string             `json:"text,omitempty"`
	ImageURL *visionImageURLRef `json:"image_url,omitempty"`
}

type visionImageURLRef struct {
	URL string `json:"url"`
}

// visionMessage is a chat message with structured (multimodal)
// content. We can't reuse the M7 ChatMessage type because that one
// has Content as a string; mixing strings and arrays in the same
// JSON field is messy.
type visionMessage struct {
	Role    string              `json:"role"`
	Content []visionContentPart `json:"content"`
}

type visionRequest struct {
	Model       string          `json:"model"`
	Messages    []visionMessage `json:"messages"`
	MaxTokens   int             `json:"max_tokens,omitempty"`
	Temperature float32         `json:"temperature,omitempty"`
}

// visionResponse mirrors the standard chat completions response
// shape — vision models return plain text in choices[0].message.content
// just like text-only models.
type visionResponse struct {
	Choices []struct {
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

// ExplainImage sends one JPEG + question to Mistral's vision model
// and returns the model's text answer. The JPEG is embedded inline
// as a base64 data URL.
//
// The question text is wrapped via Config.VisionPromptWrapper so the
// model knows to keep its reply short and audio-appropriate.
//
// Cap: ~30 s (vision models are slower than text). Caller can
// shorten via ctx.
func ExplainImage(ctx context.Context, jpeg []byte, question string) (string, error) {
	c := Get()
	if c.MistralAPIKey == "" {
		return "", fmt.Errorf("MISTRAL_API_KEY not set")
	}
	if len(jpeg) == 0 {
		return "", fmt.Errorf("empty image")
	}

	prompt := question
	if c.VisionPromptWrapper != "" {
		prompt = fmt.Sprintf(c.VisionPromptWrapper, question)
	}

	dataURL := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(jpeg)

	body, err := json.Marshal(visionRequest{
		Model: c.MistralVisionModel,
		Messages: []visionMessage{
			{
				Role: "user",
				Content: []visionContentPart{
					{Type: "image_url", ImageURL: &visionImageURLRef{URL: dataURL}},
					{Type: "text", Text: prompt},
				},
			},
		},
		MaxTokens:   c.ChatMaxTokens, // reuse the chat cap; spoken-reply length
		Temperature: 0.5,             // slightly lower than chat default — descriptions should be factual
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
		return "", fmt.Errorf("vision http: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		if len(respBody) > 400 {
			respBody = respBody[:400]
		}
		return "", fmt.Errorf("vision status=%d body=%s",
			resp.StatusCode, string(respBody))
	}

	var parsed visionResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", fmt.Errorf("vision json: %w (first 200B: %s)",
			err, string(respBody[:min(200, len(respBody))]))
	}
	if len(parsed.Choices) == 0 {
		return "", fmt.Errorf("vision returned no choices")
	}
	return parsed.Choices[0].Message.Content, nil
}
