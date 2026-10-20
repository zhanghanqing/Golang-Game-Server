// Package gate implements the client-facing gateway service.
//
// The gate terminates TCP and WebSocket connections, keeps one Session per
// client, enforces heartbeats and rate limits, and forwards client requests to
// backend services according to a route table based on message id ranges.
// Backends answer through the bus; the gate delivers their packets to the
// right session. The gate holds no game logic, so it scales horizontally and
// shields backends from raw client traffic.
package gate

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"golang.org/x/time/rate"
	"google.golang.org/protobuf/proto"

	"gameserver/framework/cluster"
	"gameserver/framework/errcode"
	"gameserver/framework/logx"
	"gameserver/framework/message"
	"gameserver/framework/network"
	"gameserver/framework/pb/clusterpb"
	"gameserver/framework/pb/syspb"
	"gameserver/framework/router"
)

var (
	metricSessions = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "gs_gate_sessions", Help: "Currently connected client sessions.",
	})
	metricPacketsIn = promauto.NewCounter(prometheus.CounterOpts{
		Name: "gs_gate_packets_in_total", Help: "Packets received from clients.",
	})
	metricPacketsOut = promauto.NewCounter(prometheus.CounterOpts{
		Name: "gs_gate_packets_out_total", Help: "Packets queued to clients.",
	})
	metricRejected = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gs_gate_rejected_total", Help: "Requests rejected by the gate.",
	}, []string{"reason"})
)

// Config configures the gate.
type Config struct {
	// TCPAddr is the TCP listen address, e.g. ":7000". Empty disables TCP.
	TCPAddr string `yaml:"tcp_addr"`
	// WSAddr is the WebSocket listen address, e.g. ":7001". Empty disables WebSocket.
	WSAddr string `yaml:"ws_addr"`
	// WSPath is the WebSocket HTTP path. Default "/ws".
	WSPath string `yaml:"ws_path"`
	// MaxConns limits concurrent connections. 0 means unlimited.
	MaxConns int `yaml:"max_conns"`
	// MaxPacketSize limits one payload. Default 64 KiB.
	MaxPacketSize int `yaml:"max_packet_size"`
	// SendQueueSize is the per-session outgoing queue length. Default 256.
	SendQueueSize int `yaml:"send_queue_size"`
	// IdleTimeout closes sessions that send nothing (not even heartbeats). Default 60s.
	IdleTimeout time.Duration `yaml:"idle_timeout"`
	// RateLimit is the sustained requests/second allowed per session. Default 50.
	RateLimit float64 `yaml:"rate_limit"`
	// RateBurst is the burst size of the per-session limiter. Default 100.
	RateBurst int `yaml:"rate_burst"`
}

func (c *Config) setDefaults() {
	if c.MaxPacketSize <= 0 {
		c.MaxPacketSize = network.DefaultMaxPacketSize
	}
	if c.SendQueueSize <= 0 {
		c.SendQueueSize = 256
	}
	if c.IdleTimeout <= 0 {
		c.IdleTimeout = 60 * time.Second
	}
	if c.RateLimit <= 0 {
		c.RateLimit = 50
	}
	if c.RateBurst <= 0 {
		c.RateBurst = 100
	}
}

// Route maps a message id range to a backend service.
type Route struct {
	// MinID and MaxID are inclusive bounds.
	MinID, MaxID uint32
	// Service is the backend service name, e.g. "login".
	Service string
	// RequireAuth rejects the request unless the session has been bound to a uid.
	RequireAuth bool
	// Sticky routes to the node bound in the session (via Bind) instead of load balancing.
	Sticky bool
}

// clientSession is the gate's view of a connected client.
type clientSession struct {
	*network.Session
	limiter *rate.Limiter

	mu     sync.RWMutex
	uid    int64
	routes map[string]string // service -> node id
}

func (s *clientSession) snapshot() (int64, map[string]string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	routes := make(map[string]string, len(s.routes))
	for k, v := range s.routes {
		routes[k] = v
	}
	return s.uid, routes
}

