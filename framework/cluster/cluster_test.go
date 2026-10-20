package cluster

import (
	"context"
	"testing"
	"time"
)

func TestLocalBusFanOutAndQueue(t *testing.T) {
	b := NewLocalBus()
	var fan, q1, q2 int
	_, _ = b.Subscribe("s", "", func(*Msg) { fan++ })
	_, _ = b.Subscribe("s", "g", func(*Msg) { q1++ })
	_, _ = b.Subscribe("s", "g", func(*Msg) { q2++ })
	for range 10 {
		_ = b.Publish("s", nil)
	}
	if fan != 10 {
		t.Fatalf("fan-out subscriber got %d, want 10", fan)
	}
	if q1+q2 != 10 || q1 == 0 || q2 == 0 {
		t.Fatalf("queue group not balanced: %d/%d", q1, q2)
	}
}

func TestLocalBusRequest(t *testing.T) {
	b := NewLocalBus()
	_, _ = b.Subscribe("echo", "", func(m *Msg) { _ = m.Respond(append([]byte("re:"), m.Data...)) })
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got, err := b.Request(ctx, "echo", []byte("x"))
	if err != nil || string(got) != "re:x" {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := b.Request(ctx, "nobody", nil); err != ErrNoResponders {
		t.Fatalf("expected ErrNoResponders, got %v", err)
	}
}

func TestUnsubscribe(t *testing.T) {
	b := NewLocalBus()
	n := 0
	sub, _ := b.Subscribe("s", "", func(*Msg) { n++ })
	_ = b.Publish("s", nil)
	_ = sub.Unsubscribe()
	_ = b.Publish("s", nil)
	if n != 1 {
		t.Fatalf("got %d deliveries, want 1", n)
	}
}

func TestPickByKeyIsStableAndBalanced(t *testing.T) {
	nodes := []string{"game-1", "game-2", "game-3"}
	counts := map[string]int{}
	for k := range uint64(30000) {
		n := PickByKey(nodes, k)
		if n != PickByKey(nodes, k) {
			t.Fatal("not deterministic")
		}
		counts[n]++
	}
	for _, n := range nodes {
		if counts[n] < 8000 || counts[n] > 12000 {
			t.Fatalf("unbalanced distribution: %v", counts)
		}
	}
	// Removing a node only moves the keys that lived on it.
	moved := 0
	for k := range uint64(30000) {
		before := PickByKey(nodes, k)
		after := PickByKey(nodes[:2], k)
		if before != "game-3" && before != after {
			moved++
		}
	}
	if moved != 0 {
		t.Fatalf("%d keys moved unnecessarily", moved)
	}
	if PickByKey(nil, 1) != "" {
		t.Fatal("empty node list must return empty string")
	}
}

func TestValidateToken(t *testing.T) {
	for _, ok := range []string{"game-1", "gate_2"} {
		if err := ValidateToken(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "a.b", "a b", "a*"} {
		if ValidateToken(bad) == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
}

func TestMemoryRegistry(t *testing.T) {
	r := NewMemoryRegistry()
	ctx := context.Background()
	_ = r.Register(ctx, "game", "b")
	_ = r.Register(ctx, "game", "a")
	if got := r.Nodes("game"); len(got) != 2 || got[0] != "a" {
		t.Fatalf("got %v", got)
	}
	_ = r.Deregister(ctx, "game", "a")
	if got := r.Nodes("game"); len(got) != 1 || got[0] != "b" {
		t.Fatalf("got %v", got)
	}
}
