package relay

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

const (
	ProtocolVersion byte = 1
	frameHeaderSize      = 12
	HMACSize             = 32
	MaxPayloadSize       = 1 << 20
)

var frameMagic = [4]byte{'C', 'A', 'N', 'Y'}

type FrameType byte

const (
	FrameHello FrameType = iota + 1
	FrameHelloAck
	FramePing
	FramePong
	FrameBye
	FrameData
	FrameError
)

type Frame struct {
	Type    FrameType
	KeyID   uint16
	Payload []byte // CBOR for control/data frames; the framing layer treats it as opaque.
	HMAC    []byte
}

// Header layout is magic[0:4], version[4], key ID[5:7], type[7], length[8:12].
// T1.8's older table puts a one-byte opcode at 5 and has no key ID. FTR-05's
// later key-rotation contract and canonical bytes 5-6 assignment require the
// uint16 key ID, so the frame type follows it at byte 7.
func EncodeFrame(f Frame) ([]byte, error) {
	if len(f.Payload) > MaxPayloadSize {
		return nil, fmt.Errorf("relay: payload too large: %d", len(f.Payload))
	}
	if !validFrameType(f.Type) {
		return nil, fmt.Errorf("relay: invalid frame type %d", f.Type)
	}
	b := make([]byte, frameHeaderSize+len(f.Payload)+len(f.HMAC))
	copy(b[:4], frameMagic[:])
	b[4] = ProtocolVersion
	binary.BigEndian.PutUint16(b[5:7], f.KeyID)
	b[7] = byte(f.Type)
	binary.BigEndian.PutUint32(b[8:12], uint32(len(f.Payload)))
	copy(b[frameHeaderSize:], f.Payload)
	copy(b[frameHeaderSize+len(f.Payload):], f.HMAC)
	return b, nil
}

func DecodeFrame(b []byte) (Frame, error) {
	if len(b) < frameHeaderSize {
		return Frame{}, io.ErrUnexpectedEOF
	}
	if string(b[:4]) != string(frameMagic[:]) {
		return Frame{}, errors.New("relay: bad frame magic")
	}
	if b[4] != ProtocolVersion {
		return Frame{}, fmt.Errorf("relay: unsupported protocol version %d", b[4])
	}
	t := FrameType(b[7])
	if !validFrameType(t) {
		return Frame{}, fmt.Errorf("relay: invalid frame type %d", t)
	}
	n := int(binary.BigEndian.Uint32(b[8:12]))
	if n > MaxPayloadSize {
		return Frame{}, fmt.Errorf("relay: payload too large: %d", n)
	}
	if len(b) != frameHeaderSize+n && len(b) != frameHeaderSize+n+HMACSize {
		return Frame{}, io.ErrUnexpectedEOF
	}
	f := Frame{Type: t, KeyID: binary.BigEndian.Uint16(b[5:7]), Payload: append([]byte(nil), b[frameHeaderSize:frameHeaderSize+n]...)}
	if len(b) == frameHeaderSize+n+HMACSize {
		f.HMAC = append([]byte(nil), b[frameHeaderSize+n:]...)
	}
	return f, nil
}

func ReadFrame(r io.Reader) (Frame, error) {
	header := make([]byte, frameHeaderSize)
	if _, err := io.ReadFull(r, header); err != nil {
		return Frame{}, err
	}
	n := int(binary.BigEndian.Uint32(header[8:12]))
	if n > MaxPayloadSize {
		return Frame{}, fmt.Errorf("relay: payload too large: %d", n)
	}
	b := append(header, make([]byte, n+HMACSize)...)
	if _, err := io.ReadFull(r, b[frameHeaderSize:]); err != nil {
		return Frame{}, err
	}
	return DecodeFrame(b)
}

func validFrameType(t FrameType) bool { return t >= FrameHello && t <= FrameError }
