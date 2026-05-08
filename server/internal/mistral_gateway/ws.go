/*
SPDX-FileCopyrightText: 2026 M5Stack Technology CO LTD
SPDX-License-Identifier: MIT
*/

package mistral_gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/gogf/gf/v2/frame/g"
	"github.com/gogf/gf/v2/net/ghttp"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

// helloTimeout matches the device-side abort window. Per
// docs/architecture/07-path-a-implementation.md, the firmware aborts
// the connection if no hello reply arrives within 10 s.
const helloTimeout = 10 * time.Second

var wsUpgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

// helloIn is the first text frame the device sends after WS connect.
// Field set per xiaozhi-esp32 v2.2.4 websocket_protocol.cc.
type helloIn struct {
	Type        string         `json:"type"`     // "hello"
	Version     int            `json:"version"`  // BinaryProtocol version
	Transport   string         `json:"transport"`
	Features    map[string]any `json:"features"`
	AudioParams audioParams    `json:"audio_params"`
}

// helloOut is the gateway reply. The device honors the audio_params we
// echo back, so we can later override sample_rate to 24 kHz for TTS.
type helloOut struct {
	Type        string      `json:"type"`        // "hello"
	Transport   string      `json:"transport"`   // "websocket"
	SessionID   string      `json:"session_id"`
	AudioParams audioParams `json:"audio_params"`
}

type audioParams struct {
	Format        string `json:"format,omitempty"`
	SampleRate    int    `json:"sample_rate"`
	Channels      int    `json:"channels,omitempty"`
	FrameDuration int    `json:"frame_duration"`
}

// inboundJSON is a partial decode used to dispatch on `type` without
// committing to a single concrete shape. MCP messages carry a Payload
// field which is the JSON-RPC body forwarded to MCPClient.
type inboundJSON struct {
	Type      string          `json:"type"`
	State     string          `json:"state,omitempty"`     // listen: start|stop|detect
	Mode      string          `json:"mode,omitempty"`      // listen: auto|manual|realtime
	Text      string          `json:"text,omitempty"`      // listen detect: wake word
	SessionID string          `json:"session_id,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`   // mcp: JSON-RPC body
}

// WsHandler implements WSS /xiaozhi/v1/. M3 milestone: hello echo,
// then per-frame opus loopback (decode → re-encode → buffer → on
// `listen state:stop`, replay as a TTS response).
func WsHandler(r *ghttp.Request) {
	ctx := r.Context()
	deviceID := r.Header.Get("Device-Id")
	clientID := r.Header.Get("Client-Id")
	authz := r.Header.Get("Authorization")

	g.Log().Infof(ctx,
		"ws upgrade  device_id=%s  client_id=%s  authz_present=%t",
		deviceID, clientID, authz != "",
	)

	conn, err := wsUpgrader.Upgrade(r.Response.Writer, r.Request, nil)
	if err != nil {
		g.Log().Errorf(ctx, "ws upgrade failed: %v", err)
		return
	}
	defer conn.Close()

	sess, err := exchangeHello(ctx, conn, deviceID)
	if err != nil {
		g.Log().Warningf(ctx, "ws hello failed device_id=%s: %v", deviceID, err)
		return
	}
	defer func() {
		// Best-effort cleanup. codec has no Close in this binding;
		// session token must be removed from the global registry so
		// later HTTP requests with this token are rejected.
		if sess != nil {
			UnregisterSessionToken(sess.VisionToken)
		}
	}()

	runSession(ctx, conn, sess)
}

