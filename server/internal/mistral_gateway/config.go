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
	"strings"
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

	// STTModel — Voxtral transcription model. Empty defaults to
	// voxtral-mini-latest. Set MISTRAL_STT_MODEL to override.
	STTModel string

	// STTLanguage — ISO language hint for transcription (e.g. "en",
	// "fr"). Empty lets the model auto-detect; setting it boosts
	// accuracy for non-English speech.
	STTLanguage string

	// STTReplyTemplate — text spoken back after transcription, with
	// %s replaced by the transcript. M5 demo path. Set to empty to
	// just speak the transcript verbatim.
	STTReplyTemplate string

	// TTSStream toggles SSE streaming for Voxtral TTS. When true (the
	// default), audio frames stream to the device as they arrive from
	// the API, cutting time-to-first-audio from ~3 s to ~0.8 s. When
	// false, the gateway falls back to the M4 buffered WAV path.
	TTSStream bool

	// MistralPCMSampleRate is the sample rate Voxtral emits when we
	// request response_format="pcm" in streaming mode. Empirically
	// observed at 24 kHz; expose as an env var in case Mistral changes
	// the default. Only used when TTSStream is true.
	MistralPCMSampleRate int

	// ChatEnabled gates the M7 LLM reply path. When true (the default
	// once an API key is present), transcripts are passed through
	// Mistral chat completions before TTS instead of the template.
	// Disable to fall back to STTReplyTemplate / static greeting.
	ChatEnabled bool

	// MistralChatModel — chat completions model name. Defaults to
	// mistral-small-latest, which is fast (~600 ms first token for
	// short prompts) and capable enough for casual conversation.
	MistralChatModel string

	// ChatSystemPrompt frames the assistant's persona. Kept short and
	// audio-aware by default so replies don't exceed comfortable
	// listening length and don't contain markdown/code blocks the TTS
	// would mangle.
	ChatSystemPrompt string

	// ChatMaxTokens caps the assistant reply length. ~200 tokens ≈
	// 15-25 spoken seconds, which is the upper limit of "still feels
	// like a conversation, not a monologue".
	ChatMaxTokens int

	// ChatHistoryLimit caps how many user/assistant turns we replay on
	// each request. 6 = last 3 exchanges. Keeps prompt cost bounded
	// without losing immediate context.
	ChatHistoryLimit int

	// ChatToolsEnabled gates the M8b function-calling path. When true
	// (the default with API key + chat enabled + MCP discovery), the
	// gateway forwards the device's discovered MCP tools to chat
	// completions and routes any tool_calls back through MCP. Disable
	// to keep chat replies pure-conversational.
	ChatToolsEnabled bool

	// ChatToolMaxIter caps how many chat → tool_call → chat round-trips
	// we permit per user turn. 3 covers most multi-step actions
	// (e.g. get_head_angles → set_head_angles → confirm); the cap
	// guards against runaway loops where the model keeps calling
	// tools without converging.
	ChatToolMaxIter int

	// ChatToolBlocklist is a comma-separated list of MCP tool names
	// that we filter OUT of the tool list sent to Mistral. Use this
	// for tools the device exposes but the gateway can't fully
	// support yet — e.g. self.camera.take_photo, which requires a
	// vision-explain endpoint we don't run yet (the device captures
	// the photo but the upload to explain_url fails, leading the
	// model to apologize for being unable to take pictures).
	//
	// Default blocks take_photo; override with GATEWAY_CHAT_TOOL_BLOCK.
	// Set to "-" (a single dash) to disable the blocklist entirely.
	ChatToolBlocklist []string
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
			TTSPeakTarget:    envInt("GATEWAY_TTS_PEAK", 28000),
			STTModel:             os.Getenv("MISTRAL_STT_MODEL"),
			STTLanguage:          os.Getenv("MISTRAL_STT_LANGUAGE"),
			STTReplyTemplate:     envOr("GATEWAY_STT_REPLY", "You said: %s"),
			TTSStream:            envBool("GATEWAY_TTS_STREAM", true),
			MistralPCMSampleRate: envInt("MISTRAL_TTS_PCM_RATE", 24000),
			ChatEnabled:          envBool("GATEWAY_CHAT_ENABLED", true),
			MistralChatModel:     envOr("MISTRAL_CHAT_MODEL", "mistral-small-latest"),
			ChatSystemPrompt: envOr("GATEWAY_CHAT_SYSTEM",
				"You are StackChan, a small friendly desktop robot with a "+
					"physical body: a head you can move, an LED you can color, "+
					"and reminders you can set. Use your tools to act on the "+
					"world when the user asks for something physical. After a "+
					"successful tool call, briefly confirm what you did. "+
					"Reply in 1-2 short spoken sentences — plain prose, no "+
					"markdown, no lists, no code blocks, no emoji."),
			ChatMaxTokens:    envInt("GATEWAY_CHAT_MAX_TOKENS", 200),
			ChatHistoryLimit: envInt("GATEWAY_CHAT_HISTORY", 6),
			ChatToolsEnabled:  envBool("GATEWAY_CHAT_TOOLS", true),
			ChatToolMaxIter:   envInt("GATEWAY_CHAT_TOOL_MAX_ITER", 3),
			ChatToolBlocklist: parseBlocklist(envOr("GATEWAY_CHAT_TOOL_BLOCK", "self.camera.take_photo")),
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

// parseBlocklist splits a comma-separated string into a trimmed list
// of tool names. The sentinel "-" disables the blocklist (returns
// nil) so operators can opt into seeing every tool, even known-broken
// ones (useful for debugging device behavior).
func parseBlocklist(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "-" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func envBool(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	switch v {
	case "1", "true", "TRUE", "yes", "on":
		return true
	case "0", "false", "FALSE", "no", "off":
		return false
	}
	return def
}
