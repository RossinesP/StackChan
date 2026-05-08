/*
SPDX-FileCopyrightText: 2026 M5Stack Technology CO LTD
SPDX-License-Identifier: MIT
*/

package mistral_gateway

import (
	"bytes"
	"encoding/binary"
)

// EncodeWAVMono16 builds a minimal RIFF/WAVE container around mono
// 16-bit PCM samples. Used to ship captured audio to Voxtral STT,
// which accepts WAV uploads.
//
// Spec: http://soundfile.sapp.org/doc/WaveFormat/
func EncodeWAVMono16(samples []int16, sampleRate int) []byte {
	const (
		channels      uint16 = 1
		bitsPerSample uint16 = 16
		audioFormat   uint16 = 1 // PCM
	)
	dataBytes := uint32(len(samples) * 2)
	byteRate := uint32(sampleRate) * uint32(channels) * uint32(bitsPerSample/8)
	blockAlign := channels * (bitsPerSample / 8)

	var buf bytes.Buffer
	buf.Grow(44 + int(dataBytes))

	buf.WriteString("RIFF")
	binary.Write(&buf, binary.LittleEndian, uint32(36+dataBytes))
	buf.WriteString("WAVE")

	buf.WriteString("fmt ")
	binary.Write(&buf, binary.LittleEndian, uint32(16))
	binary.Write(&buf, binary.LittleEndian, audioFormat)
	binary.Write(&buf, binary.LittleEndian, channels)
	binary.Write(&buf, binary.LittleEndian, uint32(sampleRate))
	binary.Write(&buf, binary.LittleEndian, byteRate)
	binary.Write(&buf, binary.LittleEndian, blockAlign)
	binary.Write(&buf, binary.LittleEndian, bitsPerSample)

	buf.WriteString("data")
	binary.Write(&buf, binary.LittleEndian, dataBytes)
	binary.Write(&buf, binary.LittleEndian, samples)

	return buf.Bytes()
}

// Resample16 converts mono int16 PCM from srcRate to dstRate using
// linear interpolation. Good enough for voice; for music-grade audio
// you'd want a polyphase filter, but Voxtral's output is voice anyway.
//
// If srcRate == dstRate, returns a copy of the input.
func Resample16(samples []int16, srcRate, dstRate int) []int16 {
	if srcRate == dstRate {
		out := make([]int16, len(samples))
		copy(out, samples)
		return out
	}
	if len(samples) == 0 {
		return nil
	}

	ratio := float64(srcRate) / float64(dstRate)
	dstLen := int(float64(len(samples)) / ratio)
	out := make([]int16, dstLen)

	for i := range out {
		srcF := float64(i) * ratio
		idx := int(srcF)
		frac := srcF - float64(idx)
		if idx+1 >= len(samples) {
			out[i] = samples[len(samples)-1]
			continue
		}
		a := float64(samples[idx])
		b := float64(samples[idx+1])
		out[i] = int16(a + frac*(b-a))
	}
	return out
}

// PeakNormalize scales `samples` so the loudest sample reaches
// `targetPeak` (signed int16 magnitude, max 32767). Smaller values
// leave more headroom and avoid distortion on transients.
//
// Returns the input unchanged if peak is already at or above target,
// or if the buffer is silent.
//
// Recommended targetPeak:
//   24000  → ~-2.7 dBFS, conservative, headroom for emotion peaks
//   29000  → ~-1.0 dBFS, loud, may clip on percussive sounds
//   32000  → near-max, last resort
func PeakNormalize(samples []int16, targetPeak int) []int16 {
	if len(samples) == 0 || targetPeak <= 0 {
		return samples
	}
	var peak int32
	for _, s := range samples {
		v := int32(s)
		if v < 0 {
			v = -v
		}
		if v > peak {
			peak = v
		}
	}
	if peak == 0 || peak >= int32(targetPeak) {
		return samples
	}
	gain := float64(targetPeak) / float64(peak)
	out := make([]int16, len(samples))
	for i, s := range samples {
		scaled := float64(s) * gain
		if scaled > 32767 {
			scaled = 32767
		} else if scaled < -32768 {
			scaled = -32768
		}
		out[i] = int16(scaled)
	}
	return out
}

// SplitFrames chunks a PCM stream into fixed-size frames suitable for
// opus encoding. The last frame is zero-padded if the input doesn't
// divide evenly — opus tolerates trailing silence.
func SplitFrames(samples []int16, frameSamples int) [][]int16 {
	if frameSamples <= 0 || len(samples) == 0 {
		return nil
	}
	n := (len(samples) + frameSamples - 1) / frameSamples
	frames := make([][]int16, n)
	for i := 0; i < n; i++ {
		start := i * frameSamples
		end := start + frameSamples
		if end > len(samples) {
			// Pad with silence to reach frameSamples.
			padded := make([]int16, frameSamples)
			copy(padded, samples[start:])
			frames[i] = padded
		} else {
			frames[i] = samples[start:end]
		}
	}
	return frames
}
