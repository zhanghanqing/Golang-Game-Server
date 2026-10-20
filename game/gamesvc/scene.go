package gamesvc

import (
	"log/slog"

	"google.golang.org/protobuf/proto"

	"gameserver/framework/actor"
	"gameserver/framework/router"
	"gameserver/game/logic/aoi"
	"gameserver/game/pb"
)

// AOICellSize is the side of one AOI cell; players see 3x3 cells around them.
const AOICellSize float32 = 100

type sceneEntity struct {
	uid     int64
	name    string
	x, y    float32
	session router.Session
}

func (e *sceneEntity) toPB() *pb.SceneEntity {
	return &pb.SceneEntity{Uid: e.uid, Name: e.name, Position: &pb.Vector2{X: e.x, Y: e.y}}
}

// Scene is a map instance owned by its own actor. Player actors talk to it via
// the exported methods, which only post closures (never block).
type Scene struct {
	id       int32
	actor    *actor.Actor
	router   *router.Router
	grid     *aoi.Grid
	entities map[int64]*sceneEntity
	logger   *slog.Logger
}

// NewScene creates and starts a scene.
func NewScene(id int32, r *router.Router, logger *slog.Logger) *Scene {
	return &Scene{
		id:       id,
		actor:    actor.New("scene", logger),
		router:   r,
		grid:     aoi.NewGrid(WorldWidth, WorldHeight, AOICellSize),
		entities: make(map[int64]*sceneEntity),
		logger:   logger,
	}
}

// ID returns the scene id.
func (s *Scene) ID() int32 { return s.id }

// Stop stops the scene actor.
func (s *Scene) Stop() { s.actor.Stop() }

// Enter adds a player. It receives every visible entity; they receive the player.
func (s *Scene) Enter(uid int64, name string, x, y float32, sess router.Session) {
	s.actor.Post(func() {
		e := &sceneEntity{uid: uid, name: name, x: x, y: y, session: sess}
		s.entities[uid] = e
		visible := s.grid.Add(uid, x, y)
		s.pushEnter(e, visible)
	})
}

// Leave removes a player and notifies everyone who could see it.
func (s *Scene) Leave(uid int64) {
	s.actor.Post(func() {
		if _, ok := s.entities[uid]; !ok {
			return
		}
		watchers := s.grid.Remove(uid)
		delete(s.entities, uid)
		s.pushTo(watchers, &pb.SceneEntityLeavePush{Uids: []int64{uid}})
	})
}

// Move relocates a player and sends enter/leave/move updates to affected watchers.
func (s *Scene) Move(uid int64, x, y float32) {
	s.actor.Post(func() {
		e, ok := s.entities[uid]
		if !ok {
			return
		}
		e.x, e.y = x, y
		entered, left, stayed := s.grid.Move(uid, x, y)

		s.pushTo(stayed, &pb.SceneEntityMovePush{Uid: uid, Position: &pb.Vector2{X: x, Y: y}})
		s.pushEnter(e, entered)
		if len(left) > 0 {
			s.pushTo(left, &pb.SceneEntityLeavePush{Uids: []int64{uid}})
			s.push(e.session, &pb.SceneEntityLeavePush{Uids: left})
		}
	})
}

// BroadcastNearby sends msg to the player and everyone who can see it.
func (s *Scene) BroadcastNearby(uid int64, msg proto.Message) {
	s.actor.Post(func() {
		if _, ok := s.entities[uid]; !ok {
			return
		}
		s.pushTo(append(s.grid.Neighbors(uid), uid), msg)
	})
}

// pushEnter makes e and others mutually visible.
func (s *Scene) pushEnter(e *sceneEntity, others []int64) {
	if len(others) == 0 {
		return
	}
	s.pushTo(others, &pb.SceneEntityEnterPush{Entities: []*pb.SceneEntity{e.toPB()}})
	list := make([]*pb.SceneEntity, 0, len(others))
	for _, uid := range others {
		if o := s.entities[uid]; o != nil {
			list = append(list, o.toPB())
		}
	}
	s.push(e.session, &pb.SceneEntityEnterPush{Entities: list})
}

func (s *Scene) pushTo(uids []int64, msg proto.Message) {
	for _, uid := range uids {
		if o := s.entities[uid]; o != nil {
			s.push(o.session, msg)
		}
	}
}

func (s *Scene) push(sess router.Session, msg proto.Message) {
	if err := s.router.Push(sess, msg); err != nil {
		s.logger.Debug("scene push failed", "err", err)
	}
}
