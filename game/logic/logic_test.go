// Package logic_test covers the pure gameplay rules.
package logic_test

import (
	"slices"
	"testing"

	"gameserver/game/logic/aoi"
	"gameserver/game/logic/bag"
	"gameserver/game/logic/levelup"
)

func TestBag(t *testing.T) {
	b := bag.New([]bag.Stack{{ID: 1, Count: 2}, {ID: 1, Count: 3}, {ID: 2, Count: 0}})
	if b.Count(1) != 5 || b.Count(2) != 0 {
		t.Fatalf("bad initial counts")
	}
	if _, err := b.Remove(1, 6); err != bag.ErrNotEnough {
		t.Fatalf("expected ErrNotEnough, got %v", err)
	}
	if left, err := b.Remove(1, 5); err != nil || left != 0 {
		t.Fatalf("remove all: %d %v", left, err)
	}
	if _, err := b.Add(3, -1); err != bag.ErrInvalidCount {
		t.Fatalf("expected ErrInvalidCount, got %v", err)
	}
	_, _ = b.Add(9, 1)
	_, _ = b.Add(4, 1)
	got := b.Stacks()
	if len(got) != 2 || got[0].ID != 4 || got[1].ID != 9 {
		t.Fatalf("stacks not sorted or stale: %+v", got)
	}
}

func TestLevelUp(t *testing.T) {
	cases := []struct {
		level     int32
		exp, gain int64
		wantLevel int32
		wantExp   int64
	}{
		{1, 0, 50, 1, 50},
		{1, 50, 50, 2, 0},
		{1, 0, 100 + 200 + 10, 3, 10},
		{99, 0, 1 << 40, levelup.MaxLevel, 0},
	}
	for _, c := range cases {
		l, e := levelup.AddExp(c.level, c.exp, c.gain)
		if l != c.wantLevel || e != c.wantExp {
			t.Errorf("AddExp(%d,%d,%d) = %d,%d want %d,%d", c.level, c.exp, c.gain, l, e, c.wantLevel, c.wantExp)
		}
	}
}

func TestAOIGrid(t *testing.T) {
	g := aoi.NewGrid(1000, 1000, 100)
	if v := g.Add(1, 50, 50); len(v) != 0 {
		t.Fatalf("first entity sees %v", v)
	}
	if v := g.Add(2, 150, 150); !slices.Equal(v, []int64{1}) { // adjacent cell
		t.Fatalf("entity 2 should see 1, got %v", v)
	}
	if v := g.Add(3, 950, 950); len(v) != 0 { // far corner
		t.Fatalf("entity 3 should see nobody, got %v", v)
	}

	// Moving inside the same neighbourhood: only "stayed".
	entered, left, stayed := g.Move(2, 160, 160)
	if len(entered) != 0 || len(left) != 0 || !slices.Equal(stayed, []int64{1}) {
		t.Fatalf("same-cell move: %v %v %v", entered, left, stayed)
	}
	// Moving next to entity 3: leaves 1, meets 3.
	entered, left, stayed = g.Move(2, 850, 850)
	if !slices.Equal(entered, []int64{3}) || !slices.Equal(left, []int64{1}) || len(stayed) != 0 {
		t.Fatalf("cross-map move: entered=%v left=%v stayed=%v", entered, left, stayed)
	}
	if w := g.Remove(3); !slices.Equal(w, []int64{2}) {
		t.Fatalf("remove watchers: %v", w)
	}
	if g.Len() != 2 {
		t.Fatalf("len %d", g.Len())
	}
	// Out-of-range coordinates are clamped, not panicking.
	g.Add(4, -5, 5000)
}