func exchangeHello(ctx context.Context, conn *websocket.Conn, deviceID string) (*Session, error) {
	if err := conn.SetReadDeadline(time.Now().Add(helloTimeout)); err != nil {
		return nil, err
	}

	mt, payload, err := conn.ReadMessage()
	if err != nil {
		return nil, err
	}
	if mt != websocket.TextMessage {
		g.Log().Warningf(ctx, "ws first frame not text (type=%d)", mt)
	}

	var in helloIn
	if err := json.Unmarshal(payload, &in); err != nil {
		return nil, err
	}
	if in.Type != "hello" {
		g.Log().Warningf(ctx, "ws first message type=%q (expected hello)", in.Type)
	}

	rate := in.AudioParams.SampleRate
	frameMS := in.AudioParams.FrameDuration
	channels := in.AudioParams.Channels
	if channels == 0 {
		channels = 1
	}
	g.Log().Infof(ctx,
		"hello in  device_id=%s  format=%s  rate=%d  frame=%dms  channels=%d  features=%v",
		deviceID, in.AudioParams.Format, rate, frameMS, channels, in.Features,
	)

	codec, err := NewOpusCodec(rate, frameMS, channels)
	if err != nil {
		return nil, err
	}

	sess := &Session{
		ID:         uuid.NewString(),
		DeviceID:   deviceID,
		BP2Version: uint16(Get().OpusVersion),
		Codec:      codec,
	}
	sess.MCP = NewMCPClient(conn, sess.ID, &sess.WriteMu)

	out := helloOut{
		Type:      "hello",
		Transport: "websocket",
		SessionID: sess.ID,
		AudioParams: audioParams{
			SampleRate:    rate,
			FrameDuration: frameMS,
		},
	}
	sess.WriteMu.Lock()
	wErr := conn.WriteJSON(out)
	sess.WriteMu.Unlock()
	if wErr != nil {
		return nil, wErr
	}

	_ = conn.SetReadDeadline(time.Time{})
	g.Log().Infof(ctx, "hello-ack sent  device_id=%s  session_id=%s", deviceID, sess.ID)

	// Kick off MCP setup (initialize + tools/list discovery) in the
	// background. The device sends responses on the same WS, picked
	// up by handleJSON and routed through sess.MCP.HandleResponse.
	//
	// Initialize MUST come before tools/list so the device has the
	// vision URL configured before the model can call take_photo.
	mcpFeature, _ := in.Features["mcp"].(bool)
	if mcpFeature {
		// Generate per-session vision token here (synchronously, so
		// it's available before the background goroutine reads it).
		// Token must be registered BEFORE the device gets it via
		// initialize, so the first HTTP request can succeed.
		c := Get()
		if c.VisionEnabled && c.MistralAPIKey != "" {
			tok, err := generateVisionToken()
			if err != nil {
				g.Log().Warningf(ctx,
					"vision token generation failed; vision disabled this session: %v", err)
			} else {
				sess.VisionToken = tok
				RegisterSessionToken(tok, sess)
			}
		}
		go setupMCP(context.Background(), sess)
	} else {
		g.Log().Infof(ctx,
			"mcp discovery skipped  device_id=%s  features.mcp=%v",
			deviceID, in.Features["mcp"])
	}

	return sess, nil
}

// setupMCP runs the MCP handshake: initialize (with vision
// capabilities, if enabled) then tools/list discovery. Errors at
// either step are logged but not fatal — the device stays usable
// for plain conversation even if MCP setup fails.
func setupMCP(ctx context.Context, sess *Session) {
	c := Get()

	// Step 1: initialize with capabilities. Skip if vision is off
	// or the URL can't be derived — sending an empty capabilities
	// block is harmless but pointless.
	if sess.VisionToken != "" {
		visionURL := effectiveVisionURL(c)
		if visionURL == "" {
			g.Log().Warningf(ctx,
				"vision url could not be derived from WSURL=%s; skipping initialize",
				c.WSURL)
		} else {
			caps := map[string]any{
				"vision": map[string]any{
					"url":   visionURL,
					"token": sess.VisionToken,
				},
			}
			t0 := time.Now()
			if err := sess.MCP.Initialize(ctx, caps); err != nil {
				g.Log().Warningf(ctx,
					"mcp initialize failed  device_id=%s  api_ms=%d: %v",
					sess.DeviceID, time.Since(t0).Milliseconds(), err)
				// Continue to discovery anyway — non-vision tools still work.
			} else {
				g.Log().Infof(ctx,
					"mcp initialize ok  device_id=%s  api_ms=%d  vision_url=%s",
					sess.DeviceID, time.Since(t0).Milliseconds(), visionURL)
			}
		}
	}

	// Step 2: discovery.
	discoverTools(ctx, sess)
}

