package network

import (
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

var (
	// ErrSessionClosed is returned by Send after the session is closed.
	ErrSessionClosed = errors.New("network: session closed")
	// ErrSendQueueFull is returned when a slow client cannot keep up; the session is closed.
	ErrSendQueueFull = errors.New("network: send queue full")
)

// DefaultWriteTimeout bounds a single write to a client.
const DefaultWriteTimeout = 10 * time.Second

// Session owns one client connection and a dedicated writer goroutine.
//
// Any goroutine may call Send: payloads are queued and written in order by the
// writer goroutine, so game logic never blocks on a slow socket. Reading is
// left to the owner (the gate), which calls Conn().ReadMessage in its own loop.
type Session struct {
	id     uint64
	conn   Conn
	sendCh chan []byte
	done   chan struct{}
	once   sync.Once
	// draining is set by CloseAfterFlush so that no further payloads are accepted.
	draining atomic.Bool
	// mu serializes Send with CloseAfterFlush so the flush marker is the last queued item.
	mu sync.Mutex
}

// NewSession wraps conn and starts its writer goroutine.
func NewSession(id uint64, conn Conn, queueSize int) *Session {
	if queueSize <= 0 {
		queueSize = 256
	}
	s := &Session{
		id:     id,
		conn:   conn,
		sendCh: make(chan []byte, queueSize),
		done:   make(chan struct{}),
	}
	go s.writeLoop()
	return s
}

// ID returns the session id (unique inside one gate process).
func (s *Session) ID() uint64 { return s.id }

// Conn exposes the underlying connection for the reader loop.
func (s *Session) Conn() Conn { return s.conn }

// Done is closed when the session is closed.
func (s *Session) Done() <-chan struct{} { return s.done }

// Send queues a packet. It never blocks: if the queue is full the client is
// considered too slow and the session is closed.
func (s *Session) Send(p Packet) error {
	return s.SendRaw(p.Encode())
}

// SendRaw queues an already encoded payload.
func (s *Session) SendRaw(payload []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.draining.Load() {
		return ErrSessionClosed
	}
	select {
	case <-s.done:
		return ErrSessionClosed
	default:
	}
	select {
	case s.sendCh <- payload:
		return nil
	default:
		go s.Close() // never close while holding mu from a hot path
		return ErrSendQueueFull
	}
}

// CloseAfterFlush writes every queued payload (plus optional last packets) and then closes.
// It is used to deliver a KickPush before disconnecting.
func (s *Session) CloseAfterFlush(last ...Packet) {
	s.mu.Lock()
	if s.draining.Swap(true) {
		s.mu.Unlock()
		return
	}
	for _, p := range last {
		select {
		case s.sendCh <- p.Encode():
		default:
		}
	}
	select {
	case s.sendCh <- nil: // nil marks "flush then close"
		s.mu.Unlock()
	default:
		s.mu.Unlock()
		s.Close()
	}
}

// Close closes the connection immediately. Safe to call many times.
func (s *Session) Close() {
	s.once.Do(func() {
		close(s.done)
		_ = s.conn.Close()
	})
}

func (s *Session) writeLoop() {
	for {
		select {
		case payload := <-s.sendCh:
			if payload == nil {
				s.Close()
				return
			}
			_ = s.conn.SetWriteDeadline(time.Now().Add(DefaultWriteTimeout))
			if err := s.conn.WriteMessage(payload); err != nil {
				s.Close()
				return
			}
		case <-s.done:
			return
		}
	}
}
