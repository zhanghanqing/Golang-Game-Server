// Package mongox wraps the official MongoDB driver as an app.Module.
//
// MongoDB suits game data well: a player is naturally one document (base
// attributes, bag, quests...), schema changes need no migrations, and
// upserts make "save the whole player" a single round trip.
package mongox

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"
)

// Config configures the MongoDB connection.
type Config struct {
	URI      string `yaml:"uri"`       // e.g. mongodb://127.0.0.1:27017
	Database string `yaml:"database"`  // database name, default "game"
	PoolSize uint64 `yaml:"pool_size"` // max connections, 0 = driver default (100)
}

// Module owns a Mongo client.
type Module struct {
	cfg    Config
	client *mongo.Client
	db     *mongo.Database
}

// New creates the module. The driver connects lazily; Init verifies connectivity.
func New(cfg Config) (*Module, error) {
	if cfg.URI == "" {
		cfg.URI = "mongodb://127.0.0.1:27017"
	}
	if cfg.Database == "" {
		cfg.Database = "game"
	}
	opts := options.Client().ApplyURI(cfg.URI).SetConnectTimeout(5 * time.Second)
	if cfg.PoolSize > 0 {
		opts.SetMaxPoolSize(cfg.PoolSize)
	}
	client, err := mongo.Connect(opts)
	if err != nil {
		return nil, fmt.Errorf("mongo connect: %w", err)
	}
	return &Module{cfg: cfg, client: client, db: client.Database(cfg.Database)}, nil
}

// DB returns the configured database handle.
func (m *Module) DB() *mongo.Database { return m.db }

func (m *Module) Name() string { return "mongo" }

func (m *Module) Init(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := m.client.Ping(ctx, readpref.Primary()); err != nil {
		return fmt.Errorf("mongo ping %s: %w", m.cfg.URI, err)
	}
	return nil
}

func (m *Module) Start(context.Context) error { return nil }

func (m *Module) Stop(ctx context.Context) error { return m.client.Disconnect(ctx) }

// IsDuplicateKey reports whether err is a unique index violation.
func IsDuplicateKey(err error) bool { return mongo.IsDuplicateKeyError(err) }

// IsNotFound reports whether err means "no document".
func IsNotFound(err error) bool { return errors.Is(err, mongo.ErrNoDocuments) }
