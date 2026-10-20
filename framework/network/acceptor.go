package network

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// ConnHandler is invoked in its own goroutine for every accepted connection.
// The connection is closed by the handler's owner (typically the gate).
type ConnHandler func(Conn)

// Acceptor accepts client connections on one transport.
type Acceptor interface {
	// Listen binds the address. Errors such as "port in use" surface here.
	Listen() error
	// Serve accepts connections until Close is called. It blocks.
	Serve(h ConnHandler) error
	// Addr returns the bound address (useful when listening on port 0).
	Addr() net.Addr
	// Close stops accepting new connections. Existing connections are not touched.
	Close() error
}

// ---------------------------------------------------------------------------
// TCP acceptor

// TCPAcceptor accepts framed TCP connections.
type TCPAcceptor struct {
	addr    string
	maxSize int
	ln      net.Listener
	closed  chan struct{}
	once    sync.Once
}

// NewTCPAcceptor creates an acceptor for addr such as ":7000".
func NewTCPAcceptor(addr string, maxSize int) *TCPAcceptor {
	return &TCPAcceptor{addr: addr, maxSize: maxSize, closed: make(chan struct{})}
}

func (a *TCPAcceptor) Listen() error {
	ln, err := net.Listen("tcp", a.addr)
	if err != nil {
		return err
	}
	a.ln = ln
	return nil
}

func (a *TCPAcceptor) Serve(h ConnHandler) error {
	var backoff time.Duration
	for {
		c, err := a.ln.Accept()
		if err != nil {
			select {
			case <-a.closed:
				return nil
			default:
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				// Temporary resource exhaustion (e.g. too many open files): back off and retry.
				backoff = min(max(backoff*2, 5*time.Millisecond), time.Second)
				slog.Warn("tcp accept error, retrying", "err", err, "backoff", backoff)
				time.Sleep(backoff)
				continue
			}
			return err
		}
		backoff = 0
		go h(NewTCPConn(c, a.maxSize))
	}
}

func (a *TCPAcceptor) Addr() net.Addr {
	if a.ln == nil {
		return nil
	}
	return a.ln.Addr()
}

func (a *TCPAcceptor) Close() error {
	var err error
	a.once.Do(func() {
		close(a.closed)
		if a.ln != nil {
			err = a.ln.Close()
		}
	})
	return err
}

// ---------------------------------------------------------------------------
// WebSocket acceptor

// WSAcceptor accepts WebSocket connections on an HTTP path.
type WSAcceptor struct {
	addr     string
	path     string
	maxSize  int
	ln       net.Listener
	upgrader websocket.Upgrader

	// mu guards srv and closed: Serve runs in its own goroutine while Close
	// may be called concurrently from the shutdown path.
	mu     sync.Mutex
	srv    *http.Server
	closed bool
}

// NewWSAcceptor creates an acceptor for addr (e.g. ":7001") and path (e.g. "/ws").
// checkOrigin may be nil to accept every origin (fine for native clients;
// browsers games should pass a stricter function).
func NewWSAcceptor(addr, path string, maxSize int, checkOrigin func(r *http.Request) bool) *WSAcceptor {
	if path == "" {
		path = "/ws"
	}
	if checkOrigin == nil {
		checkOrigin = func(*http.Request) bool { return true }
	}
	return &WSAcceptor{
		addr:    addr,
		path:    path,
		maxSize: maxSize,
		upgrader: websocket.Upgrader{
			ReadBufferSize:  4096,
			WriteBufferSize: 4096,
			CheckOrigin:     checkOrigin,
		},
	}
}

func (a *WSAcceptor) Listen() error {
	ln, err := net.Listen("tcp", a.addr)
	if err != nil {
		return err
	}
	a.ln = ln
	return nil
}

func (a *WSAcceptor) Serve(h ConnHandler) error {
	mux := http.NewServeMux()
	mux.HandleFunc(a.path, func(w http.ResponseWriter, r *http.Request) {
		c, err := a.upgrader.Upgrade(w, r, nil)
		if err != nil {
			return // Upgrade already wrote an HTTP error
		}
		go h(NewWSConn(c, a.maxSize))
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	a.mu.Lock()
	if a.closed {
		// Close won the race: the listener is already closed, nothing to serve.
		a.mu.Unlock()
		return nil
	}
	a.srv = srv
	a.mu.Unlock()

	err := srv.Serve(a.ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (a *WSAcceptor) Addr() net.Addr {
	if a.ln == nil {
		return nil
	}
	return a.ln.Addr()
}

func (a *WSAcceptor) Close() error {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return nil
	}
	a.closed = true
	srv := a.srv
	a.mu.Unlock()

	if srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		// Shutdown does not track hijacked (upgraded) connections, so it only stops the listener.
		return srv.Shutdown(ctx)
	}
	if a.ln != nil {
		return a.ln.Close()
	}
	return nil
}
