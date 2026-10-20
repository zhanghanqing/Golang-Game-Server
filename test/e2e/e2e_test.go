// Package e2e starts a complete all-in-one server in-process and plays the
// game through real TCP and WebSocket connections, exactly like a client would.
package e2e

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"gameserver/framework/client"
	"gameserver/framework/pb/syspb"
	"gameserver/game/bootstrap"
	"gameserver/game/pb"
	"gameserver/game/protocol"
)

// pushBox collects server pushes so tests can wait for specific ones.
type pushBox struct {
	mu    sync.Mutex
	msgs  []proto.Message
	added chan struct{}
}

func newPushBox() *pushBox { return &pushBox{added: make(chan struct{}, 1024)} }

func (b *pushBox) add(_ uint32, m proto.Message) {
	b.mu.Lock()
	b.msgs = append(b.msgs, m)
	b.mu.Unlock()
	select {
	case b.added <- struct{}{}:
	default:
	}
}

// waitFor blocks until a push satisfying match arrives.
func waitFor[T proto.Message](t *testing.T, b *pushBox, match func(T) bool) T {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		b.mu.Lock()
		for _, m := range b.msgs {
			if typed, ok := m.(T); ok && match(typed) {
				b.mu.Unlock()
				return typed
			}
		}
		b.mu.Unlock()
		select {
		case <-b.added:
		case <-deadline:
			var zero T
			t.Fatalf("timed out waiting for push %T", zero)
			return zero
		}
	}
}

func startServer(t *testing.T) *bootstrap.Server {
	t.Helper()
	cfg := &bootstrap.Config{}
	cfg.Node.ID = "e2e-1"
	cfg.Node.Roles = []string{bootstrap.RoleGate, bootstrap.RoleLogin, bootstrap.RoleGame}
	cfg.Log.Level = "error"
	cfg.Gate.TCPAddr = "127.0.0.1:0"
	cfg.Gate.WSAddr = "127.0.0.1:0"
	cfg.Login.BcryptCost = 4 // bcrypt.MinCost keeps tests fast
	cfg.Game.GMEnabled = true
	cfg.Game.ChatInterval = time.Millisecond
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	srv, err := bootstrap.Build(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.App.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.App.Stop() })
	return srv
}

type player struct {
	c      *client.Client
	pushes *pushBox
	uid    int64
}

