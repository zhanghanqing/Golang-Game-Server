package cluster

import (
	"context"
	"hash/fnv"
	"sort"
	"sync"
)

// Registry tracks which nodes currently provide which service.
// It is used for stateful routing, e.g. "which game node owns player 42?".
type Registry interface {
	// Register announces that nodeID provides service. Implementations keep the
	// registration alive (heartbeat) until Deregister or Close.
	Register(ctx context.Context, service, nodeID string) error
	// Deregister removes the announcement.
	Deregister(ctx context.Context, service, nodeID string) error
	// Nodes returns the alive node ids of a service, sorted.
	Nodes(service string) []string
	// Start begins background work (heartbeats, cache refresh).
	Start(ctx context.Context) error
	// Close stops background work.
	Close() error
}

// MemoryRegistry is a process-local Registry for all-in-one mode and tests.
type MemoryRegistry struct {
	mu    sync.RWMutex
	nodes map[string]map[string]struct{}
}

// NewMemoryRegistry creates an empty registry.
func NewMemoryRegistry() *MemoryRegistry {
	return &MemoryRegistry{nodes: make(map[string]map[string]struct{})}
}

func (r *MemoryRegistry) Register(_ context.Context, service, nodeID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.nodes[service] == nil {
		r.nodes[service] = make(map[string]struct{})
	}
	r.nodes[service][nodeID] = struct{}{}
	return nil
}

func (r *MemoryRegistry) Deregister(_ context.Context, service, nodeID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.nodes[service], nodeID)
	return nil
}

func (r *MemoryRegistry) Nodes(service string) []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.nodes[service]))
	for id := range r.nodes[service] {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func (r *MemoryRegistry) Start(context.Context) error { return nil }
func (r *MemoryRegistry) Close() error                { return nil }

// PickByKey chooses one node for key using rendezvous (highest random weight)
// hashing. The same key maps to the same node as long as that node is alive,
// and when nodes join or leave only ~1/N of the keys move. It returns "" if
// nodes is empty.
func PickByKey(nodes []string, key uint64) string {
	var (
		best      string
		bestScore uint64
	)
	var kb [8]byte
	for i := range 8 {
		kb[i] = byte(key >> (8 * i))
	}
	for _, n := range nodes {
		h := fnv.New64a()
		_, _ = h.Write([]byte(n))
		_, _ = h.Write(kb[:])
		score := mix64(h.Sum64())
		if best == "" || score > bestScore {
			best, bestScore = n, score
		}
	}
	return best
}

// mix64 is the splitmix64 finalizer; it spreads fnv output bits evenly.
func mix64(x uint64) uint64 {
	x ^= x >> 30
	x *= 0xbf58476d1ce4e5b9
	x ^= x >> 27
	x *= 0x94d049bb133111eb
	x ^= x >> 31
	return x
}
