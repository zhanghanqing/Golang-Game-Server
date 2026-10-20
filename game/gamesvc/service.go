// Package gamesvc hosts online players and the world scene.
//
// Concurrency model:
//
//	bus goroutine ──FORWARD──► Player actor (one goroutine per online player)
//	                              │  owns *model.Player, bag, session
//	                              └─post──► Scene actor (one goroutine per scene)
//	                                           owns the AOI grid, pushes view updates
//
// Every piece of mutable state has exactly one owning actor, so handlers are
// written as plain sequential code without locks.
package gamesvc

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"gameserver/framework/cluster"
	"gameserver/framework/logx"
	"gameserver/framework/message"
	"gameserver/framework/pb/clusterpb"
	"gameserver/framework/pb/syspb"
	"gameserver/framework/router"
	"gameserver/game/gamedata"
	"gameserver/game/presence"
	"gameserver/game/protocol"
	"gameserver/game/repo"
)

// Config configures the game service.
type Config struct {
	// SaveInterval is how often dirty players are flushed to storage. Default 30s.
	SaveInterval time.Duration `yaml:"save_interval"`
	// GMEnabled allows debug GM commands. Never enable in production.
	GMEnabled bool `yaml:"gm_enabled"`
	// ChatInterval is the minimum time between two chat messages of one player. Default 1s.
	ChatInterval time.Duration `yaml:"chat_interval"`
	// MaxMoveStep is the farthest a player may move in one SceneMoveReq. Default 50.
	MaxMoveStep float32 `yaml:"max_move_step"`
}

func (c *Config) setDefaults() {
	if c.SaveInterval <= 0 {
		c.SaveInterval = 30 * time.Second
	}
	if c.ChatInterval <= 0 {
		c.ChatInterval = time.Second
	}
	if c.MaxMoveStep <= 0 {
		c.MaxMoveStep = 50
	}
}

// Service is an app.Module.
type Service struct {
	cfg      Config
	cluster  *cluster.Cluster
	router   *router.Router
	players  repo.PlayerRepo
	presence presence.Directory
	tables   *gamedata.Tables
	scene    *Scene
	logger   *slog.Logger

	ctx    context.Context
	cancel context.CancelFunc
	sub    cluster.Subscription

	draining atomic.Bool // moved to the draining registry key: no new players
	stopping atomic.Bool // shutting down: refuse EnterGame

	mu     sync.Mutex
	online map[int64]*Player
}

// Status is a snapshot reported to operators (see the admin endpoint /game/status).
type Status struct {
	Node     string `json:"node"`
	Players  int    `json:"players"`
	Draining bool   `json:"draining"`
	Stopping bool   `json:"stopping"`
}

// New creates the game service.
func New(cfg Config, c *cluster.Cluster, reg *message.Registry, players repo.PlayerRepo, dir presence.Directory, tables *gamedata.Tables) *Service {
	cfg.setDefaults()
	ctx, cancel := context.WithCancel(context.Background())
	s := &Service{
		cfg:      cfg,
		cluster:  c,
		router:   router.New(protocol.ServiceGame, reg, c),
		players:  players,
		presence: dir,
		tables:   tables,
		logger:   logx.Module("game"),
		ctx:      ctx,
		cancel:   cancel,
		online:   make(map[int64]*Player),
	}
	s.router.Use(router.AccessLog(s.logger, 50*time.Millisecond))
	s.registerHandlers()
	return s
}

func (s *Service) Name() string { return protocol.ServiceGame }

func (s *Service) Init(context.Context) error {
	s.scene = NewScene(1, s.router, s.logger)
	return nil
}

func (s *Service) Start(ctx context.Context) error {
	sub, err := s.cluster.SubscribeEnvelopes(
		cluster.NodeSubject(protocol.ServiceGame, s.cluster.NodeID()), "", s.onEnvelope)
	if err != nil {
		return err
	}
	s.sub = sub
	return s.cluster.Registry().Register(ctx, protocol.ServiceGame, s.cluster.NodeID())
}

// Drain stops new players from being assigned to this node. Players already
// here keep playing (and reconnects still find them) until they leave or the
// node is stopped. It is idempotent.
func (s *Service) Drain(ctx context.Context) error {
	if s.draining.Swap(true) {
		return nil
	}
	reg := s.cluster.Registry()
	// Register under the draining key first so the node never looks dead to login.
	if err := reg.Register(ctx, protocol.ServiceGameDraining, s.cluster.NodeID()); err != nil {
		s.draining.Store(false)
		return err
	}
	if err := reg.Deregister(ctx, protocol.ServiceGame, s.cluster.NodeID()); err != nil {
		return err
	}
	s.logger.Info("draining: no new players will be assigned", "online", s.OnlineCount())
	return nil
}

