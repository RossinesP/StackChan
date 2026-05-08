/*
SPDX-FileCopyrightText: 2026 M5Stack Technology CO LTD
SPDX-License-Identifier: MIT
*/

package mistral_gateway

import (
	"encoding/json"
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

// WsHandler implements WSS /xiaozhi/v1/. M2 milestone: upgrade,
// exchange hello, hold the connection open, log inbound frames.
//
// M3 (audio loopback), M4–M6 (TTS / STT / LLM), and M7+ (MCP tools)
// will land in sibling files: framing.go, opus.go, stt.go, tts.go,
// llm.go, mcp.go.
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

	if err := exchangeHello(ctx, conn, deviceID); err != nil {
		g.Log().Warningf(ctx, "ws hello failed device_id=%s: %v", deviceID, err)
		return
	}

	// M2 stops here: keep the connection alive and log every frame so
	// you can verify the device is happy with the handshake. Replace
	// with the per-session state machine in M3+.
	readLoop(ctx, conn, deviceID)
}

func exchangeHello(ctx g.Ctx, conn *websocket.Conn, deviceID string) error {
	if err := conn.SetReadDeadline(time.Now().Add(helloTimeout)); err != nil {
		return err
	}

	mt, payload, err := conn.ReadMessage()
	if err != nil {
		return err
	}
	if mt != websocket.TextMessage {
		g.Log().Warningf(ctx, "ws first frame not text (type=%d)", mt)
	}

	var in helloIn
	if err := json.Unmarshal(payload, &in); err != nil {
		return err
	}
	if in.Type != "hello" {
		g.Log().Warningf(ctx, "ws first message type=%q (expected hello)", in.Type)
	}
	g.Log().Infof(ctx,
		"hello in  device_id=%s  format=%s  rate=%d  frame=%dms  features=%v",
		deviceID,
		in.AudioParams.Format, in.AudioParams.SampleRate, in.AudioParams.FrameDuration,
		in.Features,
	)

	out := helloOut{
		Type:      "hello",
		Transport: "websocket",
		SessionID: uuid.NewString(),
		AudioParams: audioParams{
			SampleRate:    in.AudioParams.SampleRate,
			FrameDuration: in.AudioParams.FrameDuration,
		},
	}
	if err := conn.WriteJSON(out); err != nil {
		return err
	}

	// Clear the hello deadline; the read loop manages its own timeouts.
	_ = conn.SetReadDeadline(time.Time{})
	g.Log().Infof(ctx, "hello-ack sent  device_id=%s  session_id=%s", deviceID, out.SessionID)
	return nil
}

func readLoop(ctx g.Ctx, conn *websocket.Conn, deviceID string) {
	for {
		mt, payload, err := conn.ReadMessage()
		if err != nil {
			g.Log().Infof(ctx, "ws closed  device_id=%s: %v", deviceID, err)
			return
		}
		switch mt {
		case websocket.TextMessage:
			g.Log().Debugf(ctx, "ws<- text  device_id=%s  bytes=%d  body=%s",
				deviceID, len(payload), string(payload))
		case websocket.BinaryMessage:
			// M3 will decode this with framing.go.
			g.Log().Debugf(ctx, "ws<- bin   device_id=%s  bytes=%d", deviceID, len(payload))
		default:
			g.Log().Debugf(ctx, "ws<- other device_id=%s  type=%d  bytes=%d", deviceID, mt, len(payload))
		}
	}
}
