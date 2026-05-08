/*
SPDX-FileCopyrightText: 2026 M5Stack Technology CO LTD
SPDX-License-Identifier: MIT
*/

// Package mistral_gateway implements the xiaozhi-protocol-speaking
// gateway that fronts Mistral APIs (chat completions + Voxtral STT/TTS).
//
// See docs/architecture/06-mistral-migration.md (Path A) and
// docs/architecture/07-path-a-implementation.md for the wire protocol
// and milestones. This file holds runtime configuration loaded from env
// vars at process start.
package mistral_gateway

import (
	"os"
	"strconv"
	"sync"
)

// Config is the gateway-wide runtime configuration. All values are
// loaded once from environment variables; nothing here is per-device.
type Config struct {
	// WSURL is what the OTA endpoint returns to the device as the
	// WebSocket URL to dial. Must point at this gateway's /xiaozhi/v1/
	// route, reachable from the ESP32 (LAN IP, mDNS, or tunnel).
	WSURL string

	// OpusVersion is the BinaryProtocol version advertised in the OTA
	// response (1, 2, or 3). Default 2 — see protocol cheat sheet in
	// docs/architecture/07-path-a-implementation.md.
	OpusVersion int

	// MistralAPIKey authenticates against api.mistral.ai. Optional for
	// M1/M2/M3, required from M4 (TTS) onwards.
	MistralAPIKey string

	// TTSModel — Voxtral TTS model name. Empty defaults to
	// voxtral-mini-tts-2603. Set MISTRAL_TTS_MODEL to override.
	TTSModel string

	// TTSVoice — preset or custom voice ID for TTS. Empty triggers
	// auto-discovery (use the first voice returned by GET /v1/audio/voices).
	// Set MISTRAL_TTS_VOICE to pin a specific voice.
	TTSVoice string

	// TTSReplyText — what the gateway speaks back when a flush fires.
	// M4 is "static TTS", so the same string is spoken regardless of
	// what the user said. M6 will replace this with LLM output.
	TTSReplyText string

	// TTSPeakTarget — peak normalization target for TTS output, in
	// signed int16 magnitude (0–32767). Voxtral masters around -8 to
	// -12 dBFS which is too quiet for the StackChan speaker; 28000
	// (~-1.4 dBFS) gives a comfortable level with minimal distortion
	// risk. Set to 0 to disable normalization.
	TTSPeakTarget int
}

var (
	cfgOnce sync.Once
	cfg     Config
)

// Get returns the process-wide gateway config, loading from env on
// first call.
func Get() Config {
	cfgOnce.Do(func() {
		cfg = Config{
			WSURL:         envOr("GATEWAY_WS_URL", "ws://localhost:12800/xiaozhi/v1/"),
			OpusVersion:   envInt("GATEWAY_OPUS_VERSION", 2),
			MistralAPIKey: os.Getenv("MISTRAL_API_KEY"),
			TTSModel:      os.Getenv("MISTRAL_TTS_MODEL"),
			TTSVoice:      os.Getenv("MISTRAL_TTS_VOICE"),
			TTSReplyText: envOr("GATEWAY_TTS_REPLY",
				"Hello! This is your StackChan, replying through Mistral's Voxtral text to speech."),
			TTSPeakTarget: envInt("GATEWAY_TTS_PEAK", 28000),
		}
	})
	return cfg
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}
