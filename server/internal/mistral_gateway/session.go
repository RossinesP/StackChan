/*
SPDX-FileCopyrightText: 2026 M5Stack Technology CO LTD
SPDX-License-Identifier: MIT
*/

package mistral_gateway

// Session is the per-WebSocket-connection state. One instance per
// connected device; lives for the duration of WsHandler's call.
//
// M3 scope: just enough to do audio loopback — buffer the user's
// re-encoded opus while listening, then play it back as TTS when the
// device sends `listen state:stop`.
//
// M4+ will extend this with: STT stream handle, LLM message history,
// MCP tools list, TTS synthesizer state.
type Session struct {
	ID          string
	DeviceID    string
	BP2Version  uint16
	Codec       *OpusCodec

	// listening is true between `listen state:start` and `listen state:stop`.
	listening bool

	// echoBuf holds re-encoded opus frames captured while listening,
	// to be replayed as a TTS response.
	echoBuf [][]byte
}

func (s *Session) StartListening() {
	s.listening = true
	s.echoBuf = s.echoBuf[:0]
}

func (s *Session) StopListening() {
	s.listening = false
}

func (s *Session) IsListening() bool { return s.listening }

func (s *Session) BufferEcho(opusPacket []byte) {
	// Copy: hraban/opus encode returns a slice that aliases an internal
	// buffer; without copy, successive frames overwrite earlier ones.
	cp := make([]byte, len(opusPacket))
	copy(cp, opusPacket)
	s.echoBuf = append(s.echoBuf, cp)
}

func (s *Session) DrainEcho() [][]byte {
	out := s.echoBuf
	s.echoBuf = nil
	return out
}