// Gate is an app.Module.
type Gate struct {
	cfg     Config
	routes  []Route
	cluster *cluster.Cluster
	reg     *message.Registry
	logger  *slog.Logger

	acceptors []network.Acceptor
	subs      []cluster.Subscription

	mu       sync.RWMutex
	sessions map[uint64]*clientSession
	byUID    map[int64]*clientSession

	nextID  atomic.Uint64
	active  atomic.Int64
	closing atomic.Bool
	wg      sync.WaitGroup
}

// New creates a gate. routes must not overlap and must not cover framework ids (<= 999).
func New(cfg Config, c *cluster.Cluster, reg *message.Registry, routes []Route) *Gate {
	cfg.setDefaults()
	return &Gate{
		cfg:      cfg,
		routes:   routes,
		cluster:  c,
		reg:      reg,
		logger:   logx.Module("gate"),
		sessions: make(map[uint64]*clientSession),
		byUID:    make(map[int64]*clientSession),
	}
}

func (g *Gate) Name() string { return "gate" }

func (g *Gate) Init(context.Context) error {
	if g.cfg.TCPAddr == "" && g.cfg.WSAddr == "" {
		return errors.New("gate: neither tcp_addr nor ws_addr configured")
	}
	sort.Slice(g.routes, func(i, j int) bool { return g.routes[i].MinID < g.routes[j].MinID })
	for i, r := range g.routes {
		if r.MinID <= message.MaxSystemID || r.MaxID < r.MinID {
			return fmt.Errorf("gate: invalid route %+v", r)
		}
		if i > 0 && r.MinID <= g.routes[i-1].MaxID {
			return fmt.Errorf("gate: route %+v overlaps %+v", r, g.routes[i-1])
		}
		if err := cluster.ValidateToken(r.Service); err != nil {
			return err
		}
	}
	return nil
}

func (g *Gate) Start(ctx context.Context) error {
	self := g.cluster.NodeID()
	for _, subject := range []string{
		cluster.NodeSubject(router.GateService, self),
		cluster.BroadcastSubject(router.GateService),
	} {
		sub, err := g.cluster.SubscribeEnvelopes(subject, "", g.onEnvelope)
		if err != nil {
			return err
		}
		g.subs = append(g.subs, sub)
	}

	if g.cfg.TCPAddr != "" {
		g.acceptors = append(g.acceptors, network.NewTCPAcceptor(g.cfg.TCPAddr, g.cfg.MaxPacketSize))
	}
	if g.cfg.WSAddr != "" {
		g.acceptors = append(g.acceptors, network.NewWSAcceptor(g.cfg.WSAddr, g.cfg.WSPath, g.cfg.MaxPacketSize, nil))
	}
	for _, a := range g.acceptors {
		if err := a.Listen(); err != nil {
			return err
		}
		g.logger.Info("gate listening", "addr", a.Addr().String(), "transport", fmt.Sprintf("%T", a))
		go func(a network.Acceptor) {
			if err := a.Serve(g.handleConn); err != nil {
				g.logger.Error("acceptor stopped", "err", err)
			}
		}(a)
	}
	return g.cluster.Registry().Register(ctx, router.GateService, self)
}

func (g *Gate) Stop(ctx context.Context) error {
	g.closing.Store(true)
	_ = g.cluster.Registry().Deregister(ctx, router.GateService, g.cluster.NodeID())
	for _, a := range g.acceptors {
		_ = a.Close()
	}
	// Tell every client why it is being disconnected, then close after flushing.
	kick := g.kickPacket(syspb.KickReason_KICK_SERVER_SHUTDOWN)
	g.mu.RLock()
	for _, s := range g.sessions {
		s.CloseAfterFlush(kick)
	}
	g.mu.RUnlock()

	done := make(chan struct{})
	go func() { g.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		g.logger.Warn("gate stop timed out waiting for sessions")
	}
	for _, s := range g.subs {
		_ = s.Unsubscribe()
	}
	return nil
}

// TCPAddr returns the bound TCP address (nil if disabled). Useful with port 0 in tests.
func (g *Gate) TCPAddr() net.Addr { return g.addrOf("*network.TCPAcceptor") }

// WSAddr returns the bound WebSocket address (nil if disabled).
func (g *Gate) WSAddr() net.Addr { return g.addrOf("*network.WSAcceptor") }

