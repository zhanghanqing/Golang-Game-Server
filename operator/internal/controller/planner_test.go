package controller

import (
	"slices"
	"testing"
	"time"
)

var t0 = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

func active(name string, players int) gameNode {
	return gameNode{Name: name, Version: "v1", Ready: true, Players: players}
}

func draining(name string, players int, since time.Duration) gameNode {
	n := active(name, players)
	at := t0.Add(-since)
	n.Drain = &at
	return n
}

func auto(min, max, target int) *autoscalePolicy {
	return &autoscalePolicy{Min: min, Max: max, Target: target, ScaleDownDelay: 2 * time.Minute}
}

func TestPlan(t *testing.T) {
	outdated := func(n gameNode) gameNode { n.Version = "v0"; return n }
	starting := func(n gameNode) gameNode { n.Ready = false; n.Players = -1; return n }
	failed := func(n gameNode) gameNode { n.Failed = true; return n }

	tests := []struct {
		name string
		in   planInput
		want gamePlan // only Desired, Create, Drain, Delete, Scaled are compared
	}{
		{
			name: "empty fixed cluster creates all nodes",
			in:   planInput{Replicas: 3},
			want: gamePlan{Desired: 3, Create: 3, Scaled: true},
		},
		{
			name: "steady state does nothing",
			in:   planInput{Replicas: 2, Nodes: []gameNode{active("a", 10), active("b", 20)}},
			want: gamePlan{Desired: 2},
		},
		{
			name: "autoscale up: 250 players at 100 per node needs 3",
			in:   planInput{Autoscale: auto(1, 10, 100), Nodes: []gameNode{active("a", 130), active("b", 120)}},
			want: gamePlan{Desired: 3, Create: 1, Scaled: true},
		},
		{
			name: "autoscale is clamped to max",
			in:   planInput{Autoscale: auto(1, 3, 10), Nodes: []gameNode{active("a", 500)}},
			want: gamePlan{Desired: 3, Create: 2, Scaled: true},
		},
		{
			name: "autoscale is clamped to min with no players",
			in:   planInput{Autoscale: auto(2, 5, 100)},
			want: gamePlan{Desired: 2, Create: 2, Scaled: true},
		},
		{
			name: "scale down drains the emptiest node",
			in: planInput{Autoscale: auto(1, 10, 100), LastScale: t0.Add(-time.Hour),
				Nodes: []gameNode{active("a", 40), active("b", 3), active("c", 30)}},
			want: gamePlan{Desired: 1, Drain: []string{"b"}, Scaled: true},
		},
		{
			name: "scale down waits for the cool-down",
			in: planInput{Autoscale: auto(1, 10, 100), LastScale: t0.Add(-30 * time.Second),
				Nodes: []gameNode{active("a", 40), active("b", 3)}},
			want: gamePlan{Desired: 1},
		},
		{
			name: "only one node drains at a time",
			in: planInput{Autoscale: auto(1, 10, 100), LastScale: t0.Add(-time.Hour), DrainTimeout: time.Hour,
				Nodes: []gameNode{active("a", 10), active("b", 5), draining("c", 4, time.Minute)}},
			want: gamePlan{Desired: 1},
		},
		{
			name: "no new drain in the pass that removes a drained node",
			in: planInput{Autoscale: auto(1, 10, 100), LastScale: t0.Add(-time.Hour), DrainTimeout: time.Hour,
				Nodes: []gameNode{active("a", 10), active("b", 5), draining("c", 0, time.Minute)}},
			want: gamePlan{Desired: 1, Delete: []string{"c"}},
		},
		{
			name: "unknown player counts are drained last",
			in: planInput{Replicas: 1, Nodes: []gameNode{
				{Name: "a", Version: "v1", Ready: true, Players: -1}, active("b", 50)}},
			want: gamePlan{Desired: 1, Drain: []string{"b"}, Scaled: true},
		},
		{
			name: "empty draining node is deleted",
			in:   planInput{Replicas: 1, DrainTimeout: 5 * time.Minute, Nodes: []gameNode{active("a", 5), draining("b", 0, time.Second)}},
			want: gamePlan{Desired: 1, Delete: []string{"b"}},
		},
		{
			name: "draining node with players waits",
			in:   planInput{Replicas: 1, DrainTimeout: 5 * time.Minute, Nodes: []gameNode{active("a", 5), draining("b", 7, time.Minute)}},
			want: gamePlan{Desired: 1},
		},
		{
			name: "drain timeout deletes the node anyway",
			in:   planInput{Replicas: 1, DrainTimeout: 5 * time.Minute, Nodes: []gameNode{active("a", 5), draining("b", 7, 6*time.Minute)}},
			want: gamePlan{Desired: 1, Delete: []string{"b"}},
		},
		{
			name: "failed node is deleted and replaced",
			in:   planInput{Replicas: 2, Nodes: []gameNode{active("a", 5), failed(active("b", 0))}},
			want: gamePlan{Desired: 2, Create: 1, Delete: []string{"b"}, Scaled: true},
		},
		{
			name: "no draining while a node is still starting",
			in:   planInput{Replicas: 1, Nodes: []gameNode{active("a", 5), starting(active("b", 0))}},
			want: gamePlan{Desired: 1},
		},
		{
			name: "rolling update: surge one new node first",
			in:   planInput{Replicas: 2, Version: "v1", Nodes: []gameNode{outdated(active("a", 5)), outdated(active("b", 9))}},
			want: gamePlan{Desired: 2, Create: 1},
		},
		{
			name: "rolling update: then drain the emptiest outdated node, ignoring cool-down",
			in: planInput{Autoscale: auto(2, 5, 100), LastScale: t0, Version: "v1",
				Nodes: []gameNode{outdated(active("a", 9)), outdated(active("b", 5)), active("c", 0)}},
			want: gamePlan{Desired: 2, Drain: []string{"b"}},
		},
		{
			name: "rolling update: wait for the drain before the next surge",
			in: planInput{Replicas: 2, Version: "v1", DrainTimeout: time.Hour,
				Nodes: []gameNode{outdated(active("a", 9)), draining("b", 5, time.Minute), active("c", 1)}},
			want: gamePlan{Desired: 2},
		},
		{
			name: "players on draining nodes still count toward capacity",
			in: planInput{Autoscale: auto(1, 10, 100), DrainTimeout: time.Hour,
				Nodes: []gameNode{active("a", 100), draining("b", 150, time.Minute)}},
			want: gamePlan{Desired: 3, Create: 2, Scaled: true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.in.Now = t0
			if tt.in.Version == "" {
				tt.in.Version = "v1"
			}
			got := plan(tt.in)
			if got.Desired != tt.want.Desired || got.Create != tt.want.Create || got.Scaled != tt.want.Scaled ||
				!slices.Equal(got.Drain, tt.want.Drain) || !slices.Equal(got.Delete, tt.want.Delete) {
				t.Fatalf("plan:\n got  desired=%d create=%d drain=%v delete=%v scaled=%v\n want desired=%d create=%d drain=%v delete=%v scaled=%v\n reasons=%q",
					got.Desired, got.Create, got.Drain, got.Delete, got.Scaled,
					tt.want.Desired, tt.want.Create, tt.want.Drain, tt.want.Delete, tt.want.Scaled, got.Reasons)
			}
		})
	}
}

