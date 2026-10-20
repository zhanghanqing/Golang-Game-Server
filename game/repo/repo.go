// Package repo is the persistence boundary of the game. Services depend on the
// interfaces below; MongoDB and in-memory implementations are provided. The
// in-memory version lets the whole server run (and be tested) with zero
// external dependencies.
package repo

import (
	"context"
	"errors"

	"gameserver/game/model"
)

var (
	// ErrNotFound means the document does not exist.
	ErrNotFound = errors.New("repo: not found")
	// ErrDuplicate means a document with the same key already exists.
	ErrDuplicate = errors.New("repo: duplicate")
)

// AccountRepo stores login accounts.
type AccountRepo interface {
	Get(ctx context.Context, name string) (*model.Account, error)
	// Create inserts a new account and fails with ErrDuplicate if the name is taken.
	Create(ctx context.Context, acc *model.Account) error
}

// PlayerRepo stores player documents.
type PlayerRepo interface {
	Load(ctx context.Context, uid int64) (*model.Player, error)
	// Save upserts the whole document.
	Save(ctx context.Context, p *model.Player) error
}