func (g *Gate) addrOf(typ string) net.Addr {
	for _, a := range g.acceptors {
		if fmt.Sprintf("%T", a) == typ {
			return a.Addr()
		}
	}
	return nil
}

// SessionCount returns the number of connected sessions.
func (g *Gate) SessionCount() int {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return len(g.sessions)
}

// ---------------------------------------------------------------------------
// Client side

func (g *Gate) handleConn(conn network.Conn) {
	if g.closing.Load() {
		_ = conn.Close()
		return
	}
	if g.cfg.MaxConns > 0 && g.active.Add(1) > int64(g.cfg.MaxConns) {
		g.active.Add(-1)
		metricRejected.WithLabelValues("max_conns").Inc()
		_ = conn.Close()
		return
	} else if g.cfg.MaxConns <= 0 {
		g.active.Add(1)
	}
	g.wg.Add(1)
	defer func() {
		g.active.Add(-1)
		g.wg.Done()
	}()

	s := &clientSession{
		Session: network.NewSession(g.nextID.Add(1), conn, g.cfg.SendQueueSize),
		limiter: rate.NewLimiter(rate.Limit(g.cfg.RateLimit), g.cfg.RateBurst),
		routes:  make(map[string]string),
	}
	g.mu.Lock()
	g.sessions[s.ID()] = s
	g.mu.Unlock()
	metricSessions.Inc()
	g.logger.Debug("session opened", "sid", s.ID(), "remote", conn.RemoteAddr().String())

	g.readLoop(s)
	g.onSessionClosed(s)
}

func (g *Gate) readLoop(s *clientSession) {
	defer s.Close()
	conn := s.Conn()
	for {
		_ = conn.SetReadDeadline(time.Now().Add(g.cfg.IdleTimeout))
		data, err := conn.ReadMessage()
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				s.CloseAfterFlush(g.kickPacket(syspb.KickReason_KICK_IDLE_TIMEOUT))
				// Wait briefly for the kick to flush before the deferred Close.
				select {
				case <-s.Done():
				case <-time.After(time.Second):
				}
			}
			return
		}
		pkt, err := network.DecodePacket(data)
		if err != nil {
			return // malformed client: drop the connection
		}
		metricPacketsIn.Inc()
		g.handlePacket(s, pkt)
	}
}

func (g *Gate) handlePacket(s *clientSession, pkt network.Packet) {
	if pkt.MsgID == uint32(syspb.SysMsgID_HEARTBEAT_REQ) {
		req := &syspb.HeartbeatReq{}
		_ = proto.Unmarshal(pkt.Body, req)
		g.sendMsg(s, pkt.Seq, &syspb.HeartbeatResp{
			ClientTimeMs: req.ClientTimeMs,
			ServerTimeMs: time.Now().UnixMilli(),
		})
		return
	}
	if !s.limiter.Allow() {
		metricRejected.WithLabelValues("rate_limited").Inc()
		g.sendError(s, pkt, errcode.ErrRateLimited)
		return
	}
	route := g.match(pkt.MsgID)
	if route == nil {
		metricRejected.WithLabelValues("unknown_message").Inc()
		g.sendError(s, pkt, errcode.ErrUnknownMessage)
		return
	}
	uid, routes := s.snapshot()
	if route.RequireAuth && uid == 0 {
		metricRejected.WithLabelValues("not_authenticated").Inc()
		g.sendError(s, pkt, errcode.ErrNotAuthenticated)
		return
	}
	env := &clusterpb.Envelope{
		Kind:      clusterpb.Kind_KIND_FORWARD,
		GateNode:  g.cluster.NodeID(),
		SessionId: s.ID(),
		Uid:       uid,
		MsgId:     pkt.MsgID,
		Seq:       pkt.Seq,
		Body:      pkt.Body,
	}
	var err error
	if route.Sticky {
		node := routes[route.Service]
		if node == "" {
			g.sendError(s, pkt, errcode.ErrServiceUnavailable)
			return
		}
		err = g.cluster.SendToNode(route.Service, node, env)
	} else {
		if len(g.cluster.Registry().Nodes(route.Service)) == 0 {
			g.sendError(s, pkt, errcode.ErrServiceUnavailable)
			return
		}
		err = g.cluster.SendToService(route.Service, env)
	}
	if err != nil {
		g.logger.Warn("forward failed", "service", route.Service, "err", err)
		g.sendError(s, pkt, errcode.ErrServiceUnavailable)
	}
}

