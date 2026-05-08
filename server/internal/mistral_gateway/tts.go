/*
SPDX-FileCopyrightText: 2026 M5Stack Technology CO LTD
SPDX-License-Identifier: MIT
*/

package mistral_gateway

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// mistralTTSEndpoint is the canonical Voxtral TTS REST endpoint.
// See docs/architecture/06-mistral-migration.md and
// https://docs.mistral.ai/api/endpoint/audio/speech.
const mistralTTSEndpoint = "https://api.mistral.ai/v1/audio/speech"

// mistralVoicesEndpoint lists/manages voice profiles.
const mistralVoicesEndpoint = "https://api.mistral.ai/v1/audio/voices"

// mistralTTSDefaultModel — Voxtral Mini TTS as of 2026.
const mistralTTSDefaultModel = "voxtral-mini-tts-2603"

// Note on the request shape: the docs list `voice_id` but the
// non-streaming endpoint historically required `voice`; the streaming
// endpoint (added later) appears to honor `voice_id`. We send both —
// it's the only combination that's worked across endpoint variants
// without 400s or 500s.
type ttsRequest struct {
	Model          string `json:"model"`
	Input          string `json:"input"`
	Voice          string `json:"voice,omitempty"`
	VoiceID        string `json:"voice_id,omitempty"`
	RefAudio       string `json:"ref_audio,omitempty"`
	ResponseFormat string `json:"response_format"`
	Stream         bool   `json:"stream"`
}

type ttsResponse struct {
	AudioData string `json:"audio_data"` // base64-encoded
}

type voicesListResponse struct {
	Items []voiceItem `json:"items"`
}

type voiceItem struct {
	ID     string  `json:"id"`
	Name   string  `json:"name"`
	Slug   string  `json:"slug,omitempty"`
	UserID *string `json:"user_id"` // null for preset voices, set for user-cloned
}

var (
	autoVoiceOnce sync.Once
	autoVoiceID   string
	autoVoiceErr  error
)

// resolveVoice picks the voice to send. Order:
//  1. MISTRAL_TTS_VOICE env var (explicit)
//  2. The first voice returned by GET /v1/audio/voices (auto-discovery,
//     cached for the process lifetime)
//
// We don't pass `ref_audio` here — that's for one-off zero-shot cloning
// which would force the user to supply a sample file. M10 will surface
// per-agent voice config from the DB.
func resolveVoice(ctx context.Context, c Config) (string, error) {
	if c.TTSVoice != "" {
		return c.TTSVoice, nil
	}
	autoVoiceOnce.Do(func() {
		autoVoiceID, autoVoiceErr = discoverFirstVoice(ctx, c.MistralAPIKey)
	})
	return autoVoiceID, autoVoiceErr
}

func discoverFirstVoice(ctx context.Context, apiKey string) (string, error) {
	// limit=100 covers the typical voice catalog in one page (presets +
	// user clones). If the catalog ever grows past that, fall back to
	// the first page.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		mistralVoicesEndpoint+"?limit=100", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return "", fmt.Errorf("voices list http: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("voices list status=%d body=%s", resp.StatusCode,
			string(body[:min(400, len(body))]))
	}

	var list voicesListResponse
	if err := json.Unmarshal(body, &list); err != nil {
		return "", fmt.Errorf("voices list json: %w", err)
	}
	if len(list.Items) == 0 {
		return "", fmt.Errorf("no voices available — create one with " +
			"POST /v1/audio/voices, or set MISTRAL_TTS_VOICE explicitly")
	}

	// Prefer presets (user_id == null) over user-cloned voices, so the
	// gateway doesn't silently pick up a personal clone as the default.
	// Inside presets, prefer something matching "neutral" if present so
	// the demo voice isn't a strong emotion variant.
	var preset, neutral *voiceItem
	for i := range list.Items {
		v := &list.Items[i]
		if v.UserID != nil {
			continue
		}
		if preset == nil {
			preset = v
		}
		if hasSubstr(v.Slug+" "+v.Name, "neutral") {
			neutral = v
			break
		}
	}
	pick := neutral
	if pick == nil {
		pick = preset
	}
	if pick == nil {
		pick = &list.Items[0] // no presets at all — fall back to whatever
	}
	return pick.ID, nil
}