func dial(t *testing.T, srv *bootstrap.Server, ws bool) *player {
	t.Helper()
	box := newPushBox()
	opts := client.Options{Registry: protocol.Registry(), OnPush: box.add}
	var (
		c   *client.Client
		err error
	)
	if ws {
		c, err = client.DialWS(fmt.Sprintf("ws://%s/ws", srv.Gate.WSAddr()), opts)
	} else {
		c, err = client.DialTCP(srv.Gate.TCPAddr().String(), opts)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return &player{c: c, pushes: box}
}

func call[T proto.Message](t *testing.T, p *player, req proto.Message) T {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	resp, err := client.Call[T](ctx, p.c, req)
	if err != nil {
		t.Fatalf("%T failed: %v", req, err)
	}
	return resp
}

func callErr(t *testing.T, p *player, req proto.Message) int32 {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := p.c.Request(ctx, req)
	var se *client.ServerError
	if !errors.As(err, &se) {
		t.Fatalf("%T: expected server error, got %v", req, err)
	}
	return se.Code
}

func login(t *testing.T, p *player, account string) *pb.EnterGameResp {
	t.Helper()
	lr := call[*pb.LoginResp](t, p, &pb.LoginReq{Account: account, Password: "secret123"})
	p.uid = lr.Uid
	return call[*pb.EnterGameResp](t, p, &pb.EnterGameReq{Name: account})
}

func TestFullGameFlow(t *testing.T) {
	srv := startServer(t)

	alice := dial(t, srv, false)
	bob := dial(t, srv, true) // WebSocket client

	// Protocol guards.
	if code := callErr(t, alice, &pb.BagGetReq{}); code != int32(syspb.ErrorCode_NOT_AUTHENTICATED) {
		t.Fatalf("unauthenticated request: code %d", code)
	}
	if code := callErr(t, alice, &pb.LoginReq{Account: "x", Password: "secret123"}); code != int32(pb.ErrCode_ERR_INVALID_ACCOUNT) {
		t.Fatalf("invalid account: code %d", code)
	}

	// Heartbeat is answered by the gate itself.
	hb := call[*syspb.HeartbeatResp](t, alice, &syspb.HeartbeatReq{ClientTimeMs: 1})
	if hb.ClientTimeMs != 1 || hb.ServerTimeMs == 0 {
		t.Fatalf("bad heartbeat: %+v", hb)
	}

	ea := login(t, alice, "alice")
	if ea.Player.Level != 1 || ea.Player.Name != "alice" {
		t.Fatalf("bad new player: %+v", ea.Player)
	}
	login(t, bob, "bob")

	// Both spawn at the same point, so they must see each other (AOI).
	waitFor(t, alice.pushes, func(m *pb.SceneEntityEnterPush) bool {
		return len(m.Entities) == 1 && m.Entities[0].Uid == bob.uid
	})
	waitFor(t, bob.pushes, func(m *pb.SceneEntityEnterPush) bool {
		return len(m.Entities) == 1 && m.Entities[0].Uid == alice.uid
	})

	// Bag: starter pack and item use (EXP potion +50 exp per unit).
	bag := call[*pb.BagGetResp](t, alice, &pb.BagGetReq{})
	if len(bag.Items) != 3 {
		t.Fatalf("starter bag: %+v", bag.Items)
	}
	call[*pb.ItemUseResp](t, alice, &pb.ItemUseReq{ItemId: 1001, Count: 2})
	waitFor(t, alice.pushes, func(m *pb.PlayerInfoPush) bool { return m.Player.Level == 2 && m.Player.Exp == 0 })
	waitFor(t, alice.pushes, func(m *pb.BagUpdatePush) bool { return m.Items[0].ItemId == 1001 && m.Items[0].Count == 3 })
	if code := callErr(t, alice, &pb.ItemUseReq{ItemId: 2001}); code != int32(pb.ErrCode_ERR_ITEM_NOT_USABLE) {
		t.Fatalf("unusable item: code %d", code)
	}
	if code := callErr(t, alice, &pb.ItemUseReq{ItemId: 1002, Count: 99}); code != int32(pb.ErrCode_ERR_ITEM_NOT_ENOUGH) {
		t.Fatalf("not enough: code %d", code)
	}

	// World chat reaches bob through the gate broadcast.
	call[*pb.ChatSendResp](t, alice, &pb.ChatSendReq{Channel: pb.ChatChannel_CHAT_CHANNEL_WORLD, Text: "hi bob"})
	waitFor(t, bob.pushes, func(m *pb.ChatPush) bool { return m.Text == "hi bob" && m.FromUid == alice.uid })

	// Movement: bob sees alice move; teleporting is rejected.
	call[*pb.SceneMoveResp](t, alice, &pb.SceneMoveReq{Target: &pb.Vector2{X: 520, Y: 510}})
	waitFor(t, bob.pushes, func(m *pb.SceneEntityMovePush) bool { return m.Uid == alice.uid && m.Position.X == 520 })
	if code := callErr(t, alice, &pb.SceneMoveReq{Target: &pb.Vector2{X: 10, Y: 10}}); code != int32(pb.ErrCode_ERR_MOVE_TOO_FAR) {
		t.Fatalf("teleport: code %d", code)
	}

	// GM command.
	gm := call[*pb.GmCommandResp](t, alice, &pb.GmCommandReq{Command: "addgold 1000"})
	if gm.Output == "" {
		t.Fatal("empty gm output")
	}

	// Duplicate login: a second connection for alice kicks the first one.
	alice2 := dial(t, srv, false)
	if code := callErr(t, alice2, &pb.LoginReq{Account: "alice", Password: "wrong-pass"}); code != int32(pb.ErrCode_ERR_WRONG_PASSWORD) {
		t.Fatalf("wrong password: code %d", code)
	}
	again := login(t, alice2, "alice")
	if again.Player.Level != 2 || again.Player.Gold != 1000 {
		t.Fatalf("state not kept across re-login: %+v", again.Player)
	}
	kick := waitFor(t, alice.pushes, func(*syspb.KickPush) bool { return true })
	if kick.Reason != syspb.KickReason_KICK_DUPLICATE_LOGIN {
		t.Fatalf("kick reason %v", kick.Reason)
	}
	select {
	case <-alice.c.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("old connection was not closed")
	}
	// The kicked session's close must not log the new session out.
	call[*pb.BagGetResp](t, alice2, &pb.BagGetReq{})

	// Disconnect: the player is saved and unloaded.
	_ = alice2.c.Close()
	deadline := time.Now().Add(3 * time.Second)
	for {
		saved, err := srv.Players.Load(context.Background(), alice.uid)
		if err == nil && saved.Gold == 1000 && saved.PosX == 520 && srv.Game.OnlineCount() == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("player not saved/unloaded: saved=%+v err=%v online=%d", saved, err, srv.Game.OnlineCount())
		}
		time.Sleep(20 * time.Millisecond)
	}
	waitFor(t, bob.pushes, func(m *pb.SceneEntityLeavePush) bool {
		return len(m.Uids) == 1 && m.Uids[0] == alice.uid
	})
}

func TestManyConcurrentPlayers(t *testing.T) {
	srv := startServer(t)
	const n = 50
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c, err := client.DialTCP(srv.Gate.TCPAddr().String(), client.Options{Registry: protocol.Registry()})
			if err != nil {
				errs <- err
				return
			}
			defer c.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if _, err := c.Request(ctx, &pb.LoginReq{Account: fmt.Sprintf("user_%d", i), Password: "secret123"}); err != nil {
				errs <- err
				return
			}
			if _, err := c.Request(ctx, &pb.EnterGameReq{}); err != nil {
				errs <- err
				return
			}
			for step := range 10 {
				target := &pb.Vector2{X: 500 + float32(step), Y: 500}
				if _, err := c.Request(ctx, &pb.SceneMoveReq{Target: target}); err != nil {
					errs <- err
					return
				}
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestShutdownSavesPlayers(t *testing.T) {
	srv := startServer(t)
	p := dial(t, srv, false)
	login(t, p, "carol")
	call[*pb.GmCommandResp](t, p, &pb.GmCommandReq{Command: "addgold 7"})

	if err := srv.App.Stop(); err != nil {
		t.Fatal(err)
	}
	kick := waitFor(t, p.pushes, func(*syspb.KickPush) bool { return true })
	if kick.Reason != syspb.KickReason_KICK_SERVER_SHUTDOWN {
		t.Fatalf("kick reason %v", kick.Reason)
	}
	saved, err := srv.Players.Load(context.Background(), p.uid)
	if err != nil || saved.Gold != 7 {
		t.Fatalf("not saved on shutdown: %+v %v", saved, err)
	}
}
