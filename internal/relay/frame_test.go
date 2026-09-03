package relay

import (
	"bytes"
	"testing"
)

func TestFrameRoundTrip(t *testing.T) {
	types := []FrameType{FrameHello, FrameHelloAck, FramePing, FramePong, FrameBye, FrameData, FrameError}
	for _, typ := range types {
		for _, payload := range [][]byte{nil, bytes.Repeat([]byte{0xa5}, MaxPayloadSize)} {
			in := Frame{Type: typ, KeyID: 0x1234, Payload: payload}
			b, err := EncodeFrame(in)
			if err != nil {
				t.Fatal(err)
			}
			out, err := DecodeFrame(b)
			if err != nil {
				t.Fatal(err)
			}
			if out.Type != in.Type || out.KeyID != in.KeyID || !bytes.Equal(out.Payload, in.Payload) {
				t.Fatalf("round trip = %#v", out)
			}
		}
	}
}

func TestFrameDecodeRejectsInvalid(t *testing.T) {
	b, _ := EncodeFrame(Frame{Type: FrameHello})
	for n := 0; n < len(b); n++ {
		if _, err := DecodeFrame(b[:n]); err == nil {
			t.Fatalf("truncation %d accepted", n)
		}
	}
	badMagic := append([]byte(nil), b...)
	badMagic[0] = 0
	if _, err := DecodeFrame(badMagic); err == nil {
		t.Fatal("bad magic accepted")
	}
	badVersion := append([]byte(nil), b...)
	badVersion[4]++
	if _, err := DecodeFrame(badVersion); err == nil {
		t.Fatal("bad version accepted")
	}
}
