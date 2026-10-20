// Package router dispatches client requests on backend services.
//
// A backend service (login, game, ...) receives FORWARD envelopes from gates.
// The router decodes the body using the message registry, runs the middleware
// chain and the registered handler, and sends the handler's response (or an
// ErrorResp) back to the client's gate. It also offers helpers to push
// messages, broadcast, bind sessions and kick clients.
package router

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"google.golang.org/protobuf/proto"

	"gameserver/framework/errcode"
	"gameserver/framework/logx"
	"gameserver/framework/message"
	"gameserver/framework/pb/clusterpb"
	"gameserver/framework/pb/syspb"
)

// GateService is the service name every gate registers under.
const GateService = "gate"

// Sender is the subset of *cluster.Cluster the router needs. It is an
// interface so the router can be unit-tested without a bus.
type Sender interface {
	NodeID() string
	SendToNode(service, nodeID string, env *clusterpb.Envelope) error
	Broadcast(service string, env *clusterpb.Envelope) error
}

// Session references a client session living on a gate node. It is a small
// value type that business code can store (e.g. in a player object) and use
// later to push messages.
type Session struct {
	GateNode  string
	SessionID uint64
	UID       int64
}

// Valid reports whether the session reference is set.
func (s Session) Valid() bool { return s.GateNode != "" && s.SessionID != 0 }

// HandlerFunc handles one decoded request. Returning a non-nil message sends it
// as the response; returning an error sends an ErrorResp. Returning (nil, nil)
// sends nothing (the handler may reply later with Context.Reply).
type HandlerFunc func(c *Context, req proto.Message) (proto.Message, error)

// Middleware wraps a handler (logging, metrics, auth, recovery...).
type Middleware func(next HandlerFunc) HandlerFunc

// Router holds handlers for one backend service.
type Router struct {
	service  string
	reg      *message.Registry
	sender   Sender
	handlers map[uint32]HandlerFunc
	mws      []Middleware
	logger   *slog.Logger
}

// New creates a router for service. Recovery and metrics middleware are installed by default.
func New(service string, reg *message.Registry, sender Sender) *Router {
	r := &Router{
		service:  service,
		reg:      reg,
		sender:   sender,
		handlers: make(map[uint32]HandlerFunc),
		logger:   logx.Module("router." + service),
	}
	r.Use(Recover(r.logger), Metrics(service))
	return r
}

// Use appends middleware. It must be called before handlers are registered.
func (r *Router) Use(mws ...Middleware) { r.mws = append(r.mws, mws...) }

// Registry returns the message registry.
func (r *Router) Registry() *message.Registry { return r.reg }

// HandleFunc registers h for message id. It panics on duplicates or unknown ids.
func (r *Router) HandleFunc(id uint32, h HandlerFunc) {
	if _, ok := r.reg.New(id); !ok {
		panic(fmt.Sprintf("router: message id %d is not registered", id))
	}
	if _, dup := r.handlers[id]; dup {
		panic(fmt.Sprintf("router: duplicate handler for id %d", id))
	}
	for i := len(r.mws) - 1; i >= 0; i-- {
		h = r.mws[i](h)
	}
	r.handlers[id] = h
}

// Handle registers a strongly typed handler. Example:
//
//	router.Handle(r, uint32(pb.MsgID_LOGIN_REQ), func(c *router.Context, req *pb.LoginReq) (proto.Message, error) {...})
func Handle[Req proto.Message](r *Router, id uint32, fn func(*Context, Req) (proto.Message, error)) {
	r.HandleFunc(id, func(c *Context, req proto.Message) (proto.Message, error) {
		typed, ok := req.(Req)
		if !ok {
			return nil, errcode.ErrBadRequest
		}
		return fn(c, typed)
	})
}

// Has reports whether a handler exists for id.
func (r *Router) Has(id uint32) bool {
	_, ok := r.handlers[id]
	return ok
}