// discoverTools runs `tools/list` (with pagination) and caches the
// result on the session. Errors are logged but not fatal — the device
// stays usable for plain conversation even if MCP discovery fails.
func discoverTools(ctx context.Context, sess *Session) {
	t0 := time.Now()
	listCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	tools, err := sess.MCP.ListTools(listCtx)
	sess.ToolsMu.Lock()
	sess.Tools = tools
	sess.ToolsErr = err
	sess.ToolsDone = true
	sess.ToolsMu.Unlock()

	if err != nil {
		g.Log().Warningf(ctx,
			"mcp tools/list failed  device_id=%s  api_ms=%d: %v",
			sess.DeviceID, time.Since(t0).Milliseconds(), err)
		return
	}
	names := make([]string, len(tools))
	for i, t := range tools {
		names[i] = t.Name
	}
	blocked := Get().ChatToolBlocklist
	if len(blocked) > 0 {
		filtered := FilterTools(tools, blocked)
		g.Log().Infof(ctx,
			"mcp tools/list ok  device_id=%s  api_ms=%d  count=%d  exposed=%d  blocked=%v  tools=%v",
			sess.DeviceID, time.Since(t0).Milliseconds(), len(tools), len(filtered), blocked, names)
	} else {
		g.Log().Infof(ctx,
			"mcp tools/list ok  device_id=%s  api_ms=%d  count=%d  tools=%v",
			sess.DeviceID, time.Since(t0).Milliseconds(), len(tools), names)
	}
}

// runSession is the per-connection main loop. It reads frames forever
// (until the WS closes) and dispatches:
//   - text JSON  → handleJSON  (listen start/stop, etc.)
//   - binary BP2 → handleAudio (decode, re-encode, buffer)
func runSession(ctx context.Context, conn *websocket.Conn, sess *Session) {
	for {
		mt, payload, err := conn.ReadMessage()
		if err != nil {
			g.Log().Infof(ctx, "ws closed  device_id=%s: %v", sess.DeviceID, err)
			return
		}
		switch mt {
		case websocket.TextMessage:
			handleJSON(ctx, conn, sess, payload)
		case websocket.BinaryMessage:
			handleAudio(ctx, conn, sess, payload)
		default:
			g.Log().Debugf(ctx, "ws<- other type=%d bytes=%d", mt, len(payload))
		}
	}
}

func handleJSON(ctx context.Context, conn *websocket.Conn, sess *Session, payload []byte) {
	var msg inboundJSON
	if err := json.Unmarshal(payload, &msg); err != nil {
		g.Log().Warningf(ctx, "ws<- text invalid json: %v body=%s", err, string(payload))
		return
	}
	g.Log().Debugf(ctx,
		"ws<- text  type=%s  state=%s  mode=%s",
		msg.Type, msg.State, msg.Mode,
	)

	switch msg.Type {
	case "listen":
		switch msg.State {
		case "start":
			sess.StartListening()
		case "detect":
			// Wake-word event; treat like listen start so we capture
			// any subsequent audio.
			sess.StartListening()
		case "stop":
			sess.StopListening()
			triggerReply(ctx, conn, sess, "listen:stop")
		}
	case "mcp":
		// Inbound MCP message — could be a response to one of our
		// outstanding requests (tools/list, tools/call), or a
		// notification the device pushed unprompted (rare; the device
		// is the MCP server, not a notification source we expect).
		// Route by JSON-RPC ID; unmatched IDs are dropped silently.
		if sess.MCP == nil || len(msg.Payload) == 0 {
			g.Log().Debugf(ctx, "mcp inbound ignored (mcp=%t payload=%dB)",
				sess.MCP != nil, len(msg.Payload))
			return
		}
		if !sess.MCP.HandleResponse(msg.Payload) {
			g.Log().Debugf(ctx,
				"mcp inbound unmatched  device_id=%s  payload=%s",
				sess.DeviceID, string(msg.Payload))
		}
	}
}

