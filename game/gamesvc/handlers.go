package gamesvc

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"google.golang.org/protobuf/proto"

	"gameserver/framework/errcode"
	"gameserver/framework/pb/syspb"
	"gameserver/framework/router"
	"gameserver/game/logic/bag"
	"gameserver/game/logic/levelup"
	"gameserver/game/pb"
)

// MaxChatRunes limits one chat message.
const MaxChatRunes = 200

var (
	errNotInGame     = errcode.New(int32(pb.ErrCode_ERR_NOT_IN_GAME), "enter game first")
	errItemNotFound  = errcode.New(int32(pb.ErrCode_ERR_ITEM_NOT_FOUND), "unknown item")
	errItemNotEnough = errcode.New(int32(pb.ErrCode_ERR_ITEM_NOT_ENOUGH), "not enough items")
	errItemNotUsable = errcode.New(int32(pb.ErrCode_ERR_ITEM_NOT_USABLE), "item cannot be used")
	errChatEmpty     = errcode.New(int32(pb.ErrCode_ERR_CHAT_EMPTY), "empty message")
	errChatTooLong   = errcode.New(int32(pb.ErrCode_ERR_CHAT_TOO_LONG), "message too long")
	errChatTooFast   = errcode.New(int32(pb.ErrCode_ERR_CHAT_TOO_FAST), "sending too fast")
	errOutOfBounds   = errcode.New(int32(pb.ErrCode_ERR_POSITION_OUT_OF_BOUNDS), "position out of bounds")
	errMoveTooFar    = errcode.New(int32(pb.ErrCode_ERR_MOVE_TOO_FAR), "move step too large")
	errGMDisabled    = errcode.New(int32(pb.ErrCode_ERR_GM_DISABLED), "gm commands are disabled")
)

// playerHandler is the signature of game handlers: they run inside the
// player's actor and receive the *Player directly.
type playerHandler[Req proto.Message] func(p *Player, c *router.Context, req Req) (proto.Message, error)

// handle registers a player handler. Unless allowUnbound is set, the request
// is rejected when the player has not entered the game with this session.
func handle[Req proto.Message](s *Service, id pb.MsgID, allowUnbound bool, fn playerHandler[Req]) {
	router.Handle(s.router, uint32(id), func(c *router.Context, req Req) (proto.Message, error) {
		p, _ := c.Data.(*Player)
		if p == nil {
			return nil, errNotInGame
		}
		if !allowUnbound && !p.boundTo(c.Session) {
			if !p.loaded() {
				// The entity was created just for this request: release it.
				s.logout(p)
			}
			return nil, errNotInGame
		}
		return fn(p, c, req)
	})
}

func (s *Service) registerHandlers() {
	handle(s, pb.MsgID_ENTER_GAME_REQ, true, s.handleEnterGame)
	handle(s, pb.MsgID_BAG_GET_REQ, false, s.handleBagGet)
	handle(s, pb.MsgID_ITEM_USE_REQ, false, s.handleItemUse)
	handle(s, pb.MsgID_CHAT_SEND_REQ, false, s.handleChat)
	handle(s, pb.MsgID_SCENE_MOVE_REQ, false, s.handleMove)
	handle(s, pb.MsgID_GM_COMMAND_REQ, false, s.handleGM)
}

// ---------------------------------------------------------------------------
// Player

func (s *Service) handleEnterGame(p *Player, c *router.Context, req *pb.EnterGameReq) (proto.Message, error) {
	if !p.loaded() {
		if s.stopping.Load() {
			s.logout(p)
			return nil, errcode.ErrServiceUnavailable
		}
		if err := p.acquireOwnership(c); err != nil {
			s.logout(p)
			return nil, err
		}
		if err := p.load(c, strings.TrimSpace(req.Name)); err != nil {
			s.logout(p) // do not keep a half-initialized entity around
			return nil, err
		}
		s.logger.Info("player loaded", "uid", p.uid, "name", p.data.Name)
	}
	// Same character, new connection (re-login from another device, or reconnect):
	// kick the old session; its SESSION_CLOSED will be ignored as stale.
	if p.session.Valid() && p.session != c.Session {
		_ = s.router.Kick(p.session, syspb.KickReason_KICK_DUPLICATE_LOGIN)
	}
	p.session = c.Session
	p.data.LastLoginAt = time.Now()
	p.dirty = true

	// Reply before entering the scene so the client receives EnterGameResp
	// before any scene pushes.
	_ = c.Reply(&pb.EnterGameResp{
		Player:   p.info(),
		SceneId:  s.scene.ID(),
		Position: &pb.Vector2{X: p.data.PosX, Y: p.data.PosY},
	})
	if p.inScene {
		s.scene.Leave(p.uid)
	}
	s.scene.Enter(p.uid, p.data.Name, p.data.PosX, p.data.PosY, p.session)
	p.inScene = true
	return nil, nil
}

// ---------------------------------------------------------------------------
// Bag

func (s *Service) handleBagGet(p *Player, _ *router.Context, _ *pb.BagGetReq) (proto.Message, error) {
	return &pb.BagGetResp{Items: toPBItems(p.bag.Stacks())}, nil
}

