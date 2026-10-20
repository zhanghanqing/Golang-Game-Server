package gamesvc

import (
	"context"
	"errors"
	"fmt"
	"time"

	"google.golang.org/protobuf/proto"

	"gameserver/framework/actor"
	"gameserver/framework/router"
	"gameserver/game/logic/bag"
	"gameserver/game/model"
	"gameserver/game/pb"
	"gameserver/game/repo"
)

// Spawn point and world size of the sample scene.
const (
	SpawnX      float32 = 500
	SpawnY      float32 = 500
	WorldWidth  float32 = 1000
	WorldHeight float32 = 1000
)

// Player is one online character. All fields are owned by the player's actor:
// only read or write them from functions running inside p.actor.
type Player struct {
	uid   int64
	svc   *Service
	actor *actor.Actor

	data    *model.Player // nil until EnterGame loads it
	bag     *bag.Bag
	session router.Session

	dirty     bool
	closed    bool
	inScene   bool
	lastChat  time.Time
	saveTimer *actor.Timer
}

func newPlayer(uid int64, svc *Service) *Player {
	return &Player{
		uid:   uid,
		svc:   svc,
		actor: actor.New(fmt.Sprintf("player-%d", uid), svc.logger),
	}
}

// loaded reports whether the player document is in memory.
func (p *Player) loaded() bool { return p.data != nil }

// boundTo reports whether s is the session currently controlling this player.
func (p *Player) boundTo(s router.Session) bool {
	return p.loaded() && p.session == s
}

// load reads the player from storage, creating a new character on first login.
func (p *Player) load(ctx context.Context, name string) error {
	data, err := p.svc.players.Load(ctx, p.uid)
	switch {
	case err == nil:
	case isNotFound(err):
		if name == "" {
			name = fmt.Sprintf("Hero%04d", p.uid%10000)
		}
		now := time.Now()
		data = &model.Player{
			UID:       p.uid,
			Name:      name,
			Level:     1,
			PosX:      SpawnX,
			PosY:      SpawnY,
			CreatedAt: now,
			// Starter pack.
			Items: []model.ItemStack{{ID: 1001, Count: 5}, {ID: 1002, Count: 2}, {ID: 2001, Count: 1}},
		}
		p.dirty = true
	default:
		return err
	}
	p.data = data
	stacks := make([]bag.Stack, len(data.Items))
	for i, it := range data.Items {
		stacks[i] = bag.Stack{ID: it.ID, Count: it.Count}
	}
	p.bag = bag.New(stacks)
	p.saveTimer = p.actor.Every(p.svc.cfg.SaveInterval, p.save)
	return nil
}

// snapshot copies in-memory state into a document for persistence.
func (p *Player) snapshot() *model.Player {
	d := p.data.Clone()
	stacks := p.bag.Stacks()
	d.Items = make([]model.ItemStack, len(stacks))
	for i, s := range stacks {
		d.Items[i] = model.ItemStack{ID: s.ID, Count: s.Count}
	}
	return d
}

// save writes the player if it changed since the last save.
func (p *Player) save() {
	if !p.loaded() || !p.dirty {
		return
	}
	p.data.Version++
	doc := p.snapshot()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := p.svc.players.Save(ctx, doc); err != nil {
		// Keep dirty so the next tick retries.
		p.svc.logger.Error("save player failed", "uid", p.uid, "err", err)
		return
	}
	p.dirty = false
}

func (p *Player) info() *pb.PlayerInfo {
	return &pb.PlayerInfo{
		Uid:         p.data.UID,
		Name:        p.data.Name,
		Level:       p.data.Level,
		Exp:         p.data.Exp,
		Gold:        p.data.Gold,
		CreatedAtMs: p.data.CreatedAt.UnixMilli(),
	}
}

// push sends a server push to the player's current session.
func (p *Player) push(msg proto.Message) {
	if !p.session.Valid() {
		return
	}
	if err := p.svc.router.Push(p.session, msg); err != nil {
		p.svc.logger.Warn("push failed", "uid", p.uid, "err", err)
	}
}

func isNotFound(err error) bool { return errors.Is(err, repo.ErrNotFound) }
