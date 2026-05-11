// Package dtach implements a small dtach-like helper: a long-lived process that
// hosts a PTY-backed command and exposes it on a Unix socket so multiple
// short-lived clients can attach and detach without disturbing the program.
//
// Wire protocol (both directions):
//
//	1 byte  message type
//	4 bytes payload length, big-endian uint32
//	N bytes payload
//
// Client -> server messages:
//
//	MsgInput   payload = raw bytes to write to the PTY
//	MsgResize  payload = 4 bytes: cols (uint16 BE) then rows (uint16 BE)
//
// Server -> client messages:
//
//	MsgSnapshot payload = scrollback bytes replayed at attach time
//	MsgOutput   payload = bytes read from the PTY
//	MsgExit     payload = 4 bytes: exit code (int32 BE)
package dtach

import (
	"encoding/binary"
	"fmt"
	"io"
)

// MsgType identifies a wire message.
type MsgType byte

const (
	MsgInput    MsgType = 0x01
	MsgResize   MsgType = 0x02
	MsgSnapshot MsgType = 0x10
	MsgOutput   MsgType = 0x11
	MsgExit     MsgType = 0x12
)

// MaxPayload caps a single frame payload to guard against runaway allocations.
const MaxPayload = 1 << 20

// WriteFrame writes a single framed message.
func WriteFrame(w io.Writer, t MsgType, payload []byte) error {
	if len(payload) > MaxPayload {
		return fmt.Errorf("dtach: payload too large: %d", len(payload))
	}
	var hdr [5]byte
	hdr[0] = byte(t)
	binary.BigEndian.PutUint32(hdr[1:], uint32(len(payload)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	if len(payload) > 0 {
		if _, err := w.Write(payload); err != nil {
			return err
		}
	}
	return nil
}

// ReadFrame reads a single framed message.
func ReadFrame(r io.Reader) (MsgType, []byte, error) {
	var hdr [5]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(hdr[1:])
	if n > MaxPayload {
		return 0, nil, fmt.Errorf("dtach: payload too large: %d", n)
	}
	buf := make([]byte, n)
	if n > 0 {
		if _, err := io.ReadFull(r, buf); err != nil {
			return 0, nil, err
		}
	}
	return MsgType(hdr[0]), buf, nil
}

// EncodeResize packs a (cols, rows) pair into a MsgResize payload.
func EncodeResize(cols, rows uint16) []byte {
	var b [4]byte
	binary.BigEndian.PutUint16(b[0:2], cols)
	binary.BigEndian.PutUint16(b[2:4], rows)
	return b[:]
}

// DecodeResize unpacks a MsgResize payload.
func DecodeResize(p []byte) (cols, rows uint16, ok bool) {
	if len(p) != 4 {
		return 0, 0, false
	}
	return binary.BigEndian.Uint16(p[0:2]), binary.BigEndian.Uint16(p[2:4]), true
}

// EncodeExit packs an exit code into a MsgExit payload.
func EncodeExit(code int32) []byte {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], uint32(code))
	return b[:]
}

// DecodeExit unpacks a MsgExit payload.
func DecodeExit(p []byte) (int32, bool) {
	if len(p) != 4 {
		return 0, false
	}
	return int32(binary.BigEndian.Uint32(p)), true
}
