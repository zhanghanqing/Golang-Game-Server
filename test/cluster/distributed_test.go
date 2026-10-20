// Package cluster_test runs the distributed topology inside one test process:
// a real (embedded) NATS server, an in-memory Redis (miniredis) for service
// discovery, and five independent nodes:
//
//	gate-1  gate-2  login-1  game-1  game-2
//
// Every node has its own NATS connection and registry client, exactly as if it
// were a separate process, so this exercises the same code paths as a
// docker-compose deployment.
package cluster_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"google.golang.org/protobuf/proto"

	"gameserver/framework/client"
	"gameserver/framework/pb/syspb"
	"gameserver/game/bootstrap"
	"gameserver/game/pb"
	"gameserver/game/protocol"
	"gameserver/game/repo"
)

type env struct {
	natsURL   string
	redisAddr string
	accounts  repo.AccountRepo
	players   repo.PlayerRepo
	nodes     map[string]*bootstrap.Server
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ns, err := natsserver.NewServer(&natsserver.Options{Host: "127.0.0.1", Port: -1, NoLog: true, NoSigs: true})
	if err != nil {
		t.Fatal(err)
	}
	go ns.Start()
	if !ns.ReadyForConnections(5 * time.Second) {
		t.Fatal("nats not ready")
	}
	t.Cleanup(ns.Shutdown)
	mr := miniredis.RunT(t)
	return &env{
		natsURL:   ns.ClientURL(),
		redisAddr: mr.Addr(),
		// Stand-in for the shared MongoDB of a real deployment.
		accounts: repo.NewMemoryAccounts(),
		players:  repo.NewMemoryPlayers(),
		nodes:    map[string]*bootstrap.Server{},
	}
}

func (e *env) start(t *testing.T, id string, roles ...string) *bootstrap.Server {
	t.Helper()
	cfg := &bootstrap.Config{}
	cfg.Node.ID = id
	cfg.Node.Roles = roles
	cfg.Node.WorkerID = int64(len(e.nodes) + 1)
	cfg.Log.Level = "error"
	cfg.Cluster.Bus = "nats"
	cfg.Cluster.NatsURL = e.natsURL
	cfg.Cluster.Registry = "redis"
	cfg.Cluster.RegistryTTL = 3 * time.Second
	cfg.Redis.Addr = e.redisAddr
	cfg.Storage.Driver = "mongo" // satisfied by WithRepos below; keeps Validate honest
	cfg.Gate.TCPAddr = "127.0.0.1:0"
	cfg.Login.BcryptCost = 4
	cfg.Game.GMEnabled = true
	cfg.Game.ChatInterval = time.Millisecond
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	srv, err := bootstrap.Build(cfg, bootstrap.WithRepos(e.accounts, e.players))
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.App.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.App.Stop() })
	e.nodes[id] = srv
	return srv
}

type pushBox struct {
	mu   sync.Mutex
	msgs []proto.Message
}

func (b *pushBox) add(_ uint32, m proto.Message) {
	b.mu.Lock()
	b.msgs = append(b.msgs, m)
	b.mu.Unlock()
}

func waitPush[T proto.Message](t *testing.T, b *pushBox, match func(T) bool) T {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		b.mu.Lock()
		for _, m := range b.msgs {
			if typed, ok := m.(T); ok && match(typed) {
				b.mu.Unlock()
				return typed
			}
		}
		b.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	var zero T
	t.Fatalf("timed out waiting for %T", zero)
	return zero
}

type conn struct {
	c      *client.Client
	pushes *pushBox
	uid    int64
}

func connect(t *testing.T, gate *bootstrap.Server) *conn {
	t.Helper()
	box := &pushBox{}
	c, err := client.DialTCP(gate.Gate.TCPAddr().String(), client.Options{Registry: protocol.Registry(), OnPush: box.add})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return &conn{c: c, pushes: box}
}

func do[T proto.Message](t *testing.T, c *conn, req proto.Message) T {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := client.Call[T](ctx, c.c, req)
	if err != nil {
		t.Fatalf("%T: %v", req, err)
	}
	return resp
}

