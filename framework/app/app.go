// Package app manages the lifecycle of a server process.
//
// A process is a list of Modules. The App initializes and starts them in the
// order they were added, waits for a shutdown signal and stops them in reverse
// order. This gives every component a predictable place to acquire and release
// resources, and makes graceful shutdown (saving players, draining sessions)
// the default behaviour.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// Module is a unit of the server with an explicit lifecycle.
//
//   - Init prepares resources (connect to databases, validate configuration).
//     It must not start serving traffic.
//   - Start begins serving (listen on sockets, subscribe to the bus). It must not block.
//   - Stop releases resources. It is called in reverse order and must honour ctx's deadline.
type Module interface {
	Name() string
	Init(ctx context.Context) error
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
}

// Options tune the App.
type Options struct {
	// StopTimeout bounds the total time spent in Stop calls. Default: 30s.
	StopTimeout time.Duration
	// Logger used for lifecycle messages. Default: slog.Default().
	Logger *slog.Logger
}

// App runs a list of modules.
type App struct {
	opts    Options
	modules []Module
	started []Module
}

// New creates an App.
func New(opts Options) *App {
	if opts.StopTimeout <= 0 {
		opts.StopTimeout = 30 * time.Second
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &App{opts: opts}
}

// Add appends modules. Order matters: dependencies must be added first.
func (a *App) Add(m ...Module) {
	a.modules = append(a.modules, m...)
}

// Start initializes and starts every module. On failure the already-started
// modules are stopped and the error is returned.
func (a *App) Start(ctx context.Context) error {
	for _, m := range a.modules {
		a.opts.Logger.Info("module init", "module", m.Name())
		if err := m.Init(ctx); err != nil {
			return fmt.Errorf("init %s: %w", m.Name(), err)
		}
	}
	for _, m := range a.modules {
		a.opts.Logger.Info("module start", "module", m.Name())
		if err := m.Start(ctx); err != nil {
			stopErr := a.Stop()
			return errors.Join(fmt.Errorf("start %s: %w", m.Name(), err), stopErr)
		}
		a.started = append(a.started, m)
	}
	return nil
}

// Stop stops started modules in reverse order within StopTimeout.
func (a *App) Stop() error {
	ctx, cancel := context.WithTimeout(context.Background(), a.opts.StopTimeout)
	defer cancel()
	var errs []error
	for i := len(a.started) - 1; i >= 0; i-- {
		m := a.started[i]
		a.opts.Logger.Info("module stop", "module", m.Name())
		if err := m.Stop(ctx); err != nil {
			a.opts.Logger.Error("module stop failed", "module", m.Name(), "err", err)
			errs = append(errs, fmt.Errorf("stop %s: %w", m.Name(), err))
		}
	}
	a.started = nil
	return errors.Join(errs...)
}

// Run starts the app, blocks until ctx is cancelled or SIGINT/SIGTERM arrives,
// then stops the app gracefully.
func (a *App) Run(ctx context.Context) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := a.Start(ctx); err != nil {
		return err
	}
	a.opts.Logger.Info("server started")
	<-ctx.Done()
	a.opts.Logger.Info("shutdown requested")
	return a.Stop()
}

// Func adapts plain functions into a Module. Nil functions are treated as no-ops.
type Func struct {
	ModuleName string
	OnInit     func(ctx context.Context) error
	OnStart    func(ctx context.Context) error
	OnStop     func(ctx context.Context) error
}

func (f Func) Name() string { return f.ModuleName }

func (f Func) Init(ctx context.Context) error {
	if f.OnInit == nil {
		return nil
	}
	return f.OnInit(ctx)
}

func (f Func) Start(ctx context.Context) error {
	if f.OnStart == nil {
		return nil
	}
	return f.OnStart(ctx)
}

func (f Func) Stop(ctx context.Context) error {
	if f.OnStop == nil {
		return nil
	}
	return f.OnStop(ctx)
}
