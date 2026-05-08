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
	// to be replayed as a TTS response (M3 fallback path).
	echoBuf [][]byte

	// pcmFrames holds the decoded PCM (16-bit, sess.Codec.sampleRate Hz,
	// mono) captured while listening. Concatenated and shipped to
	// Voxtral STT in M5. Kept alongside echoBuf because re-encoding to
	// opus is lossy — STT works better on the original PCM.
	pcmFrames [][]int16

	// History is the per-session conversation log used by the M7 chat
	// reply path. Truncated on each turn via truncateHistory.
	History []ChatMessage
}

// AppendTurn records one user/assistant exchange in the session
// history, then truncates to the configured limit. Called after a
// successful chat completion so the next turn has context.
func (s *Session) AppendTurn(userText, assistantText string) {
	s.History = append(s.History,
		ChatMessage{Role: RoleUser, Content: userText},
		ChatMessage{Role: RoleAssistant, Content: assistantText},
	)
	s.History = truncateHistory(s.History, Get().ChatHistoryLimit)
}

func (s *Session) StartListening() {
	s.listening = true
	s.echoBuf = s.echoBuf[:0]
	s.pcmFrames = s.pcmFrames[:0]
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

// BufferPCM stores a copy of one decoded PCM frame. Copy is mandatory
// because hraban/opus' Decode aliases an internal buffer.
func (s *Session) BufferPCM(pcm []int16) {
	cp := make([]int16, len(pcm))
	copy(cp, pcm)
	s.pcmFrames = append(s.pcmFrames, cp)
}

// DrainPCM returns all buffered PCM frames concatenated into a single
// slice, then resets the buffer.
func (s *Session) DrainPCM() []int16 {
	if len(s.pcmFrames) == 0 {
		return nil
	}
	total := 0
	for _, f := range s.pcmFrames {
		total += len(f)
	}
	out := make([]int16, 0, total)
	for _, f := range s.pcmFrames {
		out = append(out, f...)
	}
	s.pcmFrames = nil
	return out
}