// TestRollingUpdateConverges simulates a whole rolling update: apply each plan
// to a fake world until nothing is left to do, and check capacity never drops.
func TestRollingUpdateConverges(t *testing.T) {
	nodes := []gameNode{
		{Name: "old-1", Version: "v0", Ready: true, Players: 30},
		{Name: "old-2", Version: "v0", Ready: true, Players: 20},
		{Name: "old-3", Version: "v0", Ready: true, Players: 10},
	}
	now := t0
	created := 0
	for step := range 50 {
		p := plan(planInput{Now: now, Nodes: nodes, Version: "v1", Replicas: 3, DrainTimeout: time.Minute})
		activeCount := 0
		for _, n := range nodes {
			if n.Drain == nil {
				activeCount++
			}
		}
		if activeCount < 3 {
			t.Fatalf("step %d: only %d active nodes", step, activeCount)
		}
		busy := slices.ContainsFunc(nodes, func(n gameNode) bool { return n.Drain != nil || n.Version != "v1" })
		if p.Create == 0 && len(p.Drain) == 0 && len(p.Delete) == 0 && !busy {
			for _, n := range nodes {
				if n.Version != "v1" || n.Drain != nil {
					t.Fatalf("converged with %+v", nodes)
				}
			}
			if created != 3 {
				t.Fatalf("created %d nodes, want 3", created)
			}
			return
		}
		// Apply: deleted nodes vanish, draining nodes lose their players over
		// time, new nodes come up ready and empty.
		var next []gameNode
		for _, n := range nodes {
			if slices.Contains(p.Delete, n.Name) {
				continue
			}
			if slices.Contains(p.Drain, n.Name) {
				at := now
				n.Drain = &at
			}
			if n.Drain != nil {
				n.Players = max(n.Players-15, 0) // players leave or move
			}
			next = append(next, n)
		}
		for range p.Create {
			created++
			next = append(next, gameNode{Name: "new-" + string(rune('0'+created)), Version: "v1", Ready: true})
		}
		nodes = next
		now = now.Add(10 * time.Second)
	}
	t.Fatalf("rolling update did not converge: %+v", nodes)
}