func handleAudio(ctx context.Context, conn *websocket.Conn, sess *Session, payload []byte) {
	frame, err := DecodeBP2(payload)
	if err != nil {
		g.Log().Warningf(ctx, "bp2 decode failed: %v (bytes=%d, hex=% x...)",
			err, len(payload), payload[:min(8, len(payload))])
		return
	}
	if frame.Type != BP2TypeOpus {
		g.Log().Debugf(ctx, "bp2<- non-opus type=%d skip", frame.Type)
		return
	}
	if !sess.IsListening() {
		// Audio outside a listening window — the device can stream
		// briefly during state transitions; ignore silently.
		return
	}

	pcm, err := sess.Codec.Decode(frame.Payload)
	if err != nil {
		g.Log().Warningf(ctx, "opus decode failed: %v (bytes=%d)", err, len(frame.Payload))
		return
	}
	// Buffer raw PCM for STT (M5) BEFORE re-encoding — the re-encoded
	// opus is lossy and STT works better on the original samples.
	sess.BufferPCM(pcm)

	reEncoded, err := sess.Codec.Encode(pcm)
	if err != nil {
		g.Log().Warningf(ctx, "opus re-encode failed: %v (samples=%d)", err, len(pcm))
		return
	}
	sess.BufferEcho(reEncoded)

	// Heartbeat log every 50 frames (~3 s of audio) so the operator
	// sees the stream is flowing without spamming per-frame.
	n := len(sess.echoBuf)
	if n == 1 || n%50 == 0 {
		g.Log().Infof(ctx,
			"audio  frames=%d  in=%d B  out=%d B  pcm=%d samples",
			n, len(frame.Payload), len(reEncoded), len(pcm),
		)
	}

	// Auto-flush threshold. The StackChan UI doesn't send `listen:stop`
	// (the second screen-tap calls protocol_->CloseAudioChannel which
	// yanks the WS), so we can't wait for it. After ~3 s of audio,
	// pause listening and play it back. The device transitions to
	// kDeviceStateSpeaking on tts:start; subsequent frames during
	// playback are ignored because IsListening() is now false.
	if n >= echoFlushFrames {
		sess.StopListening()
		triggerReply(ctx, conn, sess, "auto-flush")
	}
}

// triggerReply spawns playbackReply in a goroutine, serialized by
// sess.ReplyMu. Two reasons to keep playback off the read loop:
//
//  1. M8b: the chat-tool loop sends MCP requests and waits on the
//     response channel. The response arrives on the WS read loop,
//     so the read loop MUST keep running while we wait.
//  2. The device can send other JSON (`listen:detect`, `iot`, etc.)
//     during a long reply; blocking the reader silently drops them.
//
// ReplyMu guarantees only one playback runs at a time per session.
// If a second trigger arrives mid-playback (e.g. user taps screen
// again), it queues behind the first and runs sequentially.
func triggerReply(ctx context.Context, conn *websocket.Conn, sess *Session, source string) {
	go func() {
		sess.ReplyMu.Lock()
		defer sess.ReplyMu.Unlock()
		if err := playbackReply(ctx, conn, sess); err != nil {
			g.Log().Errorf(ctx, "playback failed (%s): %v", source, err)
		}
	}()
}

// echoFlushFrames triggers loopback playback after this many buffered
// opus frames. At 60 ms/frame this is ~3 s of audio.
const echoFlushFrames = 50

// playbackReply dispatches between M3/M4/M5/M7 based on what's
// configured. Order matters — first match wins:
//
//	no API key                       → M3 echo (offline-friendly)
//	ChatEnabled  (default true)      → M7 STT → LLM → TTS
//	STTReplyTemplate set             → M5 STT → "You said: %s" → TTS
//	(neither chat nor template)      → M4 static greeting via TTS
//
// To force the M5 echo template instead of chat:
//	GATEWAY_CHAT_ENABLED=false
// To force the M4 static greeting:
//	GATEWAY_CHAT_ENABLED=false  GATEWAY_STT_REPLY=""
func playbackReply(ctx context.Context, conn *websocket.Conn, sess *Session) error {
	c := Get()
	if c.MistralAPIKey == "" {
		return playbackEcho(ctx, conn, sess)
	}
	if c.ChatEnabled {
		return playbackTranscribeAndReply(ctx, conn, sess)
	}
	if c.STTReplyTemplate != "" {
		return playbackTranscribeAndReply(ctx, conn, sess)
	}
	return playbackTTS(ctx, conn, sess, c.TTSReplyText)
}

