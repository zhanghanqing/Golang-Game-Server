// Package cluster connects server nodes: a message Bus for communication and a
// Registry for service discovery, plus helpers for sending Envelopes.
//
// The same business code runs in two deployment modes:
//
//   - all-in-one: LocalBus + MemoryRegistry, every service in one process.
//   - distributed: NatsBus + RedisRegistry, services spread over many processes.
//
// Only the configuration changes; services never know which mode they run in.
package cluster

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
	"google.golang.org/protobuf/proto"

	"gameserver/framework/logx"
	"gameserver/framework/pb/clusterpb"
)

// Config selects the bus and registry implementations.
type Config struct {
	// Bus is "local" (default) or "nats".
	Bus string `yaml:"bus"`
	// NatsURL is used when Bus == "nats", e.g. "nats://127.0.0.1:4222".
	NatsURL string `yaml:"nats_url"`
	// Registry is "memory" (default) or "redis".
	Registry string `yaml:"registry"`
	// RegistryTTL is how long a registration survives without heartbeat. Default 10s.
	RegistryTTL time.Duration `yaml:"registry_ttl"`
}

// Cluster is an app.Module owning the bus and the registry of this node.
type Cluster struct {
	cfg      Config
	nodeID   string
	rdb      *redis.Client
	logger   *slog.Logger
	bus      Bus
	registry Registry
}

// New creates the cluster module. rdb is required only when cfg.Registry == "redis".
func New(nodeID string, cfg Config, rdb *redis.Client) *Cluster {
	return &Cluster{cfg: cfg, nodeID: nodeID, rdb: rdb, logger: logx.Module("cluster")}
}

// NewWith creates a cluster from existing components (useful in tests).
func NewWith(nodeID string, bus Bus, registry Registry) *Cluster {
	return &Cluster{nodeID: nodeID, bus: bus, registry: registry, logger: logx.Module("cluster")}
}

func (c *Cluster) Name() string { return "cluster" }

func (c *Cluster) Init(ctx context.Context) error {
	if err := ValidateToken(c.nodeID); err != nil {
		return fmt.Errorf("node id: %w", err)
	}
	if c.bus == nil {
		switch c.cfg.Bus {
		case "", "local":
			c.bus = NewLocalBus()
		case "nats":
			b, err := NewNatsBus(c.cfg.NatsURL, c.nodeID, c.logger)
			if err != nil {
				return fmt.Errorf("connect nats %s: %w", c.cfg.NatsURL, err)
			}
			c.bus = b
		default:
			return fmt.Errorf("unknown bus %q", c.cfg.Bus)
		}
	}
	if c.registry == nil {
		switch c.cfg.Registry {
		case "", "memory":
			c.registry = NewMemoryRegistry()
		case "redis":
			if c.rdb == nil {
				return fmt.Errorf("redis registry requires a redis client")
			}
			c.registry = NewRedisRegistry(c.rdb, c.cfg.RegistryTTL, c.logger)
		default:
			return fmt.Errorf("unknown registry %q", c.cfg.Registry)
		}
	}
	c.logger.Info("cluster ready", "node", c.nodeID, "bus", c.cfg.Bus, "registry", c.cfg.Registry)
	return nil
}

func (c *Cluster) Start(ctx context.Context) error { return c.registry.Start(ctx) }

func (c *Cluster) Stop(context.Context) error {
	_ = c.registry.Close()
	return c.bus.Close()
}

// NodeID returns this node's id.
func (c *Cluster) NodeID() string { return c.nodeID }

// Bus returns the message bus.
func (c *Cluster) Bus() Bus { return c.bus }

// Registry returns the service registry.
func (c *Cluster) Registry() Registry { return c.registry }

// Publish serializes env and publishes it on subject. The source node is filled in.
func (c *Cluster) Publish(subject string, env *clusterpb.Envelope) error {
	env.SrcNode = c.nodeID
	data, err := proto.Marshal(env)
	if err != nil {
		return err
	}
	return c.bus.Publish(subject, data)
}

// SendToNode delivers env to the given service instance on a specific node.
func (c *Cluster) SendToNode(service, nodeID string, env *clusterpb.Envelope) error {
	return c.Publish(NodeSubject(service, nodeID), env)
}

// SendToService delivers env to any one instance of service (load balanced).
func (c *Cluster) SendToService(service string, env *clusterpb.Envelope) error {
	return c.Publish(ServiceSubject(service), env)
}

// Broadcast delivers env to every instance of service.
func (c *Cluster) Broadcast(service string, env *clusterpb.Envelope) error {
	return c.Publish(BroadcastSubject(service), env)
}

// EnvelopeHandler handles decoded envelopes.
type EnvelopeHandler func(*clusterpb.Envelope)

// SubscribeEnvelopes subscribes to subject and decodes every message as an Envelope.
func (c *Cluster) SubscribeEnvelopes(subject, queue string, h EnvelopeHandler) (Subscription, error) {
	return c.bus.Subscribe(subject, queue, func(m *Msg) {
		env := &clusterpb.Envelope{}
		if err := proto.Unmarshal(m.Data, env); err != nil {
			c.logger.Error("bad envelope", "subject", m.Subject, "err", err)
			return
		}
		h(env)
	})
}

// PickNode chooses a node of service for key using rendezvous hashing.
func (c *Cluster) PickNode(service string, key uint64) (string, bool) {
	n := PickByKey(c.registry.Nodes(service), key)
	return n, n != ""
}