func (s *Service) handleItemUse(p *Player, _ *router.Context, req *pb.ItemUseReq) (proto.Message, error) {
	if req.Count <= 0 {
		req.Count = 1
	}
	def, ok := s.tables.Items.Get(req.ItemId)
	if !ok {
		return nil, errItemNotFound
	}
	if !def.Usable {
		return nil, errItemNotUsable
	}
	left, err := p.bag.Remove(req.ItemId, req.Count)
	if err != nil {
		return nil, errItemNotEnough
	}
	p.data.Gold += def.Gold * req.Count
	p.data.Level, p.data.Exp = levelup.AddExp(p.data.Level, p.data.Exp, def.Exp*req.Count)
	p.dirty = true

	p.push(&pb.BagUpdatePush{Items: []*pb.Item{{ItemId: req.ItemId, Count: left}}})
	p.push(&pb.PlayerInfoPush{Player: p.info()})
	return &pb.ItemUseResp{}, nil
}

func toPBItems(stacks []bag.Stack) []*pb.Item {
	out := make([]*pb.Item, len(stacks))
	for i, st := range stacks {
		out[i] = &pb.Item{ItemId: st.ID, Count: st.Count}
	}
	return out
}

// ---------------------------------------------------------------------------
// Chat

func (s *Service) handleChat(p *Player, _ *router.Context, req *pb.ChatSendReq) (proto.Message, error) {
	text := strings.TrimSpace(req.Text)
	if text == "" {
		return nil, errChatEmpty
	}
	if utf8.RuneCountInString(text) > MaxChatRunes {
		return nil, errChatTooLong
	}
	now := time.Now()
	if now.Sub(p.lastChat) < s.cfg.ChatInterval {
		return nil, errChatTooFast
	}
	p.lastChat = now

	push := &pb.ChatPush{
		Channel:  req.Channel,
		FromUid:  p.uid,
		FromName: p.data.Name,
		Text:     text,
		TimeMs:   now.UnixMilli(),
	}
	switch req.Channel {
	case pb.ChatChannel_CHAT_CHANNEL_SCENE:
		s.scene.BroadcastNearby(p.uid, push)
	default:
		push.Channel = pb.ChatChannel_CHAT_CHANNEL_WORLD
		// World chat reaches players on every game node: broadcast through all gates.
		if err := s.router.Broadcast(nil, push); err != nil {
			return nil, err
		}
	}
	return &pb.ChatSendResp{}, nil
}

// ---------------------------------------------------------------------------
// Scene

func (s *Service) handleMove(p *Player, _ *router.Context, req *pb.SceneMoveReq) (proto.Message, error) {
	if req.Target == nil {
		return nil, errcode.ErrBadRequest
	}
	x, y := req.Target.X, req.Target.Y
	if x < 0 || y < 0 || x >= WorldWidth || y >= WorldHeight ||
		math.IsNaN(float64(x)) || math.IsNaN(float64(y)) {
		return nil, errOutOfBounds
	}
	// Server authority: reject teleport-like moves (basic anti-cheat).
	dx, dy := float64(x-p.data.PosX), float64(y-p.data.PosY)
	if math.Hypot(dx, dy) > float64(s.cfg.MaxMoveStep) {
		return nil, errMoveTooFar
	}
	p.data.PosX, p.data.PosY = x, y
	p.dirty = true
	s.scene.Move(p.uid, x, y)
	return &pb.SceneMoveResp{Position: &pb.Vector2{X: x, Y: y}}, nil
}

// ---------------------------------------------------------------------------
// GM

func (s *Service) handleGM(p *Player, _ *router.Context, req *pb.GmCommandReq) (proto.Message, error) {
	if !s.cfg.GMEnabled {
		return nil, errGMDisabled
	}
	out, err := s.runGM(p, strings.Fields(req.Command))
	if err != nil {
		return nil, errcode.New(int32(pb.ErrCode_ERR_GM_BAD_COMMAND), err.Error())
	}
	return &pb.GmCommandResp{Output: out}, nil
}

func (s *Service) runGM(p *Player, args []string) (string, error) {
	if len(args) == 0 {
		args = []string{"help"}
	}
	num := func(i int) (int64, error) {
		if i >= len(args) {
			return 0, fmt.Errorf("missing argument %d", i)
		}
		return strconv.ParseInt(args[i], 10, 64)
	}
	switch strings.ToLower(args[0]) {
	case "help":
		return "commands: additem <id> <count> | addgold <n> | addexp <n> | info", nil
	case "additem":
		id, err := num(1)
		if err != nil {
			return "", err
		}
		n, err := num(2)
		if err != nil {
			return "", err
		}
		if _, ok := s.tables.Items.Get(int32(id)); !ok {
			return "", fmt.Errorf("unknown item %d", id)
		}
		count, err := p.bag.Add(int32(id), n)
		if err != nil {
			return "", err
		}
		p.dirty = true
		p.push(&pb.BagUpdatePush{Items: []*pb.Item{{ItemId: int32(id), Count: count}}})
		return fmt.Sprintf("item %d now %d", id, count), nil
	case "addgold":
		n, err := num(1)
		if err != nil {
			return "", err
		}
		p.data.Gold += n
		p.dirty = true
		p.push(&pb.PlayerInfoPush{Player: p.info()})
		return fmt.Sprintf("gold now %d", p.data.Gold), nil
	case "addexp":
		n, err := num(1)
		if err != nil {
			return "", err
		}
		p.data.Level, p.data.Exp = levelup.AddExp(p.data.Level, p.data.Exp, n)
		p.dirty = true
		p.push(&pb.PlayerInfoPush{Player: p.info()})
		return fmt.Sprintf("level %d exp %d", p.data.Level, p.data.Exp), nil
	case "info":
		return fmt.Sprintf("uid=%d name=%s level=%d exp=%d gold=%d pos=(%.1f,%.1f)",
			p.uid, p.data.Name, p.data.Level, p.data.Exp, p.data.Gold, p.data.PosX, p.data.PosY), nil
	default:
		return "", fmt.Errorf("unknown command %q", args[0])
	}
}
