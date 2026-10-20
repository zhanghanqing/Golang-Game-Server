package cluster

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newTestRedis(t *testing.T) *redis.Client {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return rdb
}

// A node that has never read a service must not report it empty when many
// requests ask at once (regression: cold-cache thundering herd).
func TestRedisRegistryColdCacheConcurrentReads(t *testing.T) {
	rdb := newTestRedis(t)
	ctx := context.Background()

	producer := NewRedisRegistry(rdb, 5*time.Second, nil)
	if err := producer.Register(ctx, "game", "game-1"); err != nil {
		t.Fatal(err)
	}
	defer producer.Close()

	reader := NewRedisRegistry(rdb, 5*time.Second, nil)
	defer reader.Close()

	var wg sync.WaitGroup
	empty := make(chan struct{}, 50)
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if len(reader.Nodes("game")) == 0 {
				empty <- struct{}{}
			}
		}()
	}
	wg.Wait()
	if n := len(empty); n > 0 {
		t.Fatalf("%d of 50 concurrent cold reads saw no nodes", n)
	}
}

func TestRedisRegistryDeregisterAndExpiry(t *testing.T) {
	rdb := newTestRedis(t)
	ctx := context.Background()
	r := NewRedisRegistry(rdb, 5*time.Second, nil)
	_ = r.Register(ctx, "game", "game-1")
	_ = r.Register(ctx, "game", "game-2")
	if got := r.Nodes("game"); len(got) != 2 {
		t.Fatalf("got %v", got)
	}
	_ = r.Deregister(ctx, "game", "game-1")
	if got := r.Nodes("game"); len(got) != 1 || got[0] != "game-2" {
		t.Fatalf("after deregister: %v", got)
	}

	// A crashed node: its score is in the past and must be ignored.
	rdb.ZAdd(ctx, registryKey("game"), redis.Z{Score: float64(time.Now().Add(-time.Minute).UnixMilli()), Member: "dead"})
	if err := r.refreshOne(ctx, "game"); err != nil {
		t.Fatal(err)
	}
	for _, n := range r.Nodes("game") {
		if n == "dead" {
			t.Fatal("expired node still listed")
		}
	}
}

// A node moving to the draining key must never look dead to a reader whose
// cache still has the old lists (regression: player loaded on two nodes).
func TestNodeAliveDuringDrainWithStaleCache(t *testing.T) {
	rdb := newTestRedis(t)
	ctx := context.Background()
	node := NewRedisRegistry(rdb, time.Minute, nil)
	defer node.Close()
	if err := node.Register(ctx, "game", "game-1"); err != nil {
		t.Fatal(err)
	}
	reader := NewRedisRegistry(rdb, time.Minute, nil) // background refresh every 20s: stale on purpose
	defer reader.Close()
	if !NodeAlive(reader, "game", "game-1") {
		t.Fatal("registered node reported dead")
	}
	// Warm the draining key while it is still empty.
	if len(reader.Nodes(DrainingService("game"))) != 0 {
		t.Fatal("unexpected draining nodes")
	}

	// Drain: register as draining first, then leave the active key.
	if err := node.Register(ctx, DrainingService("game"), "game-1"); err != nil {
		t.Fatal(err)
	}
	if err := node.Deregister(ctx, "game", "game-1"); err != nil {
		t.Fatal(err)
	}
	// Simulate the reader having refreshed only the active key.
	if err := reader.Refresh(ctx, "game"); err != nil {
		t.Fatal(err)
	}
	if !NodeAlive(reader, "game", "game-1") {
		t.Fatal("draining node reported dead from a stale cache")
	}

	// Really gone: dead as soon as the reader's cache refreshes (at most TTL/3
	// later). "Alive" answers may be briefly stale; that is the safe direction.
	if err := node.Deregister(ctx, DrainingService("game"), "game-1"); err != nil {
		t.Fatal(err)
	}
	if err := reader.Refresh(ctx, "game", DrainingService("game")); err != nil {
		t.Fatal(err)
	}
	if NodeAlive(reader, "game", "game-1") {
		t.Fatal("removed node reported alive")
	}
}
