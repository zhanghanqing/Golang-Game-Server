package cluster

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/nats-io/nats.go"
)

// NatsBus implements Bus on top of a NATS server. It is used when services run
// in separate processes. NATS gives us subject routing, queue groups,
// request/reply and automatic reconnection out of the box.
type NatsBus struct {
	nc *nats.Conn
}

// NewNatsBus connects to url (comma separated list allowed) and identifies as name.
func NewNatsBus(url, name string, logger *slog.Logger) (*NatsBus, error) {
	if logger == nil {
		logger = slog.Default()
	}
	nc, err := nats.Connect(url,
		nats.Name(name),
		nats.MaxReconnects(-1), // never give up; the server may restart
		nats.ReconnectWait(500*time.Millisecond),
		nats.Timeout(5*time.Second),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			logger.Warn("nats disconnected", "err", err)
		}),
		nats.ReconnectHandler(func(c *nats.Conn) {
			logger.Info("nats reconnected", "url", c.ConnectedUrl())
		}),
		nats.ErrorHandler(func(_ *nats.Conn, sub *nats.Subscription, err error) {
			subject := ""
			if sub != nil {
				subject = sub.Subject
			}
			logger.Error("nats async error", "subject", subject, "err", err)
		}),
	)
	if err != nil {
		return nil, err
	}
	return &NatsBus{nc: nc}, nil
}

func (b *NatsBus) Publish(subject string, data []byte) error {
	return b.nc.Publish(subject, data)
}

func (b *NatsBus) Request(ctx context.Context, subject string, data []byte) ([]byte, error) {
	m, err := b.nc.RequestWithContext(ctx, subject, data)
	if err != nil {
		if errors.Is(err, nats.ErrNoResponders) {
			return nil, ErrNoResponders
		}
		return nil, err
	}
	return m.Data, nil
}

func (b *NatsBus) Subscribe(subject, queue string, h Handler) (Subscription, error) {
	cb := func(m *nats.Msg) {
		msg := &Msg{Subject: m.Subject, Data: m.Data}
		if m.Reply != "" {
			msg.respond = m.Respond
		}
		h(msg)
	}
	var (
		sub *nats.Subscription
		err error
	)
	if queue == "" {
		sub, err = b.nc.Subscribe(subject, cb)
	} else {
		sub, err = b.nc.QueueSubscribe(subject, queue, cb)
	}
	if err != nil {
		return nil, err
	}
	// Game traffic is bursty: allow large pending buffers instead of dropping messages.
	_ = sub.SetPendingLimits(1<<20, 512<<20)
	return sub, nil
}

// Close drains subscriptions (finishing in-flight messages) and closes the connection.
func (b *NatsBus) Close() error {
	if err := b.nc.Drain(); err != nil {
		b.nc.Close()
		return err
	}
	return nil
}
