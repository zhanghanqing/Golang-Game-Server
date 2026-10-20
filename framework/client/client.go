// Package client is a Go client for servers built on this framework.
//
// It is used by the robot (load/regression tester) and by end-to-end tests,
// and documents the client side of the protocol for engine programmers:
// request/response correlation by seq, server pushes, heartbeats and kicks.
package client

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/protobuf/proto"

	"gameserver/framework/message"
	"gameserver/framework/network"
	"gameserver/framework/pb/syspb"
)

// ErrClosed is returned when the connection is closed.
var ErrClosed = errors.New("client: connection closed")

// ServerError is returned by Request when the server answers with an ErrorResp.
type ServerError struct {
	Code int32
	Msg  string
}

func (e *ServerError) Error() string { return fmt.Sprintf("server error %d: %s", e.Code, e.Msg) }

// PushHandler receives server pushes (seq == 0). It runs on the read goroutine:
// keep it fast or hand work off.
type PushHandler func(msgID uint32, msg proto.Message)

// Options configure a client.
type Options struct {
	// Registry decodes messages. Default: message.Default.
	Registry *message.Registry
	// OnPush receives server pushes, including KickPush.
	OnPush PushHandler
	// DialTimeout bounds connection establishment. Default 5s.
	DialTimeout time.Duration
	// MaxPacketSize must match the server. Default 64 KiB.
	MaxPacketSize int
}

// Client is safe for concurrent use.
type Client struct {
	conn network.Conn
	opts Options

	seq     atomic.Uint32
	writeMu sync.Mutex

	mu      sync.Mutex
	pending map[uint32]chan proto.Message

	closed    chan struct{}
	closeOnce sync.Once
	closeErr  error
}

// DialTCP connects to a gate's TCP address.
func DialTCP(addr string, opts Options) (*Client, error) {
	opts = withDefaults(opts)
	conn, err := network.DialTCP(addr, opts.MaxPacketSize, opts.DialTimeout)
	if err != nil {
		return nil, err
	}
	return newClient(conn, opts), nil
}

// DialWS connects to a gate's WebSocket url, e.g. "ws://127.0.0.1:7001/ws".
func DialWS(url string, opts Options) (*Client, error) {
	opts = withDefaults(opts)
	conn, err := network.DialWS(url, opts.MaxPacketSize, opts.DialTimeout)
	if err != nil {
		return nil, err
	}
	return newClient(conn, opts), nil
}

func withDefaults(o Options) Options {
	if o.Registry == nil {
		o.Registry = message.Default
	}
	if o.DialTimeout <= 0 {
		o.DialTimeout = 5 * time.Second
	}
	if o.MaxPacketSize <= 0 {
		o.MaxPacketSize = network.DefaultMaxPacketSize
	}
	return o
}

func newClient(conn network.Conn, opts Options) *Client {
	c := &Client{
		conn:    conn,
		opts:    opts,
		pending: make(map[uint32]chan proto.Message),
		closed:  make(chan struct{}),
	}
	go c.readLoop()
	return c
}

// Request sends req and waits for the matching response.
// An ErrorResp from the server is returned as *ServerError.
func (c *Client) Request(ctx context.Context, req proto.Message) (proto.Message, error) {
	id, body, err := c.opts.Registry.Encode(req)
	if err != nil {
		return nil, err
	}
	seq := c.seq.Add(1)
	if seq == 0 { // 0 is reserved for pushes
		seq = c.seq.Add(1)
	}
	ch := make(chan proto.Message, 1)
	c.mu.Lock()
	c.pending[seq] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, seq)
		c.mu.Unlock()
	}()

	if err := c.write(network.Packet{MsgID: id, Seq: seq, Body: body}); err != nil {
		return nil, err
	}
	select {
	case resp := <-ch:
		if e, ok := resp.(*syspb.ErrorResp); ok {
			return nil, &ServerError{Code: e.Code, Msg: e.Message}
		}
		return resp, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.closed:
		return nil, ErrClosed
	}
}

// Call is a typed wrapper around Request.
func Call[Resp proto.Message](ctx context.Context, c *Client, req proto.Message) (Resp, error) {
	var zero Resp
	resp, err := c.Request(ctx, req)
	if err != nil {
		return zero, err
	}
	typed, ok := resp.(Resp)
	if !ok {
		return zero, fmt.Errorf("client: unexpected response %T", resp)
	}
	return typed, nil
}

// StartHeartbeat sends HeartbeatReq every interval until the client closes.
func (c *Client) StartHeartbeat(interval time.Duration) {
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				ctx, cancel := context.WithTimeout(context.Background(), interval)
				_, _ = c.Request(ctx, &syspb.HeartbeatReq{ClientTimeMs: time.Now().UnixMilli()})
				cancel()
			case <-c.closed:
				return
			}
		}
	}()
}

// Done is closed when the connection is closed.
func (c *Client) Done() <-chan struct{} { return c.closed }

// Err returns the reason the connection closed (nil while open).
func (c *Client) Err() error {
	select {
	case <-c.closed:
		return c.closeErr
	default:
		return nil
	}
}

// Close closes the connection.
func (c *Client) Close() error {
	c.shutdown(ErrClosed)
	return nil
}

func (c *Client) shutdown(err error) {
	c.closeOnce.Do(func() {
		c.closeErr = err
		close(c.closed)
		_ = c.conn.Close()
	})
}

func (c *Client) write(p network.Packet) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	select {
	case <-c.closed:
		return ErrClosed
	default:
	}
	_ = c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return c.conn.WriteMessage(p.Encode())
}

func (c *Client) readLoop() {
	for {
		data, err := c.conn.ReadMessage()
		if err != nil {
			c.shutdown(err)
			return
		}
		pkt, err := network.DecodePacket(data)
		if err != nil {
			c.shutdown(err)
			return
		}
		msg, err := c.opts.Registry.Decode(pkt.MsgID, pkt.Body)
		if err != nil {
			continue // unknown message: newer server, ignore
		}
		if pkt.Seq != 0 {
			c.mu.Lock()
			ch := c.pending[pkt.Seq]
			c.mu.Unlock()
			if ch != nil {
				ch <- msg
			}
			continue
		}
		if c.opts.OnPush != nil {
			c.opts.OnPush(pkt.MsgID, msg)
		}
	}
}
