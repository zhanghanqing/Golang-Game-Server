// Package presence records which game node currently owns each online player.
//
// Rendezvous hashing alone is not enough once game nodes come and go: when a
// node is added (autoscaling) or drained (scale-down, rolling update), the hash
// of an online player may now point to a different node. Without a record of
// the real owner, a reconnect could load the same player on two nodes at once
// and the two copies would overwrite each other's saves.
//
// The directory stores one entry per online player:
//
//	key:   gs:presence:<uid>
//	value: <game node id>
//	ttl:   refreshed by the owner every save tick
//
// Rules:
//
//   - Login routes a player to its recorded owner while that node is alive,
//     and only uses rendezvous hashing for players that are not online.
//   - A game node must Acquire the entry before loading a player and Release
//     it after the final save, so at most one live node owns a player.
//   - An entry pointing to a dead node (not in the registry) may be taken over.
package presence

import (
	"context"
	"strconv"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"gameserver/framework/cluster"
	"gameserver/game/protocol"
)

// DefaultTTL is how long an entry survives without being refreshed.
const DefaultTTL = 5 * time.Minute

// Directory maps online players to their owning game node.
type Directory interface {
	// Owner returns the node owning uid, or "" when the player is not online.
	Owner(ctx context.Context, uid int64) (string, error)
	// Acquire claims uid for node if it is free or already owned by node, and
	// refreshes the ttl. It returns the owner after the call: node on success,
	// or the other node that holds the entry.
	Acquire(ctx context.Context, uid int64, node string) (string, error)
	// Takeover moves the entry from a dead node to node. It succeeds only if
	// the current owner is still from, and returns the owner after the call.
	Takeover(ctx context.Context, uid int64, from, node string) (string, error)
	// Release deletes the entry if node still owns it.
	Release(ctx context.Context, uid int64, node string) error
}

// GameNodeAlive reports whether node is a registered game node, active or draining.
func GameNodeAlive(reg cluster.Registry, node string) bool {
	return cluster.NodeAlive(reg, protocol.ServiceGame, node)
}

// ---------------------------------------------------------------------------
// Memory implementation (all-in-one mode and tests)

type memEntry struct {
	node    string
	expires time.Time
}

// Memory is a process-local Directory.
type Memory struct {
	ttl time.Duration
	mu  sync.Mutex
	m   map[int64]memEntry
}

// NewMemory creates an empty directory. ttl <= 0 means DefaultTTL.
func NewMemory(ttl time.Duration) *Memory {
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	return &Memory{ttl: ttl, m: make(map[int64]memEntry)}
}

// get returns the live owner. Caller holds mu.
func (d *Memory) get(uid int64) string {
	e, ok := d.m[uid]
	if !ok || time.Now().After(e.expires) {
		delete(d.m, uid)
		return ""
	}
	return e.node
}

func (d *Memory) Owner(_ context.Context, uid int64) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.get(uid), nil
}

func (d *Memory) Acquire(_ context.Context, uid int64, node string) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if cur := d.get(uid); cur != "" && cur != node {
		return cur, nil
	}
	d.m[uid] = memEntry{node: node, expires: time.Now().Add(d.ttl)}
	return node, nil
}

func (d *Memory) Takeover(_ context.Context, uid int64, from, node string) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if cur := d.get(uid); cur != "" && cur != from {
		return cur, nil
	}
	d.m[uid] = memEntry{node: node, expires: time.Now().Add(d.ttl)}
	return node, nil
}

func (d *Memory) Release(_ context.Context, uid int64, node string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.get(uid) == node {
		delete(d.m, uid)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Redis implementation (distributed mode)

// Each script is a compare-and-set, so concurrent nodes cannot both win.
var (
	// KEYS[1]=key ARGV[1]=node ARGV[2]=ttl ms
	acquireScript = redis.NewScript(`
local cur = redis.call('GET', KEYS[1])
if cur and cur ~= ARGV[1] then return cur end
redis.call('SET', KEYS[1], ARGV[1], 'PX', ARGV[2])
return ARGV[1]`)
	// KEYS[1]=key ARGV[1]=from ARGV[2]=node ARGV[3]=ttl ms
	takeoverScript = redis.NewScript(`
local cur = redis.call('GET', KEYS[1])
if cur and cur ~= ARGV[1] then return cur end
redis.call('SET', KEYS[1], ARGV[2], 'PX', ARGV[3])
return ARGV[2]`)
	// KEYS[1]=key ARGV[1]=node
	releaseScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then return redis.call('DEL', KEYS[1]) end
return 0`)
)

// Redis stores the directory in Redis.
type Redis struct {
	rdb *redis.Client
	ttl time.Duration
}

// NewRedis creates a Redis-backed directory. ttl <= 0 means DefaultTTL.
func NewRedis(rdb *redis.Client, ttl time.Duration) *Redis {
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	return &Redis{rdb: rdb, ttl: ttl}
}

func presenceKey(uid int64) string { return "gs:presence:" + strconv.FormatInt(uid, 10) }

func (d *Redis) Owner(ctx context.Context, uid int64) (string, error) {
	v, err := d.rdb.Get(ctx, presenceKey(uid)).Result()
	if err == redis.Nil {
		return "", nil
	}
	return v, err
}

func (d *Redis) Acquire(ctx context.Context, uid int64, node string) (string, error) {
	return acquireScript.Run(ctx, d.rdb, []string{presenceKey(uid)}, node, d.ttl.Milliseconds()).Text()
}

func (d *Redis) Takeover(ctx context.Context, uid int64, from, node string) (string, error) {
	return takeoverScript.Run(ctx, d.rdb, []string{presenceKey(uid)}, from, node, d.ttl.Milliseconds()).Text()
}

func (d *Redis) Release(ctx context.Context, uid int64, node string) error {
	return releaseScript.Run(ctx, d.rdb, []string{presenceKey(uid)}, node).Err()
}
