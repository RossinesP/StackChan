/*
SPDX-FileCopyrightText: 2026 M5Stack Technology CO LTD
SPDX-License-Identifier: MIT
*/

package mistral_gateway

import (
	"encoding/binary"
	"fmt"
)

// BinaryProtocol2 wire layout (matches xiaozhi-esp32 protocols/protocol.h):
//
//	uint16  version       (big-endian, == 2)
//	uint16  type          (0 = OPUS, 1 = JSON)
//	uint32  reserved
//	uint32  timestamp_ms
//	uint32  payload_size
//	byte[]  payload
//
// Header is 16 bytes, all multi-byte fields big-endian.
const (
	bp2HeaderSize = 16

	BP2TypeOpus uint16 = 0
	BP2TypeJSON uint16 = 1
)

// BP2Frame is the decoded form of a BinaryProtocol2 packet.
type BP2Frame struct {
	Version     uint16
	Type        uint16
	TimestampMS uint32
	Payload     []byte
}

// DecodeBP2 parses one BinaryProtocol2 frame from the wire.
// Returns an error if the buffer is too short or self-inconsistent.
func DecodeBP2(data []byte) (*BP2Frame, error) {
	if len(data) < bp2HeaderSize {
		return nil, fmt.Errorf("bp2: short frame: %d < %d", len(data), bp2HeaderSize)
	}
	size := binary.BigEndian.Uint32(data[12:16])
	if int(size)+bp2HeaderSize > len(data) {
		return nil, fmt.Errorf("bp2: payload_size=%d exceeds buffer=%d",
			size, len(data)-bp2HeaderSize)
	}
	return &BP2Frame{
		Version:     binary.BigEndian.Uint16(data[0:2]),
		Type:        binary.BigEndian.Uint16(data[2:4]),
		TimestampMS: binary.BigEndian.Uint32(data[8:12]),
		Payload:     data[bp2HeaderSize : bp2HeaderSize+int(size)],
	}, nil
}

// EncodeBP2 builds a BinaryProtocol2 frame ready to send over the WS.
func EncodeBP2(version uint16, msgType uint16, timestampMS uint32, payload []byte) []byte {
	buf := make([]byte, bp2HeaderSize+len(payload))
	binary.BigEndian.PutUint16(buf[0:2], version)
	binary.BigEndian.PutUint16(buf[2:4], msgType)
	// reserved [4:8] = 0
	binary.BigEndian.PutUint32(buf[8:12], timestampMS)
	binary.BigEndian.PutUint32(buf[12:16], uint32(len(payload)))
	copy(buf[bp2HeaderSize:], payload)
	return buf
}
