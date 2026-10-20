package router

import (
	"context"
	"sync"
	"testing"

	"google.golang.org/protobuf/proto"

	"gameserver/framework/errcode"
	"gameserver/framework/message"
	"gameserver/framework/pb/clusterpb"
	"gameserver/framework/pb/syspb"
)

type fakeSender struct {
	mu   sync.Mutex
	sent []*clusterpb.Envelope
}

func (f *fakeSender) NodeID() string { return "test" }
func (f *fakeSender) SendToNode(_, _ string, env *clusterpb.Envelope) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, env)
	return nil
}
func (f *fakeSender) Broadcast(_ string, env *clusterpb.Envelope) error {
	return f.SendToNode("", "", env)
}

func forward(t *testing.T, id uint32, msg proto.Message) *clusterpb.Envelope {
	body, err := proto.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	return &clusterpb.Envelope{Kind: clusterpb.Kind_KIND_FORWARD, GateNode: "g1", SessionId: 5, Uid: 9, MsgId: id, Seq: 3, Body: body}
}

func TestDispatchReplyAndError(t *testing.T) {
	s := &fakeSender{}
	r := New("svc", message.Default, s)
	hb := uint32(syspb.SysMsgID_HEARTBEAT_REQ)
	Handle(r, hb, func(c *Context, req *syspb.HeartbeatReq) (proto.Message, error) {
		if req.ClientTimeMs < 0 {
			return nil, errcode.New(1234, "negative")
		}
		return &syspb.HeartbeatResp{ClientTimeMs: req.ClientTimeMs}, nil
	})

	r.Dispatch(context.Background(), forward(t, hb, &syspb.HeartbeatReq{ClientTimeMs: 42}), nil)
	r.Dispatch(context.Background(), forward(t, hb, &syspb.HeartbeatReq{ClientTimeMs: -1}), nil)
	r.Dispatch(context.Background(), forward(t, 4242, &syspb.HeartbeatReq{}), nil) // unknown id

	if len(s.sent) != 3 {
		t.Fatalf("sent %d envelopes, want 3", len(s.sent))
	}
	ok := s.sent[0]
	if ok.Kind != clusterpb.Kind_KIND_REPLY || ok.Seq != 3 || ok.SessionId != 5 || ok.MsgId != uint32(syspb.SysMsgID_HEARTBEAT_RESP) {
		t.Fatalf("bad reply envelope: %+v", ok)
	}
	for i, wantCode := range []int32{1234, int32(syspb.ErrorCode_UNKNOWN_MESSAGE)} {
		env := s.sent[i+1]
		if env.MsgId != uint32(syspb.SysMsgID_ERROR_RESP) {
			t.Fatalf("expected ErrorResp, got msg %d", env.MsgId)
		}
		e := &syspb.ErrorResp{}
		_ = proto.Unmarshal(env.Body, e)
		if e.Code != wantCode {
			t.Fatalf("code %d want %d", e.Code, wantCode)
		}
	}
}

func TestPanicBecomesInternalError(t *testing.T) {
	s := &fakeSender{}
	r := New("svc", message.Default, s)
	hb := uint32(syspb.SysMsgID_HEARTBEAT_REQ)
	r.HandleFunc(hb, func(*Context, proto.Message) (proto.Message, error) { panic("bug") })
	r.Dispatch(context.Background(), forward(t, hb, &syspb.HeartbeatReq{}), nil)
	e := &syspb.ErrorResp{}
	_ = proto.Unmarshal(s.sent[0].Body, e)
	if e.Code != int32(syspb.ErrorCode_INTERNAL) {
		t.Fatalf("code %d, want INTERNAL", e.Code)
	}
}