// playbackTranscribeAndReply (M5 + M7):
//  1. Drain the buffered PCM
//  2. Send to Voxtral STT
//  3. Generate the reply text:
//       - if ChatEnabled  → Mistral chat completions with session history
//       - else            → format transcript via STTReplyTemplate
//  4. Synthesize via Voxtral TTS (streaming when possible)
//  5. Stream back through the existing playback path
//
// Each external call has a fallback: STT failure → static greeting,
// chat failure → STT template, TTS failure → re-encoded echo. The
// device always gets some response.
func playbackTranscribeAndReply(ctx context.Context, conn *websocket.Conn, sess *Session) error {
	c := Get()
	pcm := sess.DrainPCM()
	if len(pcm) == 0 {
		g.Log().Warningf(ctx, "stt skipped: no PCM buffered")
		return playbackTTS(ctx, conn, sess, c.TTSReplyText)
	}

	t0 := time.Now()
	g.Log().Infof(ctx,
		"stt request  device_id=%s  samples=%d  duration_ms=%d",
		sess.DeviceID, len(pcm), len(pcm)*1000/sess.Codec.sampleRate,
	)

	transcript, err := TranscribeAudio(ctx, pcm, sess.Codec.sampleRate, c.STTLanguage)
	if err != nil {
		g.Log().Errorf(ctx, "stt failed, falling back to static TTS: %v", err)
		return playbackTTS(ctx, conn, sess, c.TTSReplyText)
	}
	g.Log().Infof(ctx,
		"stt response device_id=%s  api_ms=%d  transcript=%q",
		sess.DeviceID, time.Since(t0).Milliseconds(), transcript,
	)

	if transcript == "" {
		// Voxtral returned empty — silence, background noise, or
		// just unintelligible audio. Stay quiet (no spoken filler)
		// but we MUST still bracket with tts:start/stop, otherwise
		// the device sits in its current "server processing" state
		// forever and stops accepting new audio.
		//
		// Per firmware/xiaozhi-esp32/main/application.cc:524, the
		// device's tts:stop handler only transitions out of
		// kDeviceStateSpeaking — so without a preceding tts:start
		// it's a no-op. Send both, with no audio between, and the
		// device flicks Speaking → Listening (auto mode) cleanly.
		g.Log().Infof(ctx,
			"empty transcript, sending silent ack  device_id=%s",
			sess.DeviceID)
		if err := sess.WriteJSON(conn, map[string]string{
			"type": "tts", "state": "start",
		}); err != nil {
			return err
		}
		return sess.WriteJSON(conn, map[string]string{
			"type": "tts", "state": "stop",
		})
	}

	reply := generateReplyText(ctx, sess, transcript)

	// Emotion extraction (M10): pull [emotion:NAME] out of the LLM
	// reply, send it to the device as an `llm` event so the avatar
	// face matches the spoken tone, and feed the cleaned text to TTS.
	// Skip on echo / template paths — those don't have model-emitted
	// emotion tags. The check is just "is the reply different from
	// what came in?" — generateReplyText returns the raw transcript
	// echo only when chat is off, in which case there's no tag to
	// parse anyway and ExtractEmotion is a no-op.
	if c.EmotionEnabled {
		emotion, cleaned := ExtractEmotion(reply)
		if emotion != "" {
			if !IsValidEmotion(emotion) {
				g.Log().Warningf(ctx,
					"emotion %q not in firmware allowlist; device will fall back to neutral",
					emotion)
			}
			g.Log().Infof(ctx,
				"emotion device_id=%s  emotion=%s  reply_chars=%d",
				sess.DeviceID, emotion, len(cleaned))
			if err := sess.WriteJSON(conn, map[string]string{
				"type":    "llm",
				"emotion": emotion,
			}); err != nil {
				// Non-fatal: TTS still works without the avatar update.
				g.Log().Warningf(ctx, "send llm/emotion failed: %v", err)
			}
			reply = cleaned
		}
	}
	return playbackTTS(ctx, conn, sess, reply)
}

