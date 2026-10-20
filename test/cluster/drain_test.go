package cluster_test

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"testing"
	"time"

	"gameserver/framework/pb/syspb"
	"gameserver/game/bootstrap"
	"gameserver/game/gamesvc"
	"gameserver/game/pb"
	"gameserver/game/protocol"
)

// gameStatus calls the operator endpoint of a game node over HTTP.
func gameStatus(t *testing.T, n *bootstrap.Server, method string) gamesvc.Status {
	t.Helper()
	path := "/game/status"
	if method == http.MethodPost {
		path = "/game/drain"
	}
	req, _ := http.NewRequest(method, "http://"+n.Admin.Addr().String()+path, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s %s: %s", method, path, resp.Status)
	}
	var st gamesvc.Status
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		t.Fatal(err)
	}
	return st
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestScaleOutDrainAndShutdown walks through what the Kubernetes operator does
// to a game node: scale out, drain, then stop. Players must never be loaded on
// two nodes, and must keep their progress when they move.
func TestScaleOutDrainAndShutdown(t *testing.T) {
	e := newEnv(t)
	game1 := e.start(t, "game-1", bootstrap.RoleGame)
	login := e.start(t, "login-1", bootstrap.RoleLogin)
	gate := e.start(t, "gate-1", bootstrap.RoleGate)

	names := []string{"ann", "ben", "cat", "dan", "eve", "fay", "gus", "hal"}
	players := map[string]*conn{}
	for _, name := range names {
		c := connect(t, gate)
		enter(t, c, name)
		players[name] = c
		if node := e.gameNodeOf(c.uid); node != "game-1" {
			t.Fatalf("%s on %q, want game-1", name, node)
		}
	}

	// 1. Scale out. Rendezvous hashing now prefers game-2 for about half of
	// the uids, but online players must stay where their state is.
	e.start(t, "game-2", bootstrap.RoleGame)
	waitFor(t, "login sees game-2", func() bool {
		return len(login.Cluster.Registry().Nodes(protocol.ServiceGame)) == 2
	})
	for _, name := range names {
		old := players[name]
		c := connect(t, gate)
		enter(t, c, name) // re-login while the old session is still online
		waitPush(t, old.pushes, func(k *syspb.KickPush) bool { return k.Reason == syspb.KickReason_KICK_DUPLICATE_LOGIN })
		if node := e.gameNodeOf(c.uid); node != "game-1" {
			t.Fatalf("re-login of online %s moved it to %q", name, node)
		}
		players[name] = c
	}

	// 2. Drain game-1 through the operator endpoint.
	st := gameStatus(t, game1, http.MethodPost)
	if !st.Draining || st.Players != len(names) || st.Node != "game-1" {
		t.Fatalf("drain status %+v", st)
	}
	waitFor(t, "login stops assigning to game-1", func() bool {
		return !slices.Contains(login.Cluster.Registry().Nodes(protocol.ServiceGame), "game-1")
	})
	newbie := connect(t, gate)
	enter(t, newbie, "ivy")
	if node := e.gameNodeOf(newbie.uid); node != "game-2" {
		t.Fatalf("new player on %q while game-1 drains", node)
	}
	// A player already on the draining node still reconnects to it.
	ann := connect(t, gate)
	enter(t, ann, "ann")
	if node := e.gameNodeOf(ann.uid); node != "game-1" {
		t.Fatalf("ann reconnected to %q, want draining game-1", node)
	}
	players["ann"] = ann
	do[*pb.GmCommandResp](t, ann, &pb.GmCommandReq{Command: "addgold 77"})

	// 3. Stop game-1 (what SIGTERM does when the operator deletes the pod).
	if err := game1.App.Stop(); err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		waitPush(t, players[name].pushes, func(k *syspb.KickPush) bool { return k.Reason == syspb.KickReason_KICK_SERVER_SHUTDOWN })
	}
	waitFor(t, "login forgets game-1", func() bool {
		r := login.Cluster.Registry()
		return !slices.Contains(r.Nodes(protocol.ServiceGame), "game-1") &&
			!slices.Contains(r.Nodes(protocol.ServiceGameDraining), "game-1")
	})

	// 4. Everyone reconnects to game-2 with their progress intact.
	for _, name := range names {
		c := connect(t, gate)
		enter(t, c, name)
		if node := e.gameNodeOf(c.uid); node != "game-2" {
			t.Fatalf("%s reconnected to %q", name, node)
		}
		if name == "ann" {
			info := do[*pb.GmCommandResp](t, c, &pb.GmCommandReq{Command: "info"})
			p, err := e.players.Load(context.Background(), c.uid)
			if err != nil || p.Gold < 77 {
				t.Fatalf("ann lost progress: %+v %v (info %q)", p, err, info.Output)
			}
		}
	}
	if st := gameStatus(t, e.nodes["game-2"], http.MethodGet); st.Players != len(names)+1 || st.Draining {
		t.Fatalf("game-2 status %+v", st)
	}
}

// TestGateKicksSessionsOfCrashedNode simulates a game node that dies without
// a graceful stop: its registry entry just disappears. The gate must kick the
// players bound to it so they log in again on a live node.
func TestGateKicksSessionsOfCrashedNode(t *testing.T) {
	e := newEnv(t)
	game1 := e.start(t, "game-1", bootstrap.RoleGame)
	e.start(t, "game-2", bootstrap.RoleGame)
	login := e.start(t, "login-1", bootstrap.RoleLogin)
	gate := e.start(t, "gate-1", bootstrap.RoleGate)
	waitFor(t, "login sees both game nodes", func() bool {
		return len(login.Cluster.Registry().Nodes(protocol.ServiceGame)) == 2
	})

	// Find a player that lands on game-1.
	var victim *conn
	var name string
	for i := range 40 {
		name = "crash" + string(rune('a'+i%26)) + string(rune('a'+i/26))
		c := connect(t, gate)
		enter(t, c, name)
		if e.gameNodeOf(c.uid) == "game-1" {
			victim = c
			break
		}
	}
	if victim == nil {
		t.Fatal("no player hashed to game-1")
	}
	// Let the gate observe game-1 alive at least once.
	time.Sleep(300 * time.Millisecond)

	// Crash: heartbeats stop and the registry entry vanishes.
	_ = game1.Cluster.Registry().Close()
	e.redis.ZRem("gs:registry:game", "game-1")

	kick := waitPush(t, victim.pushes, func(k *syspb.KickPush) bool { return true })
	if kick.Reason != syspb.KickReason_KICK_BACKEND_LOST {
		t.Fatalf("kick reason %v", kick.Reason)
	}
	waitFor(t, "login forgets game-1", func() bool {
		return !slices.Contains(login.Cluster.Registry().Nodes(protocol.ServiceGame), "game-1")
	})
	again := connect(t, gate)
	enter(t, again, name) // presence still names game-1, which is dead: taken over
	waitFor(t, "player on game-2", func() bool {
		for id, n := range e.nodes {
			if id == "game-2" && n.Game.HasPlayer(again.uid) {
				return true
			}
		}
		return false
	})
}
