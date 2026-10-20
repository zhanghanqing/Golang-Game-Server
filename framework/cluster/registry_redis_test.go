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
