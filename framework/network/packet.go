// Package network implements the client-facing transport layer.
//
// Wire format (all integers big-endian):
//
//	TCP frame:  | length uint32 | payload (length bytes) |
//	WebSocket:  one binary WebSocket message == one payload
//
//	payload:    | msg_id uint32 | seq uint32 | body (protobuf) |
//
// msg_id identifies the protobuf type of body. seq is chosen by the client for
// requests and echoed in the matching response; server pushes use seq 0.
package network

import (
	"encoding/binary"
	"errors"
)

// HeaderSize is the size of the payload header (msg_id + seq).
const HeaderSize = 8

// DefaultMaxPacketSize is used when no limit is configured (64 KiB).
const DefaultMaxPacketSize = 64 * 1024

// ErrShortPacket is returned when a payload is smaller than HeaderSize.
var ErrShortPacket = errors.New("network: packet shorter than header")

// Packet is one application message exchanged with a client.
type Packet struct {
	MsgID uint32
	Seq   uint32
	Body  []byte
}

// Encode serializes the packet into a payload (without the TCP length prefix).
func (p Packet) Encode() []byte {
	buf := make([]byte, HeaderSize+len(p.Body))
	binary.BigEndian.PutUint32(buf[0:4], p.MsgID)
	binary.BigEndian.PutUint32(buf[4:8], p.Seq)
	copy(buf[HeaderSize:], p.Body)
	return buf
}

// DecodePacket parses a payload. The returned Body aliases b.
func DecodePacket(b []byte) (Packet, error) {
	if len(b) < HeaderSize {
		return Packet{}, ErrShortPacket
	}
	return Packet{
		MsgID: binary.BigEndian.Uint32(b[0:4]),
		Seq:   binary.BigEndian.Uint32(b[4:8]),
		Body:  b[HeaderSize:],
	}, nil
}
