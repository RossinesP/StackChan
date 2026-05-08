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
	"mime/multipart"
	"net/http"
	"time"
)

// mistralSTTEndpoint is the canonical Voxtral transcription endpoint.
// See https://docs.mistral.ai/api/endpoint/audio/transcriptions
const mistralSTTEndpoint = "https://api.mistral.ai/v1/audio/transcriptions"

// mistralSTTDefaultModel — the latest Voxtral mini transcription model.
const mistralSTTDefaultModel = "voxtral-mini-latest"

// sttResponse mirrors the success body. We only care about `text` for
// M5; segments/usage are exposed for future consumers.
type sttResponse struct {
	Text     string         `json:"text"`
	Language string         `json:"language,omitempty"`
	Segments []any          `json:"segments,omitempty"`
	Usage    map[string]any `json:"usage,omitempty"`
}

// TranscribeAudio sends mono 16-bit PCM @ sampleRate Hz to Voxtral and
// returns the transcript.
//
// Wire format: multipart/form-data with a `file` field (WAV bytes), a
// `model` field, and optional `language` for accuracy boost.
//
// We build the WAV in memory rather than streaming because: (a) the
// captured audio is short (<1 minute, 100s of KB at most), (b)
// multipart upload doesn't support chunked encoding for the file part
// without `file_url`/`file_id` indirection.
func TranscribeAudio(ctx context.Context, pcm []int16, sampleRate int, language string) (string, error) {
	c := Get()
	if c.MistralAPIKey == "" {
		return "", fmt.Errorf("MISTRAL_API_KEY not set")
	}
	if len(pcm) == 0 {
		return "", fmt.Errorf("no audio samples to transcribe")
	}

	wav := EncodeWAVMono16(pcm, sampleRate)

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)

	if err := mw.WriteField("model", firstNonEmpty(c.STTModel, mistralSTTDefaultModel)); err != nil {
		return "", err
	}
	if language != "" {
		if err := mw.WriteField("language", language); err != nil {
			return "", err
		}
	}

	fw, err := mw.CreateFormFile("file", "input.wav")
	if err != nil {
		return "", err
	}
	if _, err := fw.Write(wav); err != nil {
		return "", err
	}
	if err := mw.Close(); err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, mistralSTTEndpoint, &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.MistralAPIKey)
	req.Header.Set("Content-Type", mw.FormDataContentType())

	httpClient := &http.Client{Timeout: 30 * time.Second}
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("stt http: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		if len(respBody) > 400 {
			respBody = respBody[:400]
		}
		return "", fmt.Errorf("stt status=%d body=%s", resp.StatusCode, string(respBody))
	}

	var parsed sttResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", fmt.Errorf("stt json: %w (first 200B: %s)", err, string(respBody[:min(200, len(respBody))]))
	}
	return parsed.Text, nil
}
