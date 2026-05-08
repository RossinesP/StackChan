/*
SPDX-FileCopyrightText: 2026 M5Stack Technology CO LTD
SPDX-License-Identifier: MIT
*/

package mistral_gateway

import (
	"github.com/gogf/gf/v2/frame/g"
	"github.com/gogf/gf/v2/net/ghttp"
	"github.com/google/uuid"
)

// otaResponse mirrors the shape xiaozhi-esp32's ota.cc expects. See
// docs/architecture/07-path-a-implementation.md "OTA discovery" for the
// reference. Empty `activation` block tells the device to skip code entry.
type otaResponse struct {
	WebSocket  websocketBlock  `json:"websocket"`
	Activation activationBlock `json:"activation,omitempty"`
}

type websocketBlock struct {
	URL     string `json:"url"`
	Token   string `json:"token"`
	Version int    `json:"version"`
}

type activationBlock struct {
	Message   string `json:"message,omitempty"`
	Code      string `json:"code,omitempty"`
	Challenge string `json:"challenge,omitempty"`
	TimeoutMS int    `json:"timeout_ms,omitempty"`
}

// OtaHandler implements POST /xiaozhi/ota/. The device hits this on
// boot, persists the returned WS URL + token in NVS, then dials the WS.
//
// M1 milestone: returns a static config with no per-device personalization.
// Per-device tokens (signed JWTs scoped to the gateway) come in M10.
func OtaHandler(r *ghttp.Request) {
	ctx := r.Context()
	c := Get()

	deviceID := r.Header.Get("Device-Id")
	clientID := r.Header.Get("Client-Id")
	g.Log().Infof(ctx,
		"ota request  device_id=%s  client_id=%s  ua=%q",
		deviceID, clientID, r.Header.Get("User-Agent"),
	)

	resp := otaResponse{
		WebSocket: websocketBlock{
			URL:     c.WSURL,
			Token:   "dev-" + uuid.NewString(), // M1: opaque, not validated yet
			Version: c.OpusVersion,
		},
		// Empty activation block — device proceeds straight to WS connect.
	}

	// Note: do NOT call r.Response.WriteStatus(200) — in GoFrame that
	// writes the status text ("OK") INTO the body, which makes the
	// device's cJSON_Parse fail. 200 is the default; just write JSON.
	r.Response.Header().Set("Content-Type", "application/json")
	r.Response.WriteJson(resp)
	g.Log().Infof(ctx,
		"ota response  device_id=%s  ws_url=%s  version=%d",
		deviceID, resp.WebSocket.URL, resp.WebSocket.Version,
	)
}