// generateReplyText produces the assistant reply text from the user's
// transcript. Routes through Mistral chat when enabled. When tools
// are also enabled AND the device has finished MCP discovery with at
// least one tool, uses the M8b function-calling loop; otherwise the
// plain M7 chat path. Falls back to the M5 template / "got it" on
// chat failure.
func generateReplyText(ctx context.Context, sess *Session, transcript string) string {
	c := Get()
	if c.ChatEnabled {
		// Snapshot the tool list under the cached lock — discovery
		// runs in a goroutine so the slice can grow concurrently.
		// Apply the blocklist filter before forwarding to Mistral so
		// the model never sees tools we can't service end-to-end.
		var sessionTools []Tool
		if c.ChatToolsEnabled {
			sess.ToolsMu.RLock()
			if sess.ToolsDone && len(sess.Tools) > 0 {
				sessionTools = make([]Tool, len(sess.Tools))
				copy(sessionTools, sess.Tools)
			}
			sess.ToolsMu.RUnlock()
			sessionTools = FilterTools(sessionTools, c.ChatToolBlocklist)
		}

		t0 := time.Now()
		hist := truncateHistory(sess.History, c.ChatHistoryLimit)

		var (
			reply       string
			calledTools []string
			err         error
		)
		if len(sessionTools) > 0 {
			g.Log().Infof(ctx,
				"chat request  device_id=%s  model=%s  history_msgs=%d  tools=%d  max_iter=%d  user=%q",
				sess.DeviceID, c.MistralChatModel, len(hist), len(sessionTools), c.ChatToolMaxIter, transcript,
			)
			tools := MapMCPToolsToMistral(sessionTools)
			executor := &mcpToolExecutor{client: sess.MCP}
			reply, calledTools, err = GenerateReplyWithTools(
				ctx, transcript, hist, tools, executor, c.ChatToolMaxIter,
			)
		} else {
			g.Log().Infof(ctx,
				"chat request  device_id=%s  model=%s  history_msgs=%d  tools=0  user=%q",
				sess.DeviceID, c.MistralChatModel, len(hist), transcript,
			)
			reply, err = GenerateReply(ctx, transcript, hist)
		}

		if err == nil && reply != "" {
			if len(calledTools) > 0 {
				g.Log().Infof(ctx,
					"chat response device_id=%s  api_ms=%d  tools_called=%v  reply=%q",
					sess.DeviceID, time.Since(t0).Milliseconds(), calledTools, reply,
				)
			} else {
				g.Log().Infof(ctx,
					"chat response device_id=%s  api_ms=%d  reply=%q",
					sess.DeviceID, time.Since(t0).Milliseconds(), reply,
				)
			}
			sess.AppendTurn(transcript, reply)
			return reply
		}
		g.Log().Errorf(ctx,
			"chat failed (api_ms=%d), falling back to template: %v",
			time.Since(t0).Milliseconds(), err,
		)
	}
	if c.STTReplyTemplate != "" {
		return fmt.Sprintf(c.STTReplyTemplate, transcript)
	}
	return "Got it."
}

// playbackEcho sends the buffered re-encoded opus back to the device,
// bracketed with `tts state:start` / `tts state:stop`. M3 fallback —
// proves the audio pipeline works without an API key.
func playbackEcho(ctx context.Context, conn *websocket.Conn, sess *Session) error {
	frames := sess.DrainEcho()
	if len(frames) == 0 {
		g.Log().Debugf(ctx, "playback skipped: no audio buffered")
		return nil
	}
	g.Log().Infof(ctx, "playback echo  device_id=%s  frames=%d", sess.DeviceID, len(frames))
	return streamFramesAsTTS(conn, sess, "(echo)", frames)
}

// playbackTTS synthesizes `text` via Voxtral and streams it back. When
// streaming is enabled (the default; see Config.TTSStream) it uses the
// SSE path for sub-second time-to-first-audio. Falls back to the M4
// buffered WAV path on stream failure or when explicitly disabled.
func playbackTTS(ctx context.Context, conn *websocket.Conn, sess *Session, text string) error {
	if Get().TTSStream {
		err := playbackTTSStream(ctx, conn, sess, text)
		if err == nil {
			return nil
		}
		g.Log().Warningf(ctx, "tts stream failed, falling back to buffered: %v", err)
	}
	return playbackTTSBuffered(ctx, conn, sess, text)
}

