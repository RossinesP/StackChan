/*
SPDX-FileCopyrightText: 2026 M5Stack Technology CO LTD
SPDX-License-Identifier: MIT
*/

package mistral_gateway

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestResample16Identity(t *testing.T) {
	in := []int16{1, 2, 3, 4, 5}
	out := Resample16(in, 16000, 16000)
	if len(out) != len(in) {
		t.Fatalf("len = %d, want %d", len(out), len(in))
	}
	for i := range in {
		if in[i] != out[i] {
			t.Errorf("[%d] %d != %d", i, in[i], out[i])
		}
	}
}

func TestResample16Downsample(t *testing.T) {
	// 24000 → 16000: ratio 1.5. 12 samples in → 8 samples out.
	in := make([]int16, 12)
	for i := range in {
		in[i] = int16(i * 100)
	}
	out := Resample16(in, 24000, 16000)
	if len(out) != 8 {
		t.Fatalf("len = %d, want 8", len(out))
	}
	// First and last should be close to the original endpoints.
	if out[0] != 0 {
		t.Errorf("out[0] = %d, want 0", out[0])
	}
}

func TestSplitFramesEvenSplit(t *testing.T) {
	in := make([]int16, 1920) // 2 frames of 960
	frames := SplitFrames(in, 960)
	if len(frames) != 2 {
		t.Fatalf("frames = %d, want 2", len(frames))
	}
	for i, f := range frames {
		if len(f) != 960 {
			t.Errorf("frame[%d] len = %d, want 960", i, len(f))
		}
	}
}

func TestSplitFramesPaddingTail(t *testing.T) {
	in := make([]int16, 1000) // 1 full frame + 40 leftover
	for i := range in {
		in[i] = 1
	}
	frames := SplitFrames(in, 960)
	if len(frames) != 2 {
		t.Fatalf("frames = %d, want 2", len(frames))
	}
	// Last frame: first 40 samples = 1, rest = 0 (padding).
	last := frames[1]
	if len(last) != 960 {
		t.Fatalf("last frame len = %d, want 960", len(last))
	}
	for i := 0; i < 40; i++ {
		if last[i] != 1 {
			t.Errorf("last[%d] = %d, want 1", i, last[i])
		}
	}
	for i := 40; i < 960; i++ {
		if last[i] != 0 {
			t.Errorf("last[%d] = %d, want 0 (pad)", i, last[i])
		}
	}
}

// TestDecodeWAV builds a minimal valid WAV file (mono, 16-bit, 24 kHz)
// in-memory and round-trips it through decodeWAV.
func TestDecodeWAV(t *testing.T) {
	samples := make([]int16, 100)
	for i := range samples {
		samples[i] = int16(i * 200)
	}
	wav := buildWAV(t, samples, 1, 24000)

	pcm, rate, err := decodeWAV(wav)
	if err != nil {
		t.Fatalf("decodeWAV: %v", err)
	}
	if rate != 24000 {
		t.Errorf("rate = %d, want 24000", rate)
	}
	if len(pcm) != len(samples) {
		t.Fatalf("samples = %d, want %d", len(pcm), len(samples))
	}
	for i := range samples {
		if pcm[i] != samples[i] {
			t.Errorf("[%d] %d != %d", i, pcm[i], samples[i])
		}
	}
}

func TestEncodeDecodeWAVRoundTrip(t *testing.T) {
	samples := make([]int16, 200)
	for i := range samples {
		samples[i] = int16((i*173)%30000 - 15000) // pseudo-random shape
	}
	wav := EncodeWAVMono16(samples, 16000)

	pcm, rate, err := decodeWAV(wav)
	if err != nil {
		t.Fatalf("decodeWAV: %v", err)
	}
	if rate != 16000 {
		t.Errorf("rate = %d, want 16000", rate)
	}
	if len(pcm) != len(samples) {
		t.Fatalf("len(pcm) = %d, want %d", len(pcm), len(samples))
	}
	for i := range samples {
		if pcm[i] != samples[i] {
			t.Errorf("[%d] %d != %d", i, pcm[i], samples[i])
			break
		}
	}
}

func TestPeakNormalizeBoosts(t *testing.T) {
	in := []int16{1000, -1500, 800, -2000} // peak = 2000
	out := PeakNormalize(in, 28000)
	// Scale factor = 28000/2000 = 14
	want := []int16{14000, -21000, 11200, -28000}
	for i := range want {
		if out[i] != want[i] {
			t.Errorf("[%d] got %d, want %d", i, out[i], want[i])
		}
	}
}

func TestPeakNormalizeNoOp(t *testing.T) {
	// Already louder than target → no change.
	in := []int16{30000, -30000}
	out := PeakNormalize(in, 28000)
	for i := range in {
		if out[i] != in[i] {
			t.Errorf("[%d] got %d, want %d", i, out[i], in[i])
		}
	}
}

func TestPeakNormalizeSilence(t *testing.T) {
	in := []int16{0, 0, 0, 0}
	out := PeakNormalize(in, 28000)
	for i, s := range out {
		if s != 0 {
			t.Errorf("[%d] silence boosted to %d", i, s)
		}
	}
}

func TestDecodeWAVStereoDownmix(t *testing.T) {
	// Stereo: L=100, R=300 → mono should be 200
	pairs := []int16{100, 300, 100, 300, 100, 300}
	wav := buildWAV(t, pairs, 2, 24000)
	pcm, rate, err := decodeWAV(wav)
	if err != nil {
		t.Fatalf("decodeWAV: %v", err)
	}
	if rate != 24000 {
		t.Errorf("rate = %d, want 24000", rate)
	}
	if len(pcm) != 3 {
		t.Fatalf("mono samples = %d, want 3", len(pcm))
	}
	for i, s := range pcm {
		if s != 200 {
			t.Errorf("pcm[%d] = %d, want 200 (stereo downmix)", i, s)
		}
	}
}

// buildWAV emits a minimal RIFF/WAVE container.
func buildWAV(t *testing.T, samples []int16, channels uint16, rate uint32) []byte {
	t.Helper()
	var buf bytes.Buffer
	dataBytes := uint32(len(samples) * 2)
	byteRate := rate * uint32(channels) * 2
	blockAlign := channels * 2

	buf.WriteString("RIFF")
	binary.Write(&buf, binary.LittleEndian, uint32(36+dataBytes)) // RIFF size
	buf.WriteString("WAVE")

	buf.WriteString("fmt ")
	binary.Write(&buf, binary.LittleEndian, uint32(16))      // fmt chunk size
	binary.Write(&buf, binary.LittleEndian, uint16(1))       // PCM
	binary.Write(&buf, binary.LittleEndian, channels)
	binary.Write(&buf, binary.LittleEndian, rate)
	binary.Write(&buf, binary.LittleEndian, byteRate)
	binary.Write(&buf, binary.LittleEndian, blockAlign)
	binary.Write(&buf, binary.LittleEndian, uint16(16))      // bits per sample

	buf.WriteString("data")
	binary.Write(&buf, binary.LittleEndian, dataBytes)
	binary.Write(&buf, binary.LittleEndian, samples)

	return buf.Bytes()
}
