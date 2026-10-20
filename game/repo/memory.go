package repo

import (
	"context"
	"sync"

	"gameserver/game/model"
)

// MemoryAccounts is an in-memory AccountRepo.
type MemoryAccounts struct {
	mu   sync.Mutex
	data map[string]model.Account
}

// NewMemoryAccounts creates an empty repository.
func NewMemoryAccounts() *MemoryAccounts {
	return &MemoryAccounts{data: make(map[string]model.Account)}
}

func (r *MemoryAccounts) Get(_ context.Context, name string) (*model.Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	acc, ok := r.data[name]
	if !ok {
		return nil, ErrNotFound
	}
	return &acc, nil
}

func (r *MemoryAccounts) Create(_ context.Context, acc *model.Account) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.data[acc.Name]; ok {
		return ErrDuplicate
	}
	r.data[acc.Name] = *acc
	return nil
}

// MemoryPlayers is an in-memory PlayerRepo. It stores deep copies so callers
// can never mutate "persisted" state by accident, like a real database.
type MemoryPlayers struct {
	mu   sync.Mutex
	data map[int64]*model.Player
}

// NewMemoryPlayers creates an empty repository.
func NewMemoryPlayers() *MemoryPlayers {
	return &MemoryPlayers{data: make(map[int64]*model.Player)}
}

func (r *MemoryPlayers) Load(_ context.Context, uid int64) (*model.Player, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.data[uid]
	if !ok {
		return nil, ErrNotFound
	}
	return p.Clone(), nil
}

func (r *MemoryPlayers) Save(_ context.Context, p *model.Player) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.data[p.UID] = p.Clone()
	return nil
}