// playbackTTSBuffered is the original M4 path: request the full WAV,
// peak-normalize, encode all frames, then stream. Higher latency but
// fewer moving parts — kept as a fallback when streaming is disabled
// or the SSE call fails before any audio plays.
func playbackTTSBuffered(ctx context.Context, conn *websocket.Conn, sess *Session, text string) error {
	t0 := time.Now()
	c := Get()
	voiceHint := c.TTSVoice
	if voiceHint == "" {
		voiceHint = "(auto-discovered)"
	}
	g.Log().Infof(ctx, "tts request  device_id=%s  voice=%s  text=%q",
		sess.DeviceID, voiceHint, text)

	audio, err := SynthesizeSpeech(ctx, text)
	if err != nil {
		// Fall back to echo so the user still gets feedback that the
		// pipeline works (and so a transient API outage doesn't make
		// the device feel broken).
		g.Log().Errorf(ctx, "tts failed, falling back to echo: %v", err)
		return playbackEcho(ctx, conn, sess)
	}
	g.Log().Infof(ctx,
		"tts response  device_id=%s  src_rate=%d Hz  src_samples=%d  api_ms=%d",
		sess.DeviceID, audio.SampleRate, len(audio.PCM), time.Since(t0).Milliseconds(),
	)

	// Resample to whatever the device negotiated (typically 16 kHz).
	resampled := Resample16(audio.PCM, audio.SampleRate, sess.Codec.sampleRate)

	// Voxtral output is mastered conservatively — boost to a sensible
	// peak so the StackChan speaker is audible at normal listening
	// distance without the user reaching for the volume knob.
	boosted := PeakNormalize(resampled, c.TTSPeakTarget)

	chunks := SplitFrames(boosted, sess.Codec.frameSamples)

	frames := make([][]byte, 0, len(chunks))
	for _, pcm := range chunks {
		opusFrame, err := sess.Codec.Encode(pcm)
		if err != nil {
			return fmt.Errorf("opus encode TTS chunk: %w", err)
		}
		// Encode aliases an internal buffer; copy.
		cp := make([]byte, len(opusFrame))
		copy(cp, opusFrame)
		frames = append(frames, cp)
	}
	g.Log().Infof(ctx, "tts encoded  device_id=%s  opus_frames=%d", sess.DeviceID, len(frames))

	// Drop any buffered echo from the listening window — TTS replaces it.
	sess.DrainEcho()
	return streamFramesAsTTS(conn, sess, text, frames)
}

// streamFramesAsTTS writes the standard tts:start / sentence_start /
// binary opus frames / tts:stop sequence.
//
// Critical: frames are PACED at real-time (~60 ms each). The device's
// audio service has a finite ring buffer (a few hundred ms); if we
// burst all frames as fast as the WS can send, the buffer overflows
// and you hear the start of the reply, silence, then the tail-end
// re-trigger as the queue drains. Pacing slightly faster than playback
// keeps the buffer near-full without overrunning it.
func streamFramesAsTTS(conn *websocket.Conn, sess *Session, label string, frames [][]byte) error {
	if len(frames) == 0 {
		return nil
	}
	if err := sess.WriteJSON(conn, map[string]string{"type": "tts", "state": "start"}); err != nil {
		return err
	}
	if err := sess.WriteJSON(conn, map[string]string{
		"type": "tts", "state": "sentence_start", "text": label,
	}); err != nil {
		return err
	}

	const (
		frameMS  uint32        = 60
		paceTick               = 50 * time.Millisecond // ~10 ms ahead of playback
	)
	ticker := time.NewTicker(paceTick)
	defer ticker.Stop()

	var ts uint32
	for i, f := range frames {
		bp2 := EncodeBP2(sess.BP2Version, BP2TypeOpus, ts, f)
		if err := sess.WriteBinary(conn, bp2); err != nil {
			return err
		}
		ts += frameMS
		// Don't sleep before the first frame; we want sub-100 ms time
		// to first audio.
		if i < len(frames)-1 {
			<-ticker.C
		}
	}

	if err := sess.WriteJSON(conn, map[string]string{"type": "tts", "state": "stop"}); err != nil {
		return err
	}
	g.Log().Infof(context.Background(),
		"playback done  device_id=%s  duration=%dms  frames=%d",
		sess.DeviceID, ts, len(frames))
	return nil
}

