// Package protocol binds the game's protobuf messages to their ids and
// declares how the gate routes them to backend services.
package protocol

import (
	"sync"

	"gameserver/framework/gate"
	"gameserver/framework/message"
	"gameserver/game/pb"
)

// Backend service names. They are used as bus subjects and registry keys.
const (
	ServiceLogin = "login"
	ServiceGame  = "game"
)

var (
	once sync.Once
	reg  *message.Registry
)

// Registry returns the process-wide registry containing framework and game messages.
func Registry() *message.Registry {
	once.Do(func() {
		reg = message.Default
		Register(reg)
	})
	return reg
}

// Register adds every game message to r.
func Register(r *message.Registry) {
	r.Register(uint32(pb.MsgID_LOGIN_REQ), &pb.LoginReq{})
	r.Register(uint32(pb.MsgID_LOGIN_RESP), &pb.LoginResp{})

	r.Register(uint32(pb.MsgID_ENTER_GAME_REQ), &pb.EnterGameReq{})
	r.Register(uint32(pb.MsgID_ENTER_GAME_RESP), &pb.EnterGameResp{})
	r.Register(uint32(pb.MsgID_PLAYER_INFO_PUSH), &pb.PlayerInfoPush{})

	r.Register(uint32(pb.MsgID_BAG_GET_REQ), &pb.BagGetReq{})
	r.Register(uint32(pb.MsgID_BAG_GET_RESP), &pb.BagGetResp{})
	r.Register(uint32(pb.MsgID_ITEM_USE_REQ), &pb.ItemUseReq{})
	r.Register(uint32(pb.MsgID_ITEM_USE_RESP), &pb.ItemUseResp{})
	r.Register(uint32(pb.MsgID_BAG_UPDATE_PUSH), &pb.BagUpdatePush{})

	r.Register(uint32(pb.MsgID_CHAT_SEND_REQ), &pb.ChatSendReq{})
	r.Register(uint32(pb.MsgID_CHAT_SEND_RESP), &pb.ChatSendResp{})
	r.Register(uint32(pb.MsgID_CHAT_PUSH), &pb.ChatPush{})

	r.Register(uint32(pb.MsgID_SCENE_MOVE_REQ), &pb.SceneMoveReq{})
	r.Register(uint32(pb.MsgID_SCENE_MOVE_RESP), &pb.SceneMoveResp{})
	r.Register(uint32(pb.MsgID_SCENE_ENTITY_ENTER_PUSH), &pb.SceneEntityEnterPush{})
	r.Register(uint32(pb.MsgID_SCENE_ENTITY_LEAVE_PUSH), &pb.SceneEntityLeavePush{})
	r.Register(uint32(pb.MsgID_SCENE_ENTITY_MOVE_PUSH), &pb.SceneEntityMovePush{})

	r.Register(uint32(pb.MsgID_GM_COMMAND_REQ), &pb.GmCommandReq{})
	r.Register(uint32(pb.MsgID_GM_COMMAND_RESP), &pb.GmCommandResp{})
}

// GateRoutes tells the gate which service handles which id range.
//
//	[1000, 1999] login: stateless, load balanced, no auth required
//	[2000, 9999] game:  sticky to the node chosen at login, auth required
func GateRoutes() []gate.Route {
	return []gate.Route{
		{MinID: 1000, MaxID: 1999, Service: ServiceLogin, RequireAuth: false, Sticky: false},
		{MinID: 2000, MaxID: 9999, Service: ServiceGame, RequireAuth: true, Sticky: true},
	}
}
