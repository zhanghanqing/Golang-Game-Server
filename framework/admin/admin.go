// Package admin exposes an internal HTTP server for operations:
//
//	GET /healthz        liveness probe (process is up)
//	GET /readyz         readiness probe (all modules started, not shutting down)
//	GET /metrics        Prometheus metrics
//	GET /debug/pprof/   Go profiler (CPU, heap, goroutines...)
//
// Never expose this port to the internet.
package admin

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"gameserver/framework/logx"
)

// Config configures the admin server.
type Config struct {
	// Addr is the listen address, e.g. "127.0.0.1:9100". Empty disables the server.
	Addr string `yaml:"addr"`
}

// Server is an app.Module. Add it early so it stops late.
type Server struct {
	cfg    Config
	srv    *http.Server
	ln     net.Listener
	ready  atomic.Bool
	mux    *http.ServeMux
	logger *slog.Logger
}

// New creates the admin server.
func New(cfg Config) *Server {
	s := &Server{cfg: cfg, mux: http.NewServeMux(), logger: logx.Module("admin")}
	s.mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	s.mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		if !s.ready.Load() {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ready"))
	})
	s.mux.Handle("/metrics", promhttp.Handler())
	s.mux.HandleFunc("/debug/pprof/", pprof.Index)
	s.mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	s.mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	s.mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	s.mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	return s
}

// Handle lets other modules add admin endpoints (GM tools, reload hooks...).
func (s *Server) Handle(pattern string, h http.Handler) { s.mux.Handle(pattern, h) }

// SetReady flips the readiness probe.
func (s *Server) SetReady(v bool) { s.ready.Store(v) }

func (s *Server) Name() string { return "admin" }

func (s *Server) Init(context.Context) error { return nil }

func (s *Server) Start(context.Context) error {
	if s.cfg.Addr == "" {
		return nil
	}
	ln, err := net.Listen("tcp", s.cfg.Addr)
	if err != nil {
		return err
	}
	s.ln = ln
	s.srv = &http.Server{Handler: s.mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := s.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.logger.Error("admin server stopped", "err", err)
		}
	}()
	s.logger.Info("admin listening", "addr", ln.Addr().String())
	return nil
}

func (s *Server) Stop(ctx context.Context) error {
	s.ready.Store(false)
	if s.srv == nil {
		return nil
	}
	return s.srv.Shutdown(ctx)
}
