package controller

import (
	"cmp"
	"fmt"
	"math"
	"slices"
	"time"
)

// gameNode is what the planner knows about one game pod.
type gameNode struct {
	Name    string
	Version string     // pod template hash
	Ready   bool       // pod is Ready (process started, admin /readyz ok)
	Failed  bool       // pod phase Failed or Succeeded: it will never serve again
	Drain   *time.Time // when draining started; nil for active nodes
	Players int        // online players; -1 when unknown
}

// autoscalePolicy mirrors AutoscaleSpec with defaults applied.
type autoscalePolicy struct {
	Min, Max, Target int
	ScaleDownDelay   time.Duration
}

// planInput is a snapshot of one GameCluster's game tier.
type planInput struct {
	Now          time.Time
	Nodes        []gameNode // terminating pods are excluded by the caller
	Version      string     // hash of the current pod template
	Replicas     int        // fixed size, used when Autoscale is nil
	Autoscale    *autoscalePolicy
	DrainTimeout time.Duration
	LastScale    time.Time
}

// gamePlan is what to do now. The controller applies it and calls the planner
// again on the next reconcile; every step is small and idempotent.
type gamePlan struct {
	Desired int      // desired active nodes
	Players int      // known online players
	Create  int      // new nodes to create
	Drain   []string // active nodes to start draining
	Delete  []string // nodes to delete (failed, or finished draining)
	Scaled  bool     // a capacity change happened (updates LastScale)
	Reasons []string // human readable, for events and logs
}

// desiredNodes computes the size of the game tier.
func desiredNodes(in planInput, players int) int {
	if in.Autoscale == nil {
		return in.Replicas
	}
	a := in.Autoscale
	n := int(math.Ceil(float64(players) / float64(a.Target)))
	return min(max(n, a.Min), a.Max)
}

// plan decides the next step for the game tier. Rules, in order:
//
//  1. Failed nodes are deleted (and replaced by rule 3).
//  2. A draining node is deleted once it is empty or its drain timed out;
//     deleting sends SIGTERM, the node saves and kicks remaining players, and
//     their clients reconnect to an active node.
//  3. Too few active nodes: create the missing ones immediately.
//  4. Wait while any active node is still starting.
//  5. Too many active nodes: drain one, preferring an outdated node, then the
//     node with the fewest players. A pure scale-down (no outdated nodes)
//     waits for ScaleDownDelay since the last scaling action.
//  6. Rolling update: when outdated nodes remain and nothing is draining,
//     add one surge node; rule 5 then drains an outdated node.
//
// At most one node drains at a time (a node deleted in this pass still counts),
// so capacity never drops by more than one node.
func plan(in planInput) gamePlan {
	var p gamePlan
	for _, n := range in.Nodes {
		if n.Players > 0 {
			p.Players += n.Players
		}
	}
	p.Desired = desiredNodes(in, p.Players)

	var active []gameNode
	draining := 0
	for _, n := range in.Nodes {
		switch {
		case n.Failed:
			p.Delete = append(p.Delete, n.Name)
			p.Reasons = append(p.Reasons, fmt.Sprintf("delete failed node %s", n.Name))
		case n.Drain != nil && n.Players == 0:
			draining++ // still settling in this pass
			p.Delete = append(p.Delete, n.Name)
			p.Reasons = append(p.Reasons, fmt.Sprintf("node %s drained (no players left)", n.Name))
		case n.Drain != nil && in.Now.Sub(*n.Drain) >= in.DrainTimeout:
			draining++ // its players are about to reconnect elsewhere
			p.Delete = append(p.Delete, n.Name)
			p.Reasons = append(p.Reasons, fmt.Sprintf("node %s drain timed out with %d players: shutting down, players reconnect elsewhere", n.Name, n.Players))
		case n.Drain != nil:
			draining++
		default:
			active = append(active, n)
		}
	}

	if len(active) < p.Desired {
		p.Create = p.Desired - len(active)
		p.Scaled = true
		p.Reasons = append(p.Reasons, fmt.Sprintf("scale up: %d active, %d desired (%d players)", len(active), p.Desired, p.Players))
		return p
	}
	for _, n := range active {
		if !n.Ready {
			return p // let new nodes come up before removing capacity
		}
	}

	var outdated []gameNode
	for _, n := range active {
		if n.Version != in.Version {
			outdated = append(outdated, n)
		}
	}

	if len(active) > p.Desired {
		if draining > 0 {
			return p
		}
		candidates := outdated
		if len(candidates) == 0 {
			if in.Autoscale != nil && in.Now.Sub(in.LastScale) < in.Autoscale.ScaleDownDelay {
				return p // cool down: a recent change may still be settling
			}
			candidates = active
			p.Scaled = true
		}
		victim := slices.MinFunc(candidates, func(a, b gameNode) int {
			return cmp.Or(cmp.Compare(playersOrMax(a), playersOrMax(b)), cmp.Compare(a.Name, b.Name))
		})
		p.Drain = []string{victim.Name}
		if len(outdated) > 0 {
			p.Reasons = append(p.Reasons, fmt.Sprintf("rolling update: drain outdated node %s (%d players)", victim.Name, victim.Players))
		} else {
			p.Reasons = append(p.Reasons, fmt.Sprintf("scale down: %d active, %d desired (%d players): drain %s (%d players)",
				len(active), p.Desired, p.Players, victim.Name, victim.Players))
		}
		return p
	}

	if len(outdated) > 0 && draining == 0 {
		p.Create = 1
		p.Reasons = append(p.Reasons, fmt.Sprintf("rolling update: %d outdated node(s), add one new node first", len(outdated)))
	}
	return p
}

// playersOrMax sorts nodes with an unknown player count last.
func playersOrMax(n gameNode) int {
	if n.Players < 0 {
		return math.MaxInt
	}
	return n.Players
}
