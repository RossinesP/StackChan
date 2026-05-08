/*
SPDX-FileCopyrightText: 2026 M5Stack Technology CO LTD
SPDX-License-Identifier: MIT
*/

package mistral_gateway

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPhotoFilenameFormat(t *testing.T) {
	now := time.Date(2026, 5, 8, 23, 1, 45, 0, time.UTC)
	got := photoFilename(now, "44:1b:f6:e2:5c:a8")
	want := "20260508_230145_e25ca8.jpg"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestPhotoFilenameSortableOverTime(t *testing.T) {
	a := photoFilename(time.Date(2026, 5, 8, 23, 1, 45, 0, time.UTC), "abc:def")
	b := photoFilename(time.Date(2026, 5, 8, 23, 1, 46, 0, time.UTC), "abc:def")
	if !(a < b) {
		t.Errorf("filenames not sortable: %q !< %q", a, b)
	}
}

func TestDeviceShortIDVariants(t *testing.T) {
	cases := []struct{ in, want string }{
		{"44:1b:f6:e2:5c:a8", "e25ca8"},
		{"44-1B-F6-E2-5C-A8", "e25ca8"},
		{"441BF6E25CA8", "e25ca8"},
		{"e25ca8", "e25ca8"}, // already short
		{"", "unknown"},
		{"abc", "abc"},          // shorter than 6 — return as-is
		{"WX:YZ", "wxyz"},       // strip separators, lowercase
	}
	for _, tc := range cases {
		if got := deviceShortID(tc.in); got != tc.want {
			t.Errorf("deviceShortID(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSavePhotoCreatesDirAndWrites(t *testing.T) {
	dir := t.TempDir()
	subdir := filepath.Join(dir, "nested", "photos")
	jpeg := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10} // not a real JPEG, just bytes
	path, err := savePhoto(subdir, "44:1b:f6:e2:5c:a8", jpeg)
	if err != nil {
		t.Fatalf("savePhoto: %v", err)
	}
	if !strings.HasPrefix(path, subdir) {
		t.Errorf("path %q should be under %q", path, subdir)
	}
	if !strings.HasSuffix(path, "_e25ca8.jpg") {
		t.Errorf("path %q should end with device short id", path)
	}
	// Verify file exists with the right contents.
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if len(got) != len(jpeg) {
		t.Errorf("size = %d, want %d", len(got), len(jpeg))
	}
}

func TestBearerToken(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Bearer abc123", "abc123"},
		{"Bearer  spaced  ", "spaced"},
		{"Token abc123", ""}, // wrong scheme
		{"", ""},
		{"Bearer", ""}, // no value
	}
	for _, tc := range cases {
		if got := bearerToken(tc.in); got != tc.want {
			t.Errorf("bearerToken(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSessionTokenRegistry(t *testing.T) {
	sess := &Session{ID: "s1", DeviceID: "abc"}
	tok := "deadbeef-token"

	if got := LookupSessionByToken(tok); got != nil {
		t.Errorf("pre-registration lookup returned %v, want nil", got)
	}

	RegisterSessionToken(tok, sess)
	if got := LookupSessionByToken(tok); got != sess {
		t.Errorf("lookup returned %v, want %v", got, sess)
	}

	UnregisterSessionToken(tok)
	if got := LookupSessionByToken(tok); got != nil {
		t.Errorf("post-deregistration lookup returned %v, want nil", got)
	}

	// Empty token must be a no-op (don't poison the registry with "").
	RegisterSessionToken("", sess)
	if got := LookupSessionByToken(""); got != nil {
		t.Errorf("empty-token lookup returned %v, want nil", got)
	}
}

func TestEffectiveVisionURLAutoDerive(t *testing.T) {
	cases := []struct {
		wsURL    string
		override string
		want     string
	}{
		// Plain ws → http
		{"ws://192.168.1.10:12800/xiaozhi/v1/", "", "http://192.168.1.10:12800/xiaozhi/vision/explain"},
		// Secure wss → https
		{"wss://gateway.example.com/xiaozhi/v1/", "", "https://gateway.example.com/xiaozhi/vision/explain"},
		// Override wins
		{"ws://localhost/xiaozhi/v1/", "https://override.example/explain", "https://override.example/explain"},
		// Unparseable WSURL → empty (caller skips initialize)
		{"://broken", "", ""},
		// Unknown scheme → empty (refuse to guess)
		{"http://wrong-scheme/xiaozhi/v1/", "", ""},
	}
	for _, tc := range cases {
		c := Config{WSURL: tc.wsURL, VisionExplainURL: tc.override}
		if got := effectiveVisionURL(c); got != tc.want {
			t.Errorf("effectiveVisionURL(WSURL=%q, override=%q) = %q, want %q",
				tc.wsURL, tc.override, got, tc.want)
		}
	}
}

func TestGenerateVisionTokenShape(t *testing.T) {
	tok, err := generateVisionToken()
	if err != nil {
		t.Fatalf("generateVisionToken: %v", err)
	}
	if len(tok) != 32 {
		t.Errorf("len = %d, want 32 hex chars", len(tok))
	}
	for _, r := range tok {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			t.Errorf("non-hex char %q in token %q", r, tok)
			break
		}
	}
	// Two consecutive calls must differ (cryptographic randomness).
	tok2, _ := generateVisionToken()
	if tok == tok2 {
		t.Errorf("two consecutive tokens identical: %q", tok)
	}
}