func hasSubstr(s, sub string) bool {
	return len(sub) > 0 && len(s) >= len(sub) && bytes.Contains(
		bytes.ToLower([]byte(s)), bytes.ToLower([]byte(sub)))
}

// SynthesizedAudio is the decoded result of a TTS call: 16-bit signed
// little-endian PCM samples plus the source sample rate. Channels are
// always normalized to mono (averaging if the source was stereo).
type SynthesizedAudio struct {
	PCM        []int16
	SampleRate int
}

// SynthesizeSpeech calls Voxtral TTS and returns mono PCM int16.
//
// We request WAV because the header carries the sample rate explicitly
// — the docs don't document Voxtral's native rate, and using `pcm`
// (float32 LE without a header) would force us to guess.
func SynthesizeSpeech(ctx context.Context, text string) (*SynthesizedAudio, error) {
	c := Get()
	if c.MistralAPIKey == "" {
		return nil, fmt.Errorf("MISTRAL_API_KEY not set")
	}

	voice, err := resolveVoice(ctx, c)
	if err != nil {
		return nil, fmt.Errorf("resolve voice: %w", err)
	}

	body, err := json.Marshal(ttsRequest{
		Model:          firstNonEmpty(c.TTSModel, mistralTTSDefaultModel),
		Input:          text,
		Voice:          voice,
		VoiceID:        voice,
		ResponseFormat: "wav",
		Stream:         false,
	})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, mistralTTSEndpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.MistralAPIKey)
	req.Header.Set("Content-Type", "application/json")

	httpClient := &http.Client{Timeout: 30 * time.Second}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("tts http: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		// Truncate noisy bodies for the log line.
		if len(respBody) > 400 {
			respBody = respBody[:400]
		}
		return nil, fmt.Errorf("tts status=%d body=%s", resp.StatusCode, string(respBody))
	}

	var parsed ttsResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("tts json: %w (first 200B: %s)", err, string(respBody[:min(200, len(respBody))]))
	}

	wav, err := base64.StdEncoding.DecodeString(parsed.AudioData)
	if err != nil {
		return nil, fmt.Errorf("tts base64: %w", err)
	}

	pcm, rate, err := decodeWAV(wav)
	if err != nil {
		return nil, fmt.Errorf("tts wav: %w", err)
	}
	return &SynthesizedAudio{PCM: pcm, SampleRate: rate}, nil
}