func (g *Gate) match(id uint32) *Route {
	// Routes are few; a binary search keeps it O(log n) anyway.
	i := sort.Search(len(g.routes), func(i int) bool { return g.routes[i].MaxID >= id })
	if i < len(g.routes) && g.routes[i].MinID <= id {
		return &g.routes[i]
	}
	return nil
}

func (g *Gate) onSessionClosed(s *clientSession) {
	uid, routes := s.snapshot()
	g.mu.Lock()
	delete(g.sessions, s.ID())
	if uid != 0 && g.byUID[uid] == s {
		delete(g.byUID, uid)
	}
	g.mu.Unlock()
	metricSessions.Dec()
	g.logger.Debug("session closed", "sid", s.ID(), "uid", uid)

	// Let every sticky backend clean up (e.g. save and unload the player).
	for service, node := range routes {
		_ = g.cluster.SendToNode(service, node, &clusterpb.Envelope{
			Kind:      clusterpb.Kind_KIND_SESSION_CLOSED,
			GateNode:  g.cluster.NodeID(),
			SessionId: s.ID(),
			Uid:       uid,
		})
	}
}

func (g *Gate) sendMsg(s *clientSession, seq uint32, msg proto.Message) {
	id, body, err := g.reg.Encode(msg)
	if err != nil {
		g.logger.Error("encode failed", "err", err)
		return
	}
	if s.Send(network.Packet{MsgID: id, Seq: seq, Body: body}) == nil {
		metricPacketsOut.Inc()
	}
}

func (g *Gate) sendError(s *clientSession, req network.Packet, err *errcode.Error) {
	g.sendMsg(s, req.Seq, &syspb.ErrorResp{Code: err.Code, Message: err.Msg, ReqMsgId: req.MsgID})
}

func (g *Gate) kickPacket(reason syspb.KickReason) network.Packet {
	body, _ := proto.Marshal(&syspb.KickPush{Reason: reason, Message: reason.String()})
	return network.Packet{MsgID: uint32(syspb.SysMsgID_KICK_PUSH), Body: body}
}

// ---------------------------------------------------------------------------
// Backend side

func (g *Gate) session(id uint64) *clientSession {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.sessions[id]
}

func (g *Gate) onEnvelope(env *clusterpb.Envelope) {
	switch env.Kind {
	case clusterpb.Kind_KIND_REPLY:
		if s := g.session(env.SessionId); s != nil {
			if s.Send(network.Packet{MsgID: env.MsgId, Seq: env.Seq, Body: env.Body}) == nil {
				metricPacketsOut.Inc()
			}
		}

	case clusterpb.Kind_KIND_BIND:
		s := g.session(env.SessionId)
		if s == nil {
			return
		}
		s.mu.Lock()
		if env.Uid != 0 {
			s.uid = env.Uid
		}
		for svc, node := range env.Routes {
			s.routes[svc] = node
		}
		uid := s.uid
		s.mu.Unlock()
		if uid != 0 {
			g.mu.Lock()
			g.byUID[uid] = s
			g.mu.Unlock()
		}

	case clusterpb.Kind_KIND_KICK:
		if s := g.session(env.SessionId); s != nil {
			s.CloseAfterFlush(g.kickPacket(syspb.KickReason(env.Reason)))
		}

	case clusterpb.Kind_KIND_BROADCAST:
		payload := network.Packet{MsgID: env.MsgId, Body: env.Body}.Encode()
		g.mu.RLock()
		var targets []*clientSession
		if len(env.Uids) == 0 {
			targets = make([]*clientSession, 0, len(g.byUID))
			for _, s := range g.byUID {
				targets = append(targets, s)
			}
		} else {
			for _, uid := range env.Uids {
				if s := g.byUID[uid]; s != nil {
					targets = append(targets, s)
				}
			}
		}
		g.mu.RUnlock()
		for _, s := range targets {
			if s.SendRaw(payload) == nil {
				metricPacketsOut.Inc()
			}
		}

	default:
		g.logger.Warn("unexpected envelope kind", "kind", env.Kind.String())
	}
}
