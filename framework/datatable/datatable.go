// Package datatable loads static game configuration tables, such as
// item, skill or monster definitions exported by designers.
//
// A table is a JSON array of rows. Each row is decoded into a Go struct and
// indexed by a key. Tables are immutable after loading, so they can be read
// from any goroutine without locks. To hot-reload, load a new table and swap
// the pointer atomically (see Holder).
package datatable

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"sort"
	"sync/atomic"
)

// Table is an immutable keyed collection of rows.
type Table[K comparable, V any] struct {
	rows  map[K]*V
	order []K
}

// LoadJSON reads path from fsys, decodes a JSON array of V and indexes it with key.
// Duplicate keys are rejected.
func LoadJSON[K comparable, V any](fsys fs.FS, path string, key func(*V) K) (*Table[K, V], error) {
	raw, err := fs.ReadFile(fsys, path)
	if err != nil {
		return nil, fmt.Errorf("datatable: read %s: %w", path, err)
	}
	return ParseJSON(raw, key)
}

// ParseJSON decodes raw (a JSON array of V) into a table.
func ParseJSON[K comparable, V any](raw []byte, key func(*V) K) (*Table[K, V], error) {
	var list []V
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("datatable: decode: %w", err)
	}
	t := &Table[K, V]{rows: make(map[K]*V, len(list)), order: make([]K, 0, len(list))}
	for i := range list {
		row := &list[i]
		k := key(row)
		if _, dup := t.rows[k]; dup {
			return nil, fmt.Errorf("datatable: duplicate key %v", k)
		}
		t.rows[k] = row
		t.order = append(t.order, k)
	}
	return t, nil
}

// Get returns the row for k. Callers must not modify the returned row.
func (t *Table[K, V]) Get(k K) (*V, bool) {
	v, ok := t.rows[k]
	return v, ok
}

// Len returns the number of rows.
func (t *Table[K, V]) Len() int { return len(t.rows) }

// Keys returns keys in file order.
func (t *Table[K, V]) Keys() []K { return append([]K(nil), t.order...) }

// Each calls fn for every row in file order.
func (t *Table[K, V]) Each(fn func(K, *V)) {
	for _, k := range t.order {
		fn(k, t.rows[k])
	}
}

// SortedKeys returns keys sorted with less (handy for deterministic output).
func (t *Table[K, V]) SortedKeys(less func(a, b K) bool) []K {
	ks := t.Keys()
	sort.Slice(ks, func(i, j int) bool { return less(ks[i], ks[j]) })
	return ks
}

// Holder allows atomic hot-swapping of a table while readers keep using the old one.
type Holder[K comparable, V any] struct {
	p atomic.Pointer[Table[K, V]]
}

// Load returns the current table.
func (h *Holder[K, V]) Load() *Table[K, V] { return h.p.Load() }

// Store replaces the current table.
func (h *Holder[K, V]) Store(t *Table[K, V]) { h.p.Store(t) }