// decodeWAV parses a minimal RIFF/WAVE container and returns mono
// 16-bit PCM samples + the source sample rate.
//
// Spec: http://soundfile.sapp.org/doc/WaveFormat/
//   Bytes 0-3:   "RIFF"
//   Bytes 8-11:  "WAVE"
//   Then a series of chunks; we look for "fmt " and "data".
//
// The fmt chunk gives us audio_format, num_channels, sample_rate,
// bits_per_sample. The data chunk holds the PCM. We accept format=1
// (PCM int) and bits=16, downmixing stereo to mono if needed.
func decodeWAV(buf []byte) ([]int16, int, error) {
	if len(buf) < 44 || string(buf[0:4]) != "RIFF" || string(buf[8:12]) != "WAVE" {
		return nil, 0, fmt.Errorf("not a WAV file (len=%d)", len(buf))
	}

	var (
		audioFormat   uint16
		numChannels   uint16
		sampleRate    uint32
		bitsPerSample uint16
		dataStart     int
		dataLen       int
	)

	pos := 12
	for pos+8 <= len(buf) {
		id := string(buf[pos : pos+4])
		size := int(binary.LittleEndian.Uint32(buf[pos+4 : pos+8]))
		body := pos + 8
		switch id {
		case "fmt ":
			if size < 16 || body+16 > len(buf) {
				return nil, 0, fmt.Errorf("fmt chunk too small")
			}
			audioFormat = binary.LittleEndian.Uint16(buf[body : body+2])
			numChannels = binary.LittleEndian.Uint16(buf[body+2 : body+4])
			sampleRate = binary.LittleEndian.Uint32(buf[body+4 : body+8])
			bitsPerSample = binary.LittleEndian.Uint16(buf[body+14 : body+16])
		case "data":
			dataStart = body
			dataLen = size
		}
		// Chunks are padded to even byte counts.
		pos = body + size
		if size%2 == 1 {
			pos++
		}
		if dataStart > 0 && audioFormat != 0 {
			break
		}
	}

	if audioFormat != 1 {
		return nil, 0, fmt.Errorf("unsupported WAV audio_format=%d (want 1=PCM)", audioFormat)
	}
	if bitsPerSample != 16 {
		return nil, 0, fmt.Errorf("unsupported bits_per_sample=%d (want 16)", bitsPerSample)
	}
	if dataStart == 0 || dataLen == 0 {
		return nil, 0, fmt.Errorf("WAV has no data chunk")
	}
	if dataStart+dataLen > len(buf) {
		dataLen = len(buf) - dataStart
	}

	totalSamples := dataLen / 2 // int16 = 2 bytes
	raw := make([]int16, totalSamples)
	if err := binary.Read(bytes.NewReader(buf[dataStart:dataStart+dataLen]), binary.LittleEndian, raw); err != nil {
		return nil, 0, err
	}

	// Downmix to mono if stereo: average L+R per frame.
	if numChannels == 2 {
		mono := make([]int16, totalSamples/2)
		for i := range mono {
			l := int32(raw[i*2])
			r := int32(raw[i*2+1])
			mono[i] = int16((l + r) / 2)
		}
		raw = mono
	} else if numChannels != 1 {
		return nil, 0, fmt.Errorf("unsupported channels=%d", numChannels)
	}

	return raw, int(sampleRate), nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// SSE event payloads emitted by Voxtral when stream=true. We only
// care about audio.delta (per-chunk PCM) and audio.done (terminator).
// Other events (errors, partial transcripts on round-trip models)
// would need their own handling; we log+ignore unknown types.
type sseAudioDelta struct {
	Type      string `json:"type"`
	AudioData string `json:"audio_data"` // base64-encoded raw PCM (float32 LE)
}

// SynthesizeSpeechStream calls Voxtral TTS in streaming mode and
// invokes onChunk with each decoded PCM segment as it arrives.
//
// Streaming cuts perceived latency: the first onChunk fires after
// ~500-800 ms (vs ~3 s for the buffered SynthesizeSpeech). The trade-off
// is no peak normalization (we don't see the full waveform up front)
// and a fixed sample rate assumption — see Config.MistralPCMSampleRate.
//
// Sample rate emitted by Voxtral when response_format="pcm" is not
// documented; we observed 24 kHz when the same prompt is requested as
// WAV. The response itself is float32 LE in [-1, +1], converted to
// int16 mono by Float32LEToInt16.
//
// onChunk is called from this goroutine; it MUST NOT block on the
// network read indefinitely or the API stream will stall (TCP backpressure).
func SynthesizeSpeechStream(
	ctx context.Context,
	text string,
	onChunk func(samples []int16, sampleRate int) error,
) error {
	c := Get()
	if c.MistralAPIKey == "" {
		return fmt.Errorf("MISTRAL_API_KEY not set")
	}

	voice, err := resolveVoice(ctx, c)
	if err != nil {
		return fmt.Errorf("resolve voice: %w", err)
	}

	body, err := json.Marshal(ttsRequest{
		Model:          firstNonEmpty(c.TTSModel, mistralTTSDefaultModel),
		Input:          text,
		Voice:          voice,
		VoiceID:        voice,
		ResponseFormat: "pcm",
		Stream:         true,
	})
	if err != nil {
		return err
	}

	// No client-wide timeout: the stream may legitimately last several
	// seconds. Rely on ctx for cancellation.
	httpClient := &http.Client{}

	// Single retry on transient 5xx. Voxtral occasionally returns
	// `unreachable_backend` (status 500, code 1100) on the streaming
	// endpoint; the same prompt typically succeeds on retry. We don't
	// retry 4xx (validation / auth) since those won't change.
	var resp *http.Response
	for attempt := 0; attempt < 2; attempt++ {
		req, reqErr := http.NewRequestWithContext(ctx, http.MethodPost,
			mistralTTSEndpoint, bytes.NewReader(body))
		if reqErr != nil {
			return reqErr
		}
		req.Header.Set("Authorization", "Bearer "+c.MistralAPIKey)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/event-stream")

		resp, err = httpClient.Do(req)
		if err != nil {
			return fmt.Errorf("tts stream http: %w", err)
		}
		if resp.StatusCode == http.StatusOK {
			break
		}
		// Read+log the body, then decide.
		errBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if len(errBody) > 400 {
			errBody = errBody[:400]
		}
		if attempt == 0 && resp.StatusCode >= 500 && resp.StatusCode < 600 {
			// Transient; back off briefly and retry.
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(400 * time.Millisecond):
			}
			continue
		}
		return fmt.Errorf("tts stream status=%d body=%s",
			resp.StatusCode, string(errBody))
	}
	defer resp.Body.Close()

	rate := c.MistralPCMSampleRate
	if rate <= 0 {
		rate = 24000
	}

	// SSE parser: read line-by-line. An event is delimited by a blank
	// line, with "event: <type>" and "data: <json>" lines. Voxtral
	// includes the type *inside* the data JSON too, so we don't strictly
	// need the event line, but we honor it for correctness.
	scanner := bufio.NewScanner(resp.Body)
	// Default scanner buffer is 64 KiB; base64-encoded PCM chunks can
	// exceed this for longer phrases. Bump to 1 MiB.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	// Carry leftover bytes (incomplete float32) across chunks. In
	// practice Voxtral aligns chunks on float32 boundaries, but the
	// docs don't guarantee it — the carry buffer is cheap insurance.
	var carry []byte

	var (
		eventType string
		dataBuf   strings.Builder
	)
	flush := func() error {
		if dataBuf.Len() == 0 {
			eventType = ""
			return nil
		}
		raw := dataBuf.String()
		dataBuf.Reset()
		defer func() { eventType = "" }()

		// Determine type from event header OR from data envelope.
		typ := eventType
		if typ == "" {
			var probe struct {
				Type string `json:"type"`
			}
			_ = json.Unmarshal([]byte(raw), &probe)
			typ = probe.Type
		}

		switch typ {
		case "speech.audio.delta":
			var delta sseAudioDelta
			if err := json.Unmarshal([]byte(raw), &delta); err != nil {
				return fmt.Errorf("sse delta json: %w", err)
			}
			pcmBytes, err := base64.StdEncoding.DecodeString(delta.AudioData)
			if err != nil {
				return fmt.Errorf("sse delta base64: %w", err)
			}
			if len(carry) > 0 {
				pcmBytes = append(carry, pcmBytes...)
				carry = nil
			}
			samples, consumed := Float32LEToInt16(pcmBytes)
			if consumed < len(pcmBytes) {
				carry = append(carry, pcmBytes[consumed:]...)
			}
			if len(samples) == 0 {
				return nil
			}
			return onChunk(samples, rate)
		case "speech.audio.done", "":
			// Terminator (or empty data) — caller's outer loop will
			// detect end via scanner.Scan returning false.
			return nil
		default:
			// Ignore unknown event types; log at debug level if needed.
			return nil
		}
	}

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := flush(); err != nil {
				return err
			}
			continue
		}
		switch {
		case strings.HasPrefix(line, "event: "):
			eventType = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			if dataBuf.Len() > 0 {
				dataBuf.WriteByte('\n')
			}
			dataBuf.WriteString(strings.TrimPrefix(line, "data: "))
		case strings.HasPrefix(line, ":"):
			// SSE comment / heartbeat; ignore.
		}
	}
	// Flush trailing event without final blank line (some servers
	// close the stream without it).
	if err := flush(); err != nil {
		return err
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("sse scan: %w", err)
	}
	return nil
}
