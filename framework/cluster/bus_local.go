package cluster

import (
	"context"
	"sync"
	"sync/atomic"
)

// LocalBus is an in-process Bus used when every service runs in a single
// process (all-in-one mode) and in tests. Handlers are invoked synchronously
// in the publisher's goroutine, which preserves ordering and costs nothing more
// than a function call.
type LocalBus struct {
	mu     sync.RWMutex
	subs   map[string][]*localSub
	rr     sync.Map // queue key -> *atomic.Uint64 (round robin cursor)
	closed atomic.Bool
}

type localSub struct {
	bus     *LocalBus
	subject string
	queue   string
	h       Handler
}

// NewLocalBus creates an empty in-process bus.
func NewLocalBus() *LocalBus {
	return &LocalBus{subs: make(map[string][]*localSub)}
}

func (b *LocalBus) Subscribe(subject, queue string, h Handler) (Subscription, error) {
	if b.closed.Load() {
		return nil, ErrBusClosed
	}
	s := &localSub{bus: b, subject: subject, queue: queue, h: h}
	b.mu.Lock()
	b.subs[subject] = append(b.subs[subject], s)
	b.mu.Unlock()
	return s, nil
}

func (s *localSub) Unsubscribe() error {
	b := s.bus
	b.mu.Lock()
	defer b.mu.Unlock()
	list := b.subs[s.subject]
	for i, x := range list {
		if x == s {
			b.subs[s.subject] = append(list[:i:i], list[i+1:]...)
			break
		}
	}
	return nil
}

// targets selects the subscribers that receive one message: all plain
// subscribers plus one member of each queue group (round robin).
func (b *LocalBus) targets(subject string) []*localSub {
	b.mu.RLock()
	list := b.subs[subject]
	b.mu.RUnlock()
	if len(list) == 0 {
		return nil
	}
	var out []*localSub
	groups := map[string][]*localSub{}
	for _, s := range list {
		if s.queue == "" {
			out = append(out, s)
		} else {
			groups[s.queue] = append(groups[s.queue], s)
		}
	}
	for q, members := range groups {
		v, _ := b.rr.LoadOrStore(subject+"|"+q, new(atomic.Uint64))
		n := v.(*atomic.Uint64).Add(1)
		out = append(out, members[n%uint64(len(members))])
	}
	return out
}

func (b *LocalBus) Publish(subject string, data []byte) error {
	if b.closed.Load() {
		return ErrBusClosed
	}
	for _, s := range b.targets(subject) {
		s.h(&Msg{Subject: subject, Data: data})
	}
	return nil
}

func (b *LocalBus) Request(ctx context.Context, subject string, data []byte) ([]byte, error) {
	if b.closed.Load() {
		return nil, ErrBusClosed
	}
	targets := b.targets(subject)
	if len(targets) == 0 {
		return nil, ErrNoResponders
	}
	reply := make(chan []byte, 1)
	respond := func(d []byte) error {
		select {
		case reply <- d:
		default: // only the first response counts
		}
		return nil
	}
	for _, s := range targets {
		s.h(&Msg{Subject: subject, Data: data, respond: respond})
	}
	select {
	case d := <-reply:
		return d, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (b *LocalBus) Close() error {
	b.closed.Store(true)
	return nil
}
