package presence

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func directories(t *testing.T) map[string]Directory {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return map[string]Directory{
		"memory": NewMemory(time.Minute),
		"redis":  NewRedis(rdb, time.Minute),
	}
}

func TestOwnershipRules(t *testing.T) {
	ctx := context.Background()
	for name, d := range directories(t) {
		t.Run(name, func(t *testing.T) {
			expect := func(want string) func(string, error) {
				return func(got string, err error) {
					t.Helper()
					if err != nil || got != want {
						t.Fatalf("got (%q, %v), want %q", got, err, want)
					}
				}
			}
			expect("")(d.Owner(ctx, 1))
			expect("game-a")(d.Acquire(ctx, 1, "game-a"))
			expect("game-a")(d.Owner(ctx, 1))
			// Re-acquire by the owner refreshes; another node is refused.
			expect("game-a")(d.Acquire(ctx, 1, "game-a"))
			expect("game-a")(d.Acquire(ctx, 1, "game-b"))
			// Takeover only works against the expected previous owner.
			expect("game-a")(d.Takeover(ctx, 1, "game-x", "game-b"))
			expect("game-b")(d.Takeover(ctx, 1, "game-a", "game-b"))
			// Release by a non-owner is ignored.
			if err := d.Release(ctx, 1, "game-a"); err != nil {
				t.Fatal(err)
			}
			expect("game-b")(d.Owner(ctx, 1))
			if err := d.Release(ctx, 1, "game-b"); err != nil {
				t.Fatal(err)
			}
			expect("")(d.Owner(ctx, 1))
		})
	}
}

func TestConcurrentAcquireHasOneWinner(t *testing.T) {
	ctx := context.Background()
	for name, d := range directories(t) {
		t.Run(name, func(t *testing.T) {
			nodes := []string{"g1", "g2", "g3", "g4", "g5", "g6", "g7", "g8"}
			results := make([]string, len(nodes))
			var wg sync.WaitGroup
			for i, n := range nodes {
				wg.Go(func() {
					owner, err := d.Acquire(ctx, 42, n)
					if err != nil {
						t.Error(err)
					}
					results[i] = owner
				})
			}
			wg.Wait()
			winner, _ := d.Owner(ctx, 42)
			for i, r := range results {
				if r != winner {
					t.Fatalf("node %s saw owner %q, final owner %q", nodes[i], r, winner)
				}
			}
		})
	}
}
