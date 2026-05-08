/*
SPDX-FileCopyrightText: 2026 M5Stack Technology CO LTD
SPDX-License-Identifier: MIT
*/

package mistral_gateway

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gogf/gf/v2/frame/g"
	"github.com/gogf/gf/v2/net/ghttp"
)

// VisionExplainHandler implements POST /xiaozhi/vision/explain.
//
// Request shape (per firmware esp32_camera.cc::Explain):
//   POST /xiaozhi/vision/explain
//   Authorization: Bearer <session-token>
//   Device-Id: <mac>
//   Client-Id: <uuid>
//   Content-Type: multipart/form-data; boundary=...
//
//   --boundary
//   Content-Disposition: form-data; name="question"
//
//   <user's question text, possibly empty>
//   --boundary
//   Content-Disposition: form-data; name="file"; filename="camera.jpg"
//   Content-Type: image/jpeg
//
//   <raw JPEG bytes>
//   --boundary--
//
// Response: HTTP 200 with plain text body. The device returns the
// body verbatim as the MCP tools/call result, which then becomes the
// 'tool' role message in the chat loop. So whatever we return here
// is what the model "sees" as the photo's analysis.
//
// Behavior matrix:
//
//   question == "" (whitespace ok)  → save only, return "Photo saved (path)"
//   question != ""                  → save + ExplainImage(jpeg, question)
//   PhotoDir == ""                  → don't save (analyze-only mode)
//   VisionEnabled == false          → 503 Service Unavailable
func VisionExplainHandler(r *ghttp.Request) {
	ctx := r.Context()
	c := Get()

	if !c.VisionEnabled {
		respondText(r, http.StatusServiceUnavailable,
			"vision is disabled on this gateway")
		return
	}

	// Auth: per-session bearer token. Stops anyone on the LAN from
	// burning your Mistral quota by POSTing arbitrary images.
	token := bearerToken(r.Header.Get("Authorization"))
	if token == "" {
		g.Log().Warningf(ctx, "vision: missing or malformed Authorization")
		respondText(r, http.StatusUnauthorized, "missing bearer token")
		return
	}
	sess := LookupSessionByToken(token)
	if sess == nil {
		g.Log().Warningf(ctx, "vision: unknown token (no active session)")
		respondText(r, http.StatusUnauthorized, "unknown session")
		return
	}

	// Cap the read at MaxBytes so a runaway upload can't OOM us.
	r.Request.Body = http.MaxBytesReader(r.Response.Writer,
		r.Request.Body, int64(c.VisionMaxImageBytes)+64*1024) // +64KB for multipart overhead

	if err := r.Request.ParseMultipartForm(int64(c.VisionMaxImageBytes)); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			g.Log().Warningf(ctx, "vision: upload too large (limit=%d)", c.VisionMaxImageBytes)
			respondText(r, http.StatusRequestEntityTooLarge,
				fmt.Sprintf("image too large (max %d bytes)", c.VisionMaxImageBytes))
			return
		}
		g.Log().Warningf(ctx, "vision: multipart parse: %v", err)
		respondText(r, http.StatusBadRequest, "invalid multipart body")
		return
	}

	question := strings.TrimSpace(r.Request.FormValue("question"))

	file, header, err := r.Request.FormFile("file")
	if err != nil {
		g.Log().Warningf(ctx, "vision: missing file part: %v", err)
		respondText(r, http.StatusBadRequest, "missing 'file' part")
		return
	}
	defer file.Close()

	jpeg, err := io.ReadAll(file)
	if err != nil {
		g.Log().Warningf(ctx, "vision: read file: %v", err)
		respondText(r, http.StatusBadRequest, "failed to read file")
		return
	}
	if len(jpeg) == 0 {
		respondText(r, http.StatusBadRequest, "empty file")
		return
	}

	g.Log().Infof(ctx,
		"vision request  device_id=%s  bytes=%d  filename=%s  question_len=%d",
		sess.DeviceID, len(jpeg), header.Filename, len(question))

	// Save to disk if PhotoDir is configured. We always save (even
	// when analyzing) so you can later eyeball what the model saw.
	savedPath := ""
	if c.PhotoDir != "" {
		path, err := savePhoto(c.PhotoDir, sess.DeviceID, jpeg)
		if err != nil {
			// Logged but non-fatal — the explain path can still proceed.
			g.Log().Warningf(ctx, "vision: save failed: %v", err)
		} else {
			savedPath = path
			g.Log().Infof(ctx, "vision saved  path=%s  bytes=%d", path, len(jpeg))
		}
	}

	// Save-only path: empty question → no Pixtral call.
	if question == "" {
		reply := "Photo saved."
		if savedPath != "" {
			reply = fmt.Sprintf("Photo saved as %s.", filepath.Base(savedPath))
		}
		respondText(r, http.StatusOK, reply)
		return
	}

	// Analyze path.
	t0 := time.Now()
	answer, err := ExplainImage(ctx, jpeg, question)
	apiMS := time.Since(t0).Milliseconds()
	if err != nil {
		g.Log().Errorf(ctx, "vision explain failed (api_ms=%d): %v", apiMS, err)
		// Return 200 with an apology so the device's tool call still
		// resolves to text the chat loop can use. A non-200 would
		// throw a runtime_error on the firmware side.
		respondText(r, http.StatusOK,
			"I couldn't analyze the photo just now.")
		return
	}
	g.Log().Infof(ctx,
		"vision explain ok  api_ms=%d  question=%q  reply_chars=%d",
		apiMS, question, len(answer))
	respondText(r, http.StatusOK, answer)
}