// Dispatch handles one FORWARD envelope synchronously in the caller's goroutine.
// Callers decide the concurrency model (worker pool, actor mailbox, ...).
// data, if not nil, is exposed to the handler as Context.Data (e.g. the player).
func (r *Router) Dispatch(ctx context.Context, env *clusterpb.Envelope, data any) {
	c := &Context{
		Context: ctx,
		Router:  r,
		Env:     env,
		Session: Session{GateNode: env.GateNode, SessionID: env.SessionId, UID: env.Uid},
		Data:    data,
	}
	h, ok := r.handlers[env.MsgId]
	if !ok {
		c.ReplyError(errcode.ErrUnknownMessage)
		return
	}
	req, err := r.reg.Decode(env.MsgId, env.Body)
	if err != nil {
		c.ReplyError(errcode.ErrBadRequest)
		return
	}
	resp, err := h(c, req)
	switch {
	case err != nil:
		c.ReplyError(err)
	case resp != nil:
		if err := c.Reply(resp); err != nil {
			r.logger.Error("reply failed", "msg_id", env.MsgId, "err", err)
		}
	}
}

// Push sends msg to a session as a server push (seq 0).
func (r *Router) Push(s Session, msg proto.Message) error {
	return r.send(s, 0, msg)
}

// Broadcast sends msg to the given uids on every gate. An empty uids slice
// targets every authenticated session.
func (r *Router) Broadcast(uids []int64, msg proto.Message) error {
	id, body, err := r.reg.Encode(msg)
	if err != nil {
		return err
	}
	return r.sender.Broadcast(GateService, &clusterpb.Envelope{
		Kind:  clusterpb.Kind_KIND_BROADCAST,
		MsgId: id,
		Body:  body,
		Uids:  uids,
	})
}

// Bind attaches uid and backend routes (service -> node id) to the session on its gate.
// After binding, the gate forwards sticky requests of those services to the given nodes.
func (r *Router) Bind(s Session, uid int64, routes map[string]string) error {
	return r.sender.SendToNode(GateService, s.GateNode, &clusterpb.Envelope{
		Kind:      clusterpb.Kind_KIND_BIND,
		GateNode:  s.GateNode,
		SessionId: s.SessionID,
		Uid:       uid,
		Routes:    routes,
	})
}

// Kick asks the gate to send a KickPush and close the session.
func (r *Router) Kick(s Session, reason syspb.KickReason) error {
	return r.sender.SendToNode(GateService, s.GateNode, &clusterpb.Envelope{
		Kind:      clusterpb.Kind_KIND_KICK,
		GateNode:  s.GateNode,
		SessionId: s.SessionID,
		Uid:       s.UID,
		Reason:    int32(reason),
	})
}

func (r *Router) send(s Session, seq uint32, msg proto.Message) error {
	if !s.Valid() {
		return fmt.Errorf("router: invalid session %+v", s)
	}
	id, body, err := r.reg.Encode(msg)
	if err != nil {
		return err
	}
	return r.sender.SendToNode(GateService, s.GateNode, &clusterpb.Envelope{
		Kind:      clusterpb.Kind_KIND_REPLY,
		GateNode:  s.GateNode,
		SessionId: s.SessionID,
		Uid:       s.UID,
		MsgId:     id,
		Seq:       seq,
		Body:      body,
	})
}

// Context is passed to handlers. It embeds context.Context for cancellation and deadlines.
type Context struct {
	context.Context
	Router  *Router
	Env     *clusterpb.Envelope
	Session Session
	// Data carries per-dispatch business data, e.g. the *Player that owns the actor.
	Data    any
	replied bool
}

// MsgID returns the request message id.
func (c *Context) MsgID() uint32 { return c.Env.MsgId }

// UID returns the authenticated uid of the requesting client (0 if not logged in).
func (c *Context) UID() int64 { return c.Env.Uid }

// Reply sends msg as the response to this request (same seq). Only the first reply is sent.
func (c *Context) Reply(msg proto.Message) error {
	if c.replied {
		return nil
	}
	c.replied = true
	return c.Router.send(c.Session, c.Env.Seq, msg)
}

// ReplyError sends an ErrorResp for this request.
func (c *Context) ReplyError(err error) {
	code := errcode.Code(err)
	text := "internal error"
	var e *errcode.Error
	if errors.As(err, &e) {
		text = e.Msg
	} else {
		c.Router.logger.Error("handler failed", "msg_id", c.Env.MsgId, "uid", c.Env.Uid, "err", err)
	}
	_ = c.Reply(&syspb.ErrorResp{Code: code, Message: text, ReqMsgId: c.Env.MsgId})
}

// Push sends a server push to the requesting session.
func (c *Context) Push(msg proto.Message) error { return c.Router.Push(c.Session, msg) }
