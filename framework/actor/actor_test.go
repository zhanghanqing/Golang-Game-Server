package actor

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestActorRunsInOrder(t *testing.T) {
	a := New("t", nil)
	defer a.Stop()
	var got []int
	for i := range 1000 {
		a.Post(func() { got = append(got, i) }) // no lock needed: single goroutine
	}
	if err := a.Call(context.Background(), func() {}); err != nil {
		t.Fatal(err)
	}
	for i, v := range got {
		if v != i {
			t.Fatalf("out of order at %d: %d", i, v)
		}
	}
}

func TestActorStopDrainsQueue(t *testing.T) {
	a := New("t", nil)
	var n atomic.Int32
	for range 100 {
		a.Post(func() { n.Add(1) })
	}
	a.Stop()
	<-a.Done()
	if n.Load() != 100 {
		t.Fatalf("ran %d of 100", n.Load())
	}
	if a.Post(func() {}) {
		t.Fatal("post after stop should fail")
	}
	if err := a.Call(context.Background(), func() {}); err != ErrStopped {
		t.Fatalf("expected ErrStopped, got %v", err)
	}
}

func TestActorRecoversPanic(t *testing.T) {
	a := New("t", nil)
	defer a.Stop()
	a.Post(func() { panic("boom") })
	ok := false
	if err := a.Call(context.Background(), func() { ok = true }); err != nil || !ok {
		t.Fatal("actor died after panic")
	}
}

func TestActorTimers(t *testing.T) {
	a := New("t", nil)
	defer a.Stop()
	fired := make(chan struct{})
	a.After(10*time.Millisecond, func() { close(fired) })
	select {
	case <-fired:
	case <-time.After(time.Second):
		t.Fatal("After did not fire")
	}

	var ticks atomic.Int32
	timer := a.Every(5*time.Millisecond, func() { ticks.Add(1) })
	time.Sleep(60 * time.Millisecond)
	timer.Stop()
	if ticks.Load() < 3 {
		t.Fatalf("Every fired only %d times", ticks.Load())
	}
}

func TestSelfPostDoesNotDeadlock(t *testing.T) {
	a := New("t", nil)
	defer a.Stop()
	done := make(chan struct{})
	var depth int
	var step func()
	step = func() {
		depth++
		if depth == 10000 {
			close(done)
			return
		}
		a.Post(step)
	}
	a.Post(step)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("self-posting stalled")
	}
}
