package network

import (
	"bytes"
	"errors"
	"net"
	"testing"
	"time"
)

func TestPacketRoundTrip(t *testing.T) {
	p := Packet{MsgID: 1001, Seq: 7, Body: []byte("hello")}
	got, err := DecodePacket(p.Encode())
	if err != nil {
		t.Fatal(err)
	}
	if got.MsgID != p.MsgID || got.Seq != p.Seq || !bytes.Equal(got.Body, p.Body) {
		t.Fatalf("got %+v want %+v", got, p)
	}
	if _, err := DecodePacket([]byte{1, 2, 3}); !errors.Is(err, ErrShortPacket) {
		t.Fatalf("expected ErrShortPacket, got %v", err)
	}
}

func TestTCPFraming(t *testing.T) {
	a, b := net.Pipe()
	ca, cb := NewTCPConn(a, 1024), NewTCPConn(b, 1024)
	defer ca.Close()
	defer cb.Close()

	go func() {
		_ = ca.WriteMessage([]byte("one"))
		_ = ca.WriteMessage([]byte("two"))
	}()
	for _, want := range []string{"one", "two"} {
		got, err := cb.ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want {
			t.Fatalf("got %q want %q", got, want)
		}
	}
}

func TestTCPRejectsOversizedPacket(t *testing.T) {
	a, b := net.Pipe()
	small := NewTCPConn(b, 4)
	defer a.Close()
	defer small.Close()
	go func() { _ = NewTCPConn(a, 1024).WriteMessage([]byte("too large")) }()
	if _, err := small.ReadMessage(); !errors.Is(err, ErrPacketTooLarge) {
		t.Fatalf("expected ErrPacketTooLarge, got %v", err)
	}
}

func TestSessionCloseAfterFlush(t *testing.T) {
	a, b := net.Pipe()
	peer := NewTCPConn(b, 1024)
	s := NewSession(1, NewTCPConn(a, 1024), 8)

	if err := s.Send(Packet{MsgID: 1}); err != nil {
		t.Fatal(err)
	}
	s.CloseAfterFlush(Packet{MsgID: 4})

	for _, want := range []uint32{1, 4} {
		data, err := peer.ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		p, _ := DecodePacket(data)
		if p.MsgID != want {
			t.Fatalf("got msg %d want %d", p.MsgID, want)
		}
	}
	select {
	case <-s.Done():
	case <-time.After(time.Second):
		t.Fatal("session not closed after flush")
	}
	if err := s.Send(Packet{MsgID: 9}); !errors.Is(err, ErrSessionClosed) {
		t.Fatalf("expected ErrSessionClosed, got %v", err)
	}
}
