// Package redisx wraps the go-redis client as an app.Module.
//
// The client is created eagerly (go-redis connects lazily), so other modules
// can receive it at construction time. Init verifies connectivity with PING
// and Stop closes the connection pool.
package redisx

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// Config configures the Redis connection.
type Config struct {
	Addr     string `yaml:"addr"`      // host:port, default 127.0.0.1:6379
	Password string `yaml:"password"`  // optional
	DB       int    `yaml:"db"`        // database index
	PoolSize int    `yaml:"pool_size"` // 0 = go-redis default (10 * GOMAXPROCS)
}

// Module owns a Redis client.
type Module struct {
	client *redis.Client
}

// New creates the module and its client.
func New(cfg Config) *Module {
	if cfg.Addr == "" {
		cfg.Addr = "127.0.0.1:6379"
	}
	return &Module{client: redis.NewClient(&redis.Options{
		Addr:         cfg.Addr,
		Password:     cfg.Password,
		DB:           cfg.DB,
		PoolSize:     cfg.PoolSize,
		DialTimeout:  5 * time.Second,
		ReadTimeout:  3 * time.Second,
		WriteTimeout: 3 * time.Second,
	})}
}

// Client returns the shared client.
func (m *Module) Client() *redis.Client { return m.client }

func (m *Module) Name() string { return "redis" }

func (m *Module) Init(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := m.client.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("redis ping %s: %w", m.client.Options().Addr, err)
	}
	return nil
}

func (m *Module) Start(context.Context) error { return nil }

func (m *Module) Stop(context.Context) error { return m.client.Close() }