// Status returns the current node status.
func (s *Service) Status() Status {
	return Status{
		Node:     s.cluster.NodeID(),
		Players:  s.OnlineCount(),
		Draining: s.draining.Load(),
		Stopping: s.stopping.Load(),
	}
}

// Stop saves and unloads every online player. Order matters:
//
//  1. Drain: login stops assigning new players, but the node stays "alive"
//     so reconnects of its players are not sent elsewhere too early.
//  2. Kick, save and release every player.
//  3. Deregister completely and stop receiving messages.
func (s *Service) Stop(ctx context.Context) error {
	s.stopping.Store(true)
	if err := s.Drain(ctx); err != nil {
		s.logger.Warn("drain before stop failed", "err", err)
	}
	s.mu.Lock()
	all := make([]*Player, 0, len(s.online))
	for _, p := range s.online {
		all = append(all, p)
	}
	s.mu.Unlock()

	var wg sync.WaitGroup
	for _, p := range all {
		wg.Go(func() {
			_ = p.actor.Call(ctx, func() {
				if p.session.Valid() {
					_ = s.router.Kick(p.session, syspb.KickReason_KICK_SERVER_SHUTDOWN)
				}
				s.logout(p)
			})
		})
	}
	wg.Wait()

	_ = s.cluster.Registry().Deregister(ctx, protocol.ServiceGameDraining, s.cluster.NodeID())
	_ = s.cluster.Registry().Deregister(ctx, protocol.ServiceGame, s.cluster.NodeID())
	if s.sub != nil {
		_ = s.sub.Unsubscribe()
	}
	s.scene.Stop()
	s.cancel()
	s.logger.Info("game service stopped", "saved_players", len(all))
	return nil
}

// HasPlayer reports whether uid is currently loaded on this node.
func (s *Service) HasPlayer(uid int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.online[uid]
	return ok
}

// OnlineCount returns the number of loaded players.
func (s *Service) OnlineCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.online)
}

func (s *Service) onEnvelope(env *clusterpb.Envelope) {
	switch env.Kind {
	case clusterpb.Kind_KIND_FORWARD:
		s.dispatch(env)
	case clusterpb.Kind_KIND_SESSION_CLOSED:
		s.mu.Lock()
		p := s.online[env.Uid]
		s.mu.Unlock()
		if p == nil {
			return
		}
		p.actor.Post(func() {
			// Ignore stale notifications: after a re-login the player is bound to a newer session.
			if p.closed {
				return
			}
			if p.loaded() && (p.session.SessionID != env.SessionId || p.session.GateNode != env.GateNode) {
				return
			}
			s.logout(p)
		})
	}
}

// dispatch routes a client request into the owning player's actor.
func (s *Service) dispatch(env *clusterpb.Envelope) {
	if env.Uid == 0 {
		return // the gate enforces authentication; this is defensive only
	}
	for range 3 {
		p := s.getOrCreate(env.Uid)
		ok := p.actor.Post(func() {
			if p.closed {
				// The player logged out after we picked it: route again to a fresh instance.
				s.dispatch(env)
				return
			}
			s.router.Dispatch(s.ctx, env, p)
		})
		if ok {
			return
		}
	}
	s.logger.Error("dispatch failed: player actor unavailable", "uid", env.Uid)
}

func (s *Service) getOrCreate(uid int64) *Player {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p := s.online[uid]; p != nil {
		return p
	}
	p := newPlayer(uid, s)
	s.online[uid] = p
	return p
}

// logout saves the player, removes it from the scene and stops its actor.
// It must run inside the player's actor.
func (s *Service) logout(p *Player) {
	if p.closed {
		return
	}
	if p.inScene {
		s.scene.Leave(p.uid)
		p.inScene = false
	}
	if p.saveTimer != nil {
		p.saveTimer.Stop()
	}
	p.save()
	// Release ownership before leaving the online map: a request arriving in
	// between still reaches this (closing) entity and is re-dispatched to a
	// fresh one, which then acquires ownership again.
	if p.owned {
		p.releaseOwnership()
	}
	s.mu.Lock()
	if s.online[p.uid] == p {
		delete(s.online, p.uid)
	}
	s.mu.Unlock()
	p.closed = true
	p.actor.Stop()
	s.logger.Info("player logout", "uid", p.uid)
}
