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
// committing to a single concrete shape.
type inboundJSON struct {
	Type      string `json:"type"`
	State     string `json:"state,omitempty"`     // listen: start|stop|detect
	Mode      string `json:"mode,omitempty"`      // listen: auto|manual|realtime
	Text      string `json:"text,omitempty"`      // listen detect: wake word
	SessionID string `json:"session_id,omitempty"`
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
		// Best-effort cleanup; codec has no Close in this binding.
		_ = sess
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

	out := helloOut{
		Type:      "hello",
		Transport: "websocket",
		SessionID: sess.ID,
		AudioParams: audioParams{
			SampleRate:    rate,
			FrameDuration: frameMS,
		},
	}
	if err := conn.WriteJSON(out); err != nil {
		return nil, err
	}

	_ = conn.SetReadDeadline(time.Time{})
	g.Log().Infof(ctx, "hello-ack sent  device_id=%s  session_id=%s", deviceID, sess.ID)
	return sess, nil
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
			if err := playbackReply(ctx, conn, sess); err != nil {
				g.Log().Errorf(ctx, "playback failed: %v", err)
			}
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
		if err := playbackReply(ctx, conn, sess); err != nil {
			g.Log().Errorf(ctx, "auto-flush playback: %v", err)
		}
	}
}

// echoFlushFrames triggers loopback playback after this many buffered
// opus frames. At 60 ms/frame this is ~3 s of audio.
const echoFlushFrames = 50

// playbackReply dispatches between M3/M4/M5 based on what's configured:
//   - No API key       → M3 echo (offline-friendly fallback)
//   - API key only     → M5 transcribe-and-reply (the user-facing
//                        "interactive demo" — speaks back what was said)
// To pin the static M4 greeting instead of M5, set GATEWAY_STT_REPLY="".
func playbackReply(ctx context.Context, conn *websocket.Conn, sess *Session) error {
	c := Get()
	if c.MistralAPIKey == "" {
		return playbackEcho(ctx, conn, sess)
	}
	if c.STTReplyTemplate == "" {
		// Static TTS path (M4) — no transcription, no echo of the user.
		return playbackTTS(ctx, conn, sess, c.TTSReplyText)
	}
	return playbackTranscribeAndReply(ctx, conn, sess)
}

// playbackTranscribeAndReply (M5):
//  1. Drain the buffered PCM
//  2. Send to Voxtral STT
//  3. Format the transcript via STTReplyTemplate
//  4. Synthesize via Voxtral TTS
//  5. Stream back through the existing playback path
//
// Falls back to playbackTTS(static greeting) on STT failure so the
// device always gets some response.
func playbackTranscribeAndReply(ctx context.Context, conn *websocket.Conn, sess *Session) error {
	pcm := sess.DrainPCM()
	if len(pcm) == 0 {
		g.Log().Warningf(ctx, "stt skipped: no PCM buffered")
		return playbackTTS(ctx, conn, sess, Get().TTSReplyText)
	}

	t0 := time.Now()
	g.Log().Infof(ctx,
		"stt request  device_id=%s  samples=%d  duration_ms=%d",
		sess.DeviceID, len(pcm), len(pcm)*1000/sess.Codec.sampleRate,
	)

	transcript, err := TranscribeAudio(ctx, pcm, sess.Codec.sampleRate, Get().STTLanguage)
	if err != nil {
		g.Log().Errorf(ctx, "stt failed, falling back to static TTS: %v", err)
		return playbackTTS(ctx, conn, sess, Get().TTSReplyText)
	}
	g.Log().Infof(ctx,
		"stt response device_id=%s  api_ms=%d  transcript=%q",
		sess.DeviceID, time.Since(t0).Milliseconds(), transcript,
	)

	if transcript == "" {
		// Voxtral returned empty (silence or unintelligible) — say so.
		return playbackTTS(ctx, conn, sess, "I didn't catch that.")
	}

	reply := fmt.Sprintf(Get().STTReplyTemplate, transcript)
	return playbackTTS(ctx, conn, sess, reply)
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

// playbackTTS synthesizes `text` via Voxtral, resamples to the device's
// negotiated rate, opus-encodes 60 ms frames, and streams them to the
// device wrapped in tts:start/stop. M4 milestone.
func playbackTTS(ctx context.Context, conn *websocket.Conn, sess *Session, text string) error {
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
	if err := conn.WriteJSON(map[string]string{"type": "tts", "state": "start"}); err != nil {
		return err
	}
	if err := conn.WriteJSON(map[string]string{
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
		if err := conn.WriteMessage(websocket.BinaryMessage, bp2); err != nil {
			return err
		}
		ts += frameMS
		// Don't sleep before the first frame; we want sub-100 ms time
		// to first audio.
		if i < len(frames)-1 {
			<-ticker.C
		}
	}

	if err := conn.WriteJSON(map[string]string{"type": "tts", "state": "stop"}); err != nil {
		return err
	}
	g.Log().Infof(context.Background(),
		"playback done  device_id=%s  duration=%dms  frames=%d",
		sess.DeviceID, ts, len(frames))
	return nil
}