func enter(t *testing.T, c *conn, account string) {
	t.Helper()
	c.uid = do[*pb.LoginResp](t, c, &pb.LoginReq{Account: account, Password: "secret123"}).Uid
	do[*pb.EnterGameResp](t, c, &pb.EnterGameReq{Name: account})
}

// gameNodeOf finds which game node holds uid.
func (e *env) gameNodeOf(uid int64) string {
	for id, n := range e.nodes {
		if n.Game != nil && n.Game.HasPlayer(uid) {
			return id
		}
	}
	return ""
}

func TestDistributedCluster(t *testing.T) {
	e := newEnv(t)
	e.start(t, "game-1", bootstrap.RoleGame)
	e.start(t, "game-2", bootstrap.RoleGame)
	e.start(t, "login-1", bootstrap.RoleLogin)
	gate1 := e.start(t, "gate-1", bootstrap.RoleGate)
	gate2 := e.start(t, "gate-2", bootstrap.RoleGate)

	if nodes := gate1.Cluster.Registry().Nodes(protocol.ServiceGame); len(nodes) != 2 {
		t.Fatalf("registry sees game nodes %v", nodes)
	}

	// Log in enough players through both gates that both game nodes get some.
	var players []*conn
	for i, name := range []string{"ann", "ben", "cat", "dan", "eve", "fay", "gus", "hal"} {
		g := gate1
		if i%2 == 1 {
			g = gate2
		}
		c := connect(t, g)
		enter(t, c, name)
		players = append(players, c)
	}
	used := map[string]bool{}
	for _, p := range players {
		node := e.gameNodeOf(p.uid)
		if node == "" {
			t.Fatalf("uid %d not loaded on any game node", p.uid)
		}
		used[node] = true
	}
	if len(used) != 2 {
		t.Fatalf("players not spread over both game nodes: %v", used)
	}

	// World chat crosses game nodes and gates: everyone receives it.
	do[*pb.ChatSendResp](t, players[0], &pb.ChatSendReq{Channel: pb.ChatChannel_CHAT_CHANNEL_WORLD, Text: "hello cluster"})
	for _, p := range players {
		waitPush(t, p.pushes, func(m *pb.ChatPush) bool { return m.Text == "hello cluster" })
	}

	// Re-login of "ann" through the OTHER gate: sticky routing sends it to the
	// same game node, which kicks the old session on gate-1.
	ann := players[0]
	do[*pb.GmCommandResp](t, ann, &pb.GmCommandReq{Command: "addgold 55"})
	before := e.gameNodeOf(ann.uid)
	ann2 := connect(t, gate2)
	enter(t, ann2, "ann")
	if after := e.gameNodeOf(ann.uid); after != before {
		t.Fatalf("re-login moved player from %s to %s", before, after)
	}
	kick := waitPush(t, ann.pushes, func(*syspb.KickPush) bool { return true })
	if kick.Reason != syspb.KickReason_KICK_DUPLICATE_LOGIN {
		t.Fatalf("kick reason %v", kick.Reason)
	}
	info := do[*pb.GmCommandResp](t, ann2, &pb.GmCommandReq{Command: "info"})
	if info.Output == "" {
		t.Fatal("empty info")
	}

	// Disconnect saves the player to the shared store.
	_ = ann2.c.Close()
	deadline := time.Now().Add(5 * time.Second)
	for {
		p, err := e.players.Load(context.Background(), ann.uid)
		if err == nil && p.Gold == 55 && e.gameNodeOf(ann.uid) == "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("player not saved/unloaded: %+v %v", p, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestLoginUnavailableWithoutGameNodes(t *testing.T) {
	e := newEnv(t)
	e.start(t, "login-1", bootstrap.RoleLogin)
	gate := e.start(t, "gate-1", bootstrap.RoleGate)
	c := connect(t, gate)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := c.c.Request(ctx, &pb.LoginReq{Account: "zed", Password: "secret123"})
	var se *client.ServerError
	if !errors.As(err, &se) || se.Code != int32(syspb.ErrorCode_SERVICE_UNAVAILABLE) {
		t.Fatalf("expected SERVICE_UNAVAILABLE, got %v", err)
	}
}