// bearerToken extracts the token from an "Authorization: Bearer xxx"
// header. Returns "" if the header is missing or malformed.
func bearerToken(h string) string {
	const prefix = "Bearer "
	if !strings.HasPrefix(h, prefix) {
		return ""
	}
	return strings.TrimSpace(h[len(prefix):])
}

// respondText writes a plain-text response with the given status.
// Used for both success (the model's answer) and error replies.
func respondText(r *ghttp.Request, status int, body string) {
	r.Response.Header().Set("Content-Type", "text/plain; charset=utf-8")
	r.Response.WriteHeader(status)
	r.Response.Write([]byte(body))
}

// savePhoto writes the JPEG to PhotoDir with a sortable timestamped
// filename including the last 6 chars of the device MAC for easy
// per-device filtering.
//
// Filename: YYYYMMDD_HHMMSS_<deviceShort>.jpg
//   e.g.    20260508_230145_e25c8a.jpg
//
// Creates dir on first use.
func savePhoto(dir, deviceID string, jpeg []byte) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir: %w", err)
	}
	name := photoFilename(time.Now(), deviceID)
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, jpeg, 0o644); err != nil {
		return "", fmt.Errorf("write: %w", err)
	}
	return path, nil
}

// photoFilename builds the canonical save filename. Pulled out so
// tests can pin a clock value.
func photoFilename(now time.Time, deviceID string) string {
	short := deviceShortID(deviceID)
	return fmt.Sprintf("%s_%s.jpg",
		now.Format("20060102_150405"),
		short,
	)
}

// deviceShortID extracts a 6-char identifier from a MAC-like
// device ID. Drops colons and dashes, lowercases, takes the last 6.
// Returns "unknown" for empty / very short input.
func deviceShortID(deviceID string) string {
	clean := strings.Map(func(r rune) rune {
		switch r {
		case ':', '-', '_', ' ':
			return -1
		}
		return r
	}, strings.ToLower(deviceID))
	if len(clean) < 6 {
		if clean == "" {
			return "unknown"
		}
		return clean
	}
	return clean[len(clean)-6:]
}

// generateVisionToken returns 32 hex chars of crypto-random bytes.
// Used as the per-session bearer token in capabilities.vision.token.
func generateVisionToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// effectiveVisionURL resolves the URL we advertise to the device.
// Priority:
//  1. Config.VisionExplainURL (operator override via env)
//  2. Auto-derived from Config.WSURL (swap scheme + path)
//
// Returns "" if no URL can be derived (caller should skip vision
// initialization rather than send a broken capability).
func effectiveVisionURL(c Config) string {
	if c.VisionExplainURL != "" {
		return c.VisionExplainURL
	}
	parsed, err := url.Parse(c.WSURL)
	if err != nil || parsed.Host == "" {
		return ""
	}
	switch parsed.Scheme {
	case "ws":
		parsed.Scheme = "http"
	case "wss":
		parsed.Scheme = "https"
	default:
		return "" // unknown scheme — refuse to guess
	}
	parsed.Path = "/xiaozhi/vision/explain"
	parsed.RawQuery = ""
	return parsed.String()
}
