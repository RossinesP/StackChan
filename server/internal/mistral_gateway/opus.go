/*
SPDX-FileCopyrightText: 2026 M5Stack Technology CO LTD
SPDX-License-Identifier: MIT
*/

package mistral_gateway

import (
	"fmt"

	"github.com/hraban/opus"
)

// OpusCodec is a paired encoder + decoder for one audio session.
//
// Defaults match the firmware's audio_service.cc:
//   - 16 kHz mono
//   - 60 ms frames (== 960 samples per frame at 16 kHz)
//   - VBR, no FEC, no DTX (we re-encode here, the device's settings on
//     the inbound side don't constrain us)
//
// Sample rates supported by libopus: 8000, 12000, 16000, 24000, 48000.
// Frame durations: 2.5, 5, 10, 20, 40, 60 ms.
type OpusCodec struct {
	sampleRate    int
	channels      int
	frameSamples  int    // samples per frame, == sampleRate * frameMS / 1000
	pcmBuf        []int16
	enc           *opus.Encoder
	dec           *opus.Decoder
}

// NewOpusCodec constructs an encoder/decoder pair tied to the device's
// negotiated audio params.
func NewOpusCodec(sampleRate, frameDurationMS, channels int) (*OpusCodec, error) {
	if channels < 1 || channels > 2 {
		return nil, fmt.Errorf("opus: channels must be 1 or 2, got %d", channels)
	}
	enc, err := opus.NewEncoder(sampleRate, channels, opus.AppVoIP)
	if err != nil {
		return nil, fmt.Errorf("opus: encoder: %w", err)
	}
	dec, err := opus.NewDecoder(sampleRate, channels)
	if err != nil {
		return nil, fmt.Errorf("opus: decoder: %w", err)
	}
	frameSamples := sampleRate * frameDurationMS / 1000
	return &OpusCodec{
		sampleRate:   sampleRate,
		channels:     channels,
		frameSamples: frameSamples,
		pcmBuf:       make([]int16, frameSamples*channels),
		enc:          enc,
		dec:          dec,
	}, nil
}

// Decode turns one inbound opus packet into PCM int16 samples.
// Returns the populated slice (aliasing an internal buffer — copy if
// you need to retain it across calls).
func (c *OpusCodec) Decode(opusPacket []byte) ([]int16, error) {
	n, err := c.dec.Decode(opusPacket, c.pcmBuf)
	if err != nil {
		return nil, fmt.Errorf("opus decode: %w", err)
	}
	return c.pcmBuf[:n*c.channels], nil
}

// Encode turns one frame of PCM int16 into an opus packet.
// pcm length must equal frameSamples * channels.
func (c *OpusCodec) Encode(pcm []int16) ([]byte, error) {
	if len(pcm) != c.frameSamples*c.channels {
		return nil, fmt.Errorf("opus encode: pcm len=%d, want %d (frameSamples=%d * channels=%d)",
			len(pcm), c.frameSamples*c.channels, c.frameSamples, c.channels)
	}
	// Max bytes per opus frame is bounded; 4000 is safe headroom.
	out := make([]byte, 4000)
	n, err := c.enc.Encode(pcm, out)
	if err != nil {
		return nil, fmt.Errorf("opus encode: %w", err)
	}
	return out[:n], nil
}
