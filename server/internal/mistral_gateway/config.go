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
	// support yet.
	//
	// As of M9, take_photo IS supported when VisionEnabled (the
	// default with API key); when vision is disabled, take_photo is
	// auto-added to the blocklist by Get() so it never reaches the
	// model. Operators can append more entries via
	// GATEWAY_CHAT_TOOL_BLOCK; "-" disables blocking entirely.
	ChatToolBlocklist []string

	// VisionEnabled gates the M9 photo / image-explain pipeline.
	// When true (default with API key), the gateway:
	//   - sends MCP `initialize` with capabilities.vision = {url, token}
	//     so the device's take_photo tool knows where to upload JPEGs
	//   - exposes POST /xiaozhi/vision/explain to receive uploads
	//   - saves photos to PhotoDir
	//   - calls MistralVisionModel for non-empty questions
	// When false, take_photo is auto-blocked from the chat tool list.
	VisionEnabled bool

	// MistralVisionModel — chat completions model used for image
	// understanding. mistral-medium-latest is multimodal and gives
	// good descriptions; pixtral-large-latest is a heavier alternative.
	MistralVisionModel string

	// VisionExplainURL is the URL the device POSTs JPEGs to. Empty
	// means "auto-derive from WSURL" (swap ws→http / wss→https,
	// replace path with /xiaozhi/vision/explain). Override via
	// GATEWAY_VISION_URL when running behind a reverse proxy or
	// custom routing.
	VisionExplainURL string

	// PhotoDir is where saved JPEGs land. "./photos" lives in the
	// repo so you can browse them in your editor; gitignored. Empty
	// disables saving (the gateway still serves the explain endpoint
	// but doesn't persist anything).
	PhotoDir string

	// VisionMaxImageBytes caps the size of an inbound JPEG. The
	// ESP32 camera produces ~30-80 KB at 320×240; 1 MB is a generous
	// safety bound that defends against accidental misuse. Larger
	// uploads get HTTP 413.
	VisionMaxImageBytes int

	// VisionPromptWrapper frames the user's question for the vision
	// model. Default keeps replies short and audio-appropriate;
	// %s is replaced with the question text.
	VisionPromptWrapper string
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
					"a camera you can use, and reminders you can set. Use your "+
					"tools to act on the world when the user asks for something "+
					"physical. After a successful tool call, briefly confirm "+
					"what you did. "+
					"Camera convention: when the user asks you to JUST take or "+
					"save a photo, call take_photo with question=\"\" (empty). "+
					"When they ask what you can see or anything ABOUT the scene, "+
					"pass their question as the question argument so you receive "+
					"a description back. "+
					"Reply in 1-2 short spoken sentences — plain prose, no "+
					"markdown, no lists, no code blocks, no emoji."),
			ChatMaxTokens:    envInt("GATEWAY_CHAT_MAX_TOKENS", 200),
			ChatHistoryLimit: envInt("GATEWAY_CHAT_HISTORY", 6),
			ChatToolsEnabled:    envBool("GATEWAY_CHAT_TOOLS", true),
			ChatToolMaxIter:     envInt("GATEWAY_CHAT_TOOL_MAX_ITER", 3),
			VisionEnabled:       envBool("GATEWAY_VISION_ENABLED", true),
			MistralVisionModel:  envOr("MISTRAL_VISION_MODEL", "mistral-medium-latest"),
			VisionExplainURL:    os.Getenv("GATEWAY_VISION_URL"),
			PhotoDir:            envOr("GATEWAY_PHOTO_DIR", "./photos"),
			VisionMaxImageBytes: envInt("GATEWAY_VISION_MAX_BYTES", 1024*1024),
			VisionPromptWrapper: envOr("GATEWAY_VISION_PROMPT",
				"Look at this photo through StackChan's camera and answer briefly "+
					"(1-2 short sentences) suitable for spoken reply. Question: %s"),
		}

		// Blocklist resolution: when vision is OFF (or the API key is
		// missing — implies no vision either), auto-block take_photo
		// since the device upload would fail. Operator can override
		// either way via GATEWAY_CHAT_TOOL_BLOCK.
		visionUsable := cfg.VisionEnabled && cfg.MistralAPIKey != ""
		defaultBlock := ""
		if !visionUsable {
			defaultBlock = "self.camera.take_photo"
		}
		cfg.ChatToolBlocklist = parseBlocklist(envOr("GATEWAY_CHAT_TOOL_BLOCK", defaultBlock))
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
