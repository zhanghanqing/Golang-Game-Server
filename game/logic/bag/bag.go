// Package bag implements the inventory container. It is pure logic with no
// I/O, so it is trivially unit-testable and reusable by any service.
// It is NOT safe for concurrent use: it is owned by a player actor.
package bag

import (
	"errors"
	"sort"
)

// ErrNotEnough is returned when removing more than the bag holds.
var ErrNotEnough = errors.New("bag: not enough items")

// ErrInvalidCount is returned for non-positive counts.
var ErrInvalidCount = errors.New("bag: count must be positive")

// Stack is a quantity of one item.
type Stack struct {
	ID    int32
	Count int64
}

// Bag maps item id to count.
type Bag struct {
	items map[int32]int64
}

// New creates a bag from stored stacks.
func New(stacks []Stack) *Bag {
	b := &Bag{items: make(map[int32]int64, len(stacks))}
	for _, s := range stacks {
		if s.Count > 0 {
			b.items[s.ID] += s.Count
		}
	}
	return b
}

// Count returns how many of id the bag holds.
func (b *Bag) Count(id int32) int64 { return b.items[id] }

// Add adds n of id and returns the new count.
func (b *Bag) Add(id int32, n int64) (int64, error) {
	if n <= 0 {
		return b.items[id], ErrInvalidCount
	}
	b.items[id] += n
	return b.items[id], nil
}

// Remove removes n of id atomically (all or nothing) and returns the new count.
func (b *Bag) Remove(id int32, n int64) (int64, error) {
	if n <= 0 {
		return b.items[id], ErrInvalidCount
	}
	have := b.items[id]
	if have < n {
		return have, ErrNotEnough
	}
	left := have - n
	if left == 0 {
		delete(b.items, id)
	} else {
		b.items[id] = left
	}
	return left, nil
}

// Stacks returns every stack sorted by id (deterministic for clients and storage).
func (b *Bag) Stacks() []Stack {
	out := make([]Stack, 0, len(b.items))
	for id, n := range b.items {
		out = append(out, Stack{ID: id, Count: n})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
