package cluster

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

var (
	// ErrNoResponders is returned by Request when nobody subscribes to the subject.
	ErrNoResponders = errors.New("cluster: no responders")
	// ErrNoReply is returned by Msg.Respond when the message was not a request.
	ErrNoReply = errors.New("cluster: message has no reply subject")
	// ErrBusClosed is returned after Close.
	ErrBusClosed = errors.New("cluster: bus closed")
)

// Msg is a message delivered by the bus. Data must be treated as read-only.
type Msg struct {
	Subject string
	Data    []byte
	respond func([]byte) error
}

// Respond answers a message that was sent with Bus.Request.
func (m *Msg) Respond(data []byte) error {
	if m.respond == nil {
		return ErrNoReply
	}
	return m.respond(data)
}

// Handler processes bus messages. Handlers must return quickly: hand slow work
// to an actor or a worker goroutine, otherwise the subscription stalls.
type Handler func(*Msg)

// Subscription can be cancelled.
type Subscription interface {
	Unsubscribe() error
}

// Bus is a subject-based publish/subscribe transport between server nodes.
//
// Semantics follow NATS:
//   - Subscribe with queue == "" receives every message on the subject (fan-out).
//   - Subscribers sharing a non-empty queue name form a group; each message is
//     delivered to exactly one member of the group (load balancing).
//   - Messages from one publisher on one subject arrive in order.
type Bus interface {
	Publish(subject string, data []byte) error
	Request(ctx context.Context, subject string, data []byte) ([]byte, error)
	Subscribe(subject, queue string, h Handler) (Subscription, error)
	Close() error
}

// Subject naming. Node ids and service names must not contain '.', '*', '>' or spaces.

// NodeSubject addresses one service instance on one node, e.g. "gs.node.game.game-1".
func NodeSubject(service, nodeID string) string { return "gs.node." + service + "." + nodeID }

// ServiceSubject addresses any instance of a service (queue group), e.g. "gs.svc.login".
func ServiceSubject(service string) string { return "gs.svc." + service }

// BroadcastSubject addresses every instance of a service, e.g. "gs.bcast.gate".
func BroadcastSubject(service string) string { return "gs.bcast." + service }

// ValidateToken checks that s can be used inside a subject.
func ValidateToken(s string) error {
	if s == "" {
		return errors.New("cluster: empty name")
	}
	if strings.ContainsAny(s, ".*> \t\r\n") {
		return fmt.Errorf("cluster: %q contains characters not allowed in subjects", s)
	}
	return nil
}
