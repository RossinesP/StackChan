/*
SPDX-FileCopyrightText: 2026 M5Stack Technology CO LTD
SPDX-License-Identifier: MIT
*/

package mistral_gateway

import (
	"bytes"
	"testing"
)

func TestBP2RoundTrip(t *testing.T) {
	payload := []byte("hello world opus payload")
	encoded := EncodeBP2(2, BP2TypeOpus, 12345, payload)

	if len(encoded) != bp2HeaderSize+len(payload) {
		t.Fatalf("encoded length = %d, want %d", len(encoded), bp2HeaderSize+len(payload))
	}

	frame, err := DecodeBP2(encoded)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if frame.Version != 2 {
		t.Errorf("version = %d, want 2", frame.Version)
	}
	if frame.Type != BP2TypeOpus {
		t.Errorf("type = %d, want %d", frame.Type, BP2TypeOpus)
	}
	if frame.TimestampMS != 12345 {
		t.Errorf("ts = %d, want 12345", frame.TimestampMS)
	}
	if !bytes.Equal(frame.Payload, payload) {
		t.Errorf("payload mismatch: got %q want %q", frame.Payload, payload)
	}
}

func TestBP2DecodeShortFrame(t *testing.T) {
	if _, err := DecodeBP2([]byte{0, 0}); err == nil {
		t.Error("expected error on short frame")
	}
}

func TestBP2DecodeOversizedPayload(t *testing.T) {
	// Header claims 1000 bytes payload but only gives 4
	buf := make([]byte, bp2HeaderSize+4)
	// payload_size at [12:16]
	buf[12], buf[13], buf[14], buf[15] = 0, 0, 3, 232 // 1000
	if _, err := DecodeBP2(buf); err == nil {
		t.Error("expected error on oversized payload claim")
	}
}

func TestOpusCodecRoundTrip(t *testing.T) {
	codec, err := NewOpusCodec(16000, 60, 1)
	if err != nil {
		t.Fatalf("NewOpusCodec: %v", err)
	}
	// Synthetic PCM: a soft sine-ish ramp (silence works too — opus
	// handles it). 960 samples = 60ms @ 16kHz mono.
	pcm := make([]int16, 960)
	for i := range pcm {
		pcm[i] = int16((i % 100) * 100)
	}

	encoded, err := codec.Encode(pcm)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if len(encoded) == 0 {
		t.Fatal("encoded packet is empty")
	}
	t.Logf("opus packet: %d bytes for 960 samples (%.1f kbps)",
		len(encoded), float64(len(encoded)*8)/60.0)

	decoded, err := codec.Decode(encoded)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(decoded) != 960 {
		t.Errorf("decoded samples = %d, want 960", len(decoded))
	}
}
