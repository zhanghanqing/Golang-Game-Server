package network

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/gorilla/websocket"
)

// ErrPacketTooLarge is returned when a peer announces a payload above the configured limit.
var ErrPacketTooLarge = errors.New("network: packet too large")

// Conn is a message-oriented connection. TCP and WebSocket both implement it,
// so everything above this layer is transport agnostic.
//
// ReadMessage must be called from a single goroutine, and WriteMessage from a
// single (possibly different) goroutine.
type Conn interface {
	ReadMessage() ([]byte, error)
	WriteMessage(payload []byte) error
	SetReadDeadline(t time.Time) error
	SetWriteDeadline(t time.Time) error
	RemoteAddr() net.Addr
	Close() error
}

// ---------------------------------------------------------------------------
// TCP

type tcpConn struct {
	conn    net.Conn
	reader  *bufio.Reader
	maxSize int
	header  [4]byte
}

// NewTCPConn wraps a net.Conn with length-prefixed framing.
func NewTCPConn(c net.Conn, maxSize int) Conn {
	if maxSize <= 0 {
		maxSize = DefaultMaxPacketSize
	}
	if tc, ok := c.(*net.TCPConn); ok {
		_ = tc.SetNoDelay(true) // games are latency sensitive
	}
	return &tcpConn{conn: c, reader: bufio.NewReaderSize(c, 8*1024), maxSize: maxSize}
}

func (c *tcpConn) ReadMessage() ([]byte, error) {
	if _, err := io.ReadFull(c.reader, c.header[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(c.header[:])
	if int64(n) > int64(c.maxSize) {
		return nil, fmt.Errorf("%w: %d > %d", ErrPacketTooLarge, n, c.maxSize)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(c.reader, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

func (c *tcpConn) WriteMessage(payload []byte) error {
	if len(payload) > c.maxSize {
		return fmt.Errorf("%w: %d > %d", ErrPacketTooLarge, len(payload), c.maxSize)
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(payload)))
	bufs := net.Buffers{header[:], payload} // single writev syscall where supported
	_, err := bufs.WriteTo(c.conn)
	return err
}

func (c *tcpConn) SetReadDeadline(t time.Time) error  { return c.conn.SetReadDeadline(t) }
func (c *tcpConn) SetWriteDeadline(t time.Time) error { return c.conn.SetWriteDeadline(t) }
func (c *tcpConn) RemoteAddr() net.Addr               { return c.conn.RemoteAddr() }
func (c *tcpConn) Close() error                       { return c.conn.Close() }

// ---------------------------------------------------------------------------
// WebSocket

type wsConn struct {
	conn *websocket.Conn
}

// NewWSConn wraps a gorilla websocket connection. Each binary message is one payload.
func NewWSConn(c *websocket.Conn, maxSize int) Conn {
	if maxSize <= 0 {
		maxSize = DefaultMaxPacketSize
	}
	c.SetReadLimit(int64(maxSize))
	return &wsConn{conn: c}
}

func (c *wsConn) ReadMessage() ([]byte, error) {
	for {
		mt, data, err := c.conn.ReadMessage()
		if err != nil {
			return nil, err
		}
		if mt == websocket.BinaryMessage {
			return data, nil
		}
		// Text messages are not part of the protocol; ignore them.
	}
}

func (c *wsConn) WriteMessage(payload []byte) error {
	return c.conn.WriteMessage(websocket.BinaryMessage, payload)
}

func (c *wsConn) SetReadDeadline(t time.Time) error  { return c.conn.SetReadDeadline(t) }
func (c *wsConn) SetWriteDeadline(t time.Time) error { return c.conn.SetWriteDeadline(t) }
func (c *wsConn) RemoteAddr() net.Addr               { return c.conn.RemoteAddr() }
func (c *wsConn) Close() error                       { return c.conn.Close() }

// ---------------------------------------------------------------------------
// Client-side dialers (used by the robot client and tests)

// DialTCP opens a framed TCP connection.
func DialTCP(addr string, maxSize int, timeout time.Duration) (Conn, error) {
	c, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return nil, err
	}
	return NewTCPConn(c, maxSize), nil
}

// DialWS opens a WebSocket connection, e.g. url = "ws://127.0.0.1:7001/ws".
func DialWS(url string, maxSize int, timeout time.Duration) (Conn, error) {
	d := websocket.Dialer{HandshakeTimeout: timeout}
	c, _, err := d.Dial(url, nil)
	if err != nil {
		return nil, err
	}
	return NewWSConn(c, maxSize), nil
}
