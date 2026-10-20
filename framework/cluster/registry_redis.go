package cluster

import (
	"context"
	"log/slog"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisRegistry stores registrations in Redis sorted sets:
//
//	key:    gs:registry:<service>
//	member: <nodeID>
//	score:  expiry time in unix milliseconds
//
// Every node refreshes its own members every TTL/3. A member whose score is in
// the past belongs to a crashed node and is ignored (and eventually removed).
// Readers keep a local cache refreshed on the same period, so Nodes() never
// touches the network on the hot path.
type RedisRegistry struct {
	rdb    *redis.Client
	ttl    time.Duration
	logger *slog.Logger

	mu      sync.RWMutex
	own     map[[2]string]struct{} // (service, nodeID) registered by this process
	cache   map[string][]string    // service -> alive nodes
	watched map[string]struct{}    // services read at least once

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewRedisRegistry creates a registry. ttl defaults to 10s.
func NewRedisRegistry(rdb *redis.Client, ttl time.Duration, logger *slog.Logger) *RedisRegistry {
	if ttl <= 0 {
		ttl = 10 * time.Second
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &RedisRegistry{
		rdb:     rdb,
		ttl:     ttl,
		logger:  logger,
		own:     make(map[[2]string]struct{}),
		cache:   make(map[string][]string),
		watched: make(map[string]struct{}),
	}
}

func registryKey(service string) string { return "gs:registry:" + service }

func (r *RedisRegistry) Register(ctx context.Context, service, nodeID string) error {
	r.mu.Lock()
	r.own[[2]string{service, nodeID}] = struct{}{}
	r.watched[service] = struct{}{}
	r.mu.Unlock()
	if err := r.heartbeatOne(ctx, service, nodeID); err != nil {
		return err
	}
	return r.refreshOne(ctx, service)
}

func (r *RedisRegistry) Deregister(ctx context.Context, service, nodeID string) error {
	r.mu.Lock()
	delete(r.own, [2]string{service, nodeID})
	r.mu.Unlock()
	if err := r.rdb.ZRem(ctx, registryKey(service), nodeID).Err(); err != nil {
		return err
	}
	return r.refreshOne(ctx, service)
}

func (r *RedisRegistry) Nodes(service string) []string {
	r.mu.RLock()
	nodes, ok := r.cache[service]
	r.mu.RUnlock()
	if ok {
		return append([]string(nil), nodes...)
	}
	// Cold cache: fetch synchronously and keep the service fresh afterwards.
	// Every concurrent caller fetches until the cache is filled; returning an
	// empty list here would wrongly report the service as unavailable.
	r.mu.Lock()
	r.watched[service] = struct{}{}
	r.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := r.refreshOne(ctx, service); err != nil {
		r.logger.Warn("registry refresh failed", "service", service, "err", err)
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]string(nil), r.cache[service]...)
}

func (r *RedisRegistry) Start(context.Context) error {
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		ticker := time.NewTicker(r.ttl / 3)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				r.tick(ctx)
			}
		}
	}()
	return nil
}

func (r *RedisRegistry) Close() error {
	if r.cancel != nil {
		r.cancel()
	}
	r.wg.Wait()
	// Remove our registrations so peers stop routing to us immediately.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	r.mu.RLock()
	own := make([][2]string, 0, len(r.own))
	for k := range r.own {
		own = append(own, k)
	}
	r.mu.RUnlock()
	for _, k := range own {
		_ = r.rdb.ZRem(ctx, registryKey(k[0]), k[1]).Err()
	}
	return nil
}

func (r *RedisRegistry) tick(ctx context.Context) {
	r.mu.RLock()
	own := make([][2]string, 0, len(r.own))
	for k := range r.own {
		own = append(own, k)
	}
	services := make([]string, 0, len(r.watched))
	for s := range r.watched {
		services = append(services, s)
	}
	r.mu.RUnlock()

	for _, k := range own {
		if err := r.heartbeatOne(ctx, k[0], k[1]); err != nil {
			r.logger.Warn("registry heartbeat failed", "service", k[0], "node", k[1], "err", err)
		}
	}
	for _, s := range services {
		if err := r.refreshOne(ctx, s); err != nil {
			r.logger.Warn("registry refresh failed", "service", s, "err", err)
		}
	}
}

func (r *RedisRegistry) heartbeatOne(ctx context.Context, service, nodeID string) error {
	expiry := time.Now().Add(r.ttl).UnixMilli()
	return r.rdb.ZAdd(ctx, registryKey(service), redis.Z{Score: float64(expiry), Member: nodeID}).Err()
}

func (r *RedisRegistry) refreshOne(ctx context.Context, service string) error {
	now := strconv.FormatInt(time.Now().UnixMilli(), 10)
	key := registryKey(service)
	pipe := r.rdb.Pipeline()
	pipe.ZRemRangeByScore(ctx, key, "-inf", "("+now) // garbage-collect dead nodes
	alive := pipe.ZRangeByScore(ctx, key, &redis.ZRangeBy{Min: now, Max: "+inf"})
	if _, err := pipe.Exec(ctx); err != nil {
		return err
	}
	nodes := alive.Val()
	sort.Strings(nodes)
	r.mu.Lock()
	r.cache[service] = nodes
	r.mu.Unlock()
	return nil
}
