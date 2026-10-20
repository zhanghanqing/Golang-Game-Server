// Package message maps numeric message ids to protobuf message types.
//
// The wire format only carries a msg_id; the registry turns it into a concrete
// Go type for decoding, and turns a Go value back into its id for encoding.
// Ids [1, 999] are reserved for framework (system) messages, which are
// registered automatically in the Default registry.
package message

import (
	"fmt"
	"sync"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"gameserver/framework/pb/syspb"
)

// MaxSystemID is the highest id reserved for framework messages.
const MaxSystemID = 999

// Registry is a bidirectional id <-> protobuf type table. It is safe for concurrent use.
type Registry struct {
	mu     sync.RWMutex
	byID   map[uint32]protoreflect.MessageType
	byName map[protoreflect.FullName]uint32
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{
		byID:   make(map[uint32]protoreflect.MessageType),
		byName: make(map[protoreflect.FullName]uint32),
	}
}

// Default is the process-wide registry. System messages are pre-registered.
var Default = func() *Registry {
	r := NewRegistry()
	RegisterSystem(r)
	return r
}()

// Register binds id to the type of msg. It panics on duplicates because that is
// always a programming error that must be caught at startup.
func (r *Registry) Register(id uint32, msg proto.Message) {
	r.mu.Lock()
	defer r.mu.Unlock()
	mt := msg.ProtoReflect().Type()
	name := mt.Descriptor().FullName()
	if old, ok := r.byID[id]; ok {
		panic(fmt.Sprintf("message: id %d already registered for %s", id, old.Descriptor().FullName()))
	}
	if old, ok := r.byName[name]; ok {
		panic(fmt.Sprintf("message: %s already registered with id %d", name, old))
	}
	r.byID[id] = mt
	r.byName[name] = id
}

// New allocates an empty message for id.
func (r *Registry) New(id uint32) (proto.Message, bool) {
	r.mu.RLock()
	mt, ok := r.byID[id]
	r.mu.RUnlock()
	if !ok {
		return nil, false
	}
	return mt.New().Interface(), true
}

// ID returns the id registered for msg's type.
func (r *Registry) ID(msg proto.Message) (uint32, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	id, ok := r.byName[msg.ProtoReflect().Descriptor().FullName()]
	return id, ok
}

// Encode returns msg's id and serialized bytes.
func (r *Registry) Encode(msg proto.Message) (uint32, []byte, error) {
	id, ok := r.ID(msg)
	if !ok {
		return 0, nil, fmt.Errorf("message: %s is not registered", msg.ProtoReflect().Descriptor().FullName())
	}
	body, err := proto.Marshal(msg)
	if err != nil {
		return 0, nil, err
	}
	return id, body, nil
}

// Decode parses body as the message type registered for id.
func (r *Registry) Decode(id uint32, body []byte) (proto.Message, error) {
	msg, ok := r.New(id)
	if !ok {
		return nil, fmt.Errorf("message: unknown id %d", id)
	}
	if err := proto.Unmarshal(body, msg); err != nil {
		return nil, fmt.Errorf("message: decode id %d: %w", id, err)
	}
	return msg, nil
}

// RegisterSystem registers the framework messages from syspb.
func RegisterSystem(r *Registry) {
	r.Register(uint32(syspb.SysMsgID_HEARTBEAT_REQ), &syspb.HeartbeatReq{})
	r.Register(uint32(syspb.SysMsgID_HEARTBEAT_RESP), &syspb.HeartbeatResp{})
	r.Register(uint32(syspb.SysMsgID_ERROR_RESP), &syspb.ErrorResp{})
	r.Register(uint32(syspb.SysMsgID_KICK_PUSH), &syspb.KickPush{})
}
