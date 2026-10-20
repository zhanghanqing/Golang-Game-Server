// Package actor provides a minimal actor: a goroutine that executes posted
// functions one at a time.
//
// Game state (a player, a scene, a guild) is owned by exactly one actor and is
// only touched from inside that actor's goroutine. This removes locks from game
// logic entirely: handlers read and write state freely, and cross-actor
// interaction happens by posting functions to the other actor.
//
// The mailbox is unbounded, so Post never blocks and an actor can safely post
// to itself or to a peer that is posting back (no deadlocks from full queues).
package actor

import (
	"context"
	"errors"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"
)

// ErrStopped is returned when posting to a stopped actor.
var ErrStopped = errors.New("actor: stopped")

// Actor serially executes functions in its own goroutine.
type Actor struct {
	name   string
	logger *slog.Logger

	mu      sync.Mutex
	queue   []func()
	stopped bool
	signal  chan struct{} // capacity 1: "queue is not empty"
	done    chan struct{} // closed when the loop exits
}

// New creates and starts an actor.
func New(name string, logger *slog.Logger) *Actor {
	if logger == nil {
		logger = slog.Default()
	}
	a := &Actor{
		name:   name,
		logger: logger,
		signal: make(chan struct{}, 1),
		done:   make(chan struct{}),
	}
	go a.loop()
	return a
}

// Name returns the actor's name (used in logs).
func (a *Actor) Name() string { return a.name }

// Post enqueues fn. It returns false if the actor is stopped (fn will not run).
func (a *Actor) Post(fn func()) bool {
	a.mu.Lock()
	if a.stopped {
		a.mu.Unlock()
		return false
	}
	a.queue = append(a.queue, fn)
	a.mu.Unlock()
	select {
	case a.signal <- struct{}{}:
	default:
	}
	return true
}

// Call posts fn and waits until it has run or ctx is done.
// Never call it from inside the same actor: that would deadlock.
func (a *Actor) Call(ctx context.Context, fn func()) error {
	finished := make(chan struct{})
	if !a.Post(func() { defer close(finished); fn() }) {
		return ErrStopped
	}
	select {
	case <-finished:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Stop rejects new posts, lets already queued functions run, then exits the loop.
// It is non-blocking; use Done to wait.
func (a *Actor) Stop() {
	a.mu.Lock()
	if a.stopped {
		a.mu.Unlock()
		return
	}
	a.stopped = true
	a.mu.Unlock()
	select {
	case a.signal <- struct{}{}:
	default:
	}
}

// Done is closed after the actor has exited.
func (a *Actor) Done() <-chan struct{} { return a.done }

// Stopped reports whether Stop was called.
func (a *Actor) Stopped() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.stopped
}

func (a *Actor) loop() {
	defer close(a.done)
	var batch []func()
	for range a.signal {
		for {
			a.mu.Lock()
			batch, a.queue = a.queue, batch[:0]
			stopped := a.stopped
			a.mu.Unlock()

			if len(batch) == 0 {
				if stopped {
					return
				}
				break
			}
			for i, fn := range batch {
				a.run(fn)
				batch[i] = nil // release references for GC
			}
		}
	}
}

func (a *Actor) run(fn func()) {
	defer func() {
		if r := recover(); r != nil {
			// A bug in one message must not kill the actor (or the process).
			a.logger.Error("actor panic recovered", "actor", a.name, "panic", r, "stack", string(debug.Stack()))
		}
	}()
	fn()
}

// Timer is a cancellable scheduled function that runs inside an actor.
type Timer struct {
	stop func() bool
}

// Stop cancels the timer. It is safe to call on a nil Timer.
func (t *Timer) Stop() {
	if t != nil && t.stop != nil {
		t.stop()
	}
}

// After runs fn inside the actor once, after d.
func (a *Actor) After(d time.Duration, fn func()) *Timer {
	t := time.AfterFunc(d, func() { a.Post(fn) })
	return &Timer{stop: t.Stop}
}

// Every runs fn inside the actor every d until the timer or the actor is stopped.
func (a *Actor) Every(d time.Duration, fn func()) *Timer {
	ticker := time.NewTicker(d)
	quit := make(chan struct{})
	var once sync.Once
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if !a.Post(fn) {
					return
				}
			case <-quit:
				return
			case <-a.done:
				return
			}
		}
	}()
	return &Timer{stop: func() bool { once.Do(func() { close(quit) }); return true }}
}