// playbackTTSStream is the M6 streaming path. Time-to-first-audio is
// the round-trip to Voxtral plus one resampled+encoded frame (~60 ms),
// vs the buffered path which waits for the full WAV (~3 s for a short
// reply).
//
// Flow:
//
//	tts:start + sentence_start  ──>  device shows "Speaking" state
//	  ↓
//	for each SSE chunk from Voxtral:
//	    PCM (24 kHz f32 LE) ──> int16 ──> resample to device rate
//	      ↓ append to accumulator
//	    while acc has ≥ 60 ms:
//	      slice 60 ms frame ──> opus encode ──> BP2 ──> WS write
//	      pace 50 ms (≈10 ms ahead of device playback)
//	  ↓
//	stream done ──> zero-pad remainder, send final frame
//	  ↓
//	tts:stop
//
// Pacing is INSIDE the SSE callback. If the API stalls between chunks,
// the ticker just doesn't fire — we naturally synchronize to whichever
// is slower (network or playback). If the API streams faster than
// real-time (it does), TCP backpressure gates the upstream read at
// ~50 ms intervals, which is exactly what we want.
//
// Skipped vs the buffered path:
//   - PeakNormalize: needs the full waveform to find the peak.
//     Voxtral output is mastered conservatively (~-10 dBFS); volume
//     loss is the cost of streaming. Operator can boost speaker
//     volume or revisit with a streaming compressor later.
func playbackTTSStream(ctx context.Context, conn *websocket.Conn, sess *Session, text string) error {
	t0 := time.Now()
	c := Get()
	voiceHint := c.TTSVoice
	if voiceHint == "" {
		voiceHint = "(auto-discovered)"
	}
	g.Log().Infof(ctx, "tts stream req  device_id=%s  voice=%s  text=%q",
		sess.DeviceID, voiceHint, text)

	if err := sess.WriteJSON(conn, map[string]string{
		"type": "tts", "state": "start",
	}); err != nil {
		return fmt.Errorf("write tts:start: %w", err)
	}
	if err := sess.WriteJSON(conn, map[string]string{
		"type": "tts", "state": "sentence_start", "text": text,
	}); err != nil {
		return fmt.Errorf("write sentence_start: %w", err)
	}
	// Drop any buffered echo — TTS replaces it.
	sess.DrainEcho()

	const (
		frameMS  uint32        = 60
		paceTick               = 50 * time.Millisecond
	)
	ticker := time.NewTicker(paceTick)
	defer ticker.Stop()

	var (
		pcmAcc       []int16 // resampled, awaiting full-frame slicing
		ts           uint32
		framesSent   int
		firstChunkAt time.Time
	)
	frameSamples := sess.Codec.frameSamples
	dstRate := sess.Codec.sampleRate

	emit := func(frame []int16) error {
		opusFrame, err := sess.Codec.Encode(frame)
		if err != nil {
			return fmt.Errorf("opus encode: %w", err)
		}
		// Encode aliases an internal buffer; copy before WS send.
		cp := make([]byte, len(opusFrame))
		copy(cp, opusFrame)
		bp2 := EncodeBP2(sess.BP2Version, BP2TypeOpus, ts, cp)
		if err := sess.WriteBinary(conn, bp2); err != nil {
			return fmt.Errorf("ws write: %w", err)
		}
		ts += frameMS
		framesSent++
		// Skip pacing on the very first frame so TTFA is minimal.
		if framesSent > 1 {
			<-ticker.C
		}
		return nil
	}

	onChunk := func(samples []int16, srcRate int) error {
		if firstChunkAt.IsZero() {
			firstChunkAt = time.Now()
			g.Log().Infof(ctx,
				"tts stream first chunk  device_id=%s  ttfa_ms=%d  src_rate=%d  samples=%d",
				sess.DeviceID, time.Since(t0).Milliseconds(), srcRate, len(samples),
			)
		}
		resampled := Resample16(samples, srcRate, dstRate)
		pcmAcc = append(pcmAcc, resampled...)

		for len(pcmAcc) >= frameSamples {
			frame := pcmAcc[:frameSamples]
			pcmAcc = pcmAcc[frameSamples:]
			if err := emit(frame); err != nil {
				return err
			}
		}
		return nil
	}

	if err := SynthesizeSpeechStream(ctx, text, onChunk); err != nil {
		// Best-effort tts:stop so the device doesn't sit in Speaking forever.
		_ = sess.WriteJSON(conn, map[string]string{"type": "tts", "state": "stop"})
		return err
	}

	// Drain remainder: zero-pad to one final frame.
	if len(pcmAcc) > 0 {
		final := make([]int16, frameSamples)
		copy(final, pcmAcc)
		if err := emit(final); err != nil {
			return err
		}
		pcmAcc = nil
	}

	if err := sess.WriteJSON(conn, map[string]string{"type": "tts", "state": "stop"}); err != nil {
		return err
	}
	g.Log().Infof(ctx,
		"tts stream done  device_id=%s  total_ms=%d  duration=%dms  frames=%d",
		sess.DeviceID, time.Since(t0).Milliseconds(), ts, framesSent,
	)
	return nil
}
