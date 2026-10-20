// Package loginsvc authenticates accounts and assigns each player to a game node.
//
// The login service is stateless: any instance can handle any request, so the
// gate load-balances login requests over a queue group. After a successful
// login it binds the client session to (uid, game node) on the gate, which then
// routes every subsequent game request to that node.
package loginsvc

import (
	"context"
	"errors"
	"log/slog"
	"regexp"
	"time"

	"golang.org/x/crypto/bcrypt"
	"google.golang.org/protobuf/proto"

	"gameserver/framework/cluster"
	"gameserver/framework/errcode"
	"gameserver/framework/idgen"
	"gameserver/framework/logx"
	"gameserver/framework/message"
	"gameserver/framework/pb/clusterpb"
	"gameserver/framework/router"
	"gameserver/game/model"
	"gameserver/game/pb"
	"gameserver/game/presence"
	"gameserver/game/protocol"
	"gameserver/game/repo"
)

// Config configures the login service.
type Config struct {
	// BcryptCost is the password hashing cost. Default bcrypt.DefaultCost (10).
	BcryptCost int `yaml:"bcrypt_cost"`
	// MaxConcurrent bounds parallel logins per node (hashing is CPU heavy). Default 256.
	MaxConcurrent int `yaml:"max_concurrent"`
	// RequestTimeout bounds one login request. Default 10s.
	RequestTimeout time.Duration `yaml:"request_timeout"`
}

var (
	accountPattern = regexp.MustCompile(`^[A-Za-z0-9_]{3,32}$`)

	errInvalidAccount  = errcode.New(int32(pb.ErrCode_ERR_INVALID_ACCOUNT), "account must be 3-32 letters, digits or _")
	errInvalidPassword = errcode.New(int32(pb.ErrCode_ERR_INVALID_PASSWORD), "password must be 6-64 characters")
	errWrongPassword   = errcode.New(int32(pb.ErrCode_ERR_WRONG_PASSWORD), "wrong password")
	errAlreadyLoggedIn = errcode.New(errcode.ErrBadRequest.Code, "session already logged in")
)

// Service is an app.Module.
type Service struct {
	cfg      Config
	cluster  *cluster.Cluster
	router   *router.Router
	accounts repo.AccountRepo
	presence presence.Directory
	ids      *idgen.Snowflake
	logger   *slog.Logger

	sem chan struct{}
	sub cluster.Subscription
}

// New creates the login service.
func New(cfg Config, c *cluster.Cluster, reg *message.Registry, accounts repo.AccountRepo, dir presence.Directory, ids *idgen.Snowflake) *Service {
	if cfg.BcryptCost == 0 {
		cfg.BcryptCost = bcrypt.DefaultCost
	}
	if cfg.MaxConcurrent <= 0 {
		cfg.MaxConcurrent = 256
	}
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = 10 * time.Second
	}
	s := &Service{
		cfg:      cfg,
		cluster:  c,
		router:   router.New(protocol.ServiceLogin, reg, c),
		accounts: accounts,
		presence: dir,
		ids:      ids,
		logger:   logx.Module("login"),
		sem:      make(chan struct{}, cfg.MaxConcurrent),
	}
	s.router.Use(router.AccessLog(s.logger, 500*time.Millisecond))
	router.Handle(s.router, uint32(pb.MsgID_LOGIN_REQ), s.handleLogin)
	return s
}

func (s *Service) Name() string { return protocol.ServiceLogin }

func (s *Service) Init(context.Context) error { return nil }

func (s *Service) Start(ctx context.Context) error {
	// Queue group: each login request goes to exactly one login instance.
	sub, err := s.cluster.SubscribeEnvelopes(cluster.ServiceSubject(protocol.ServiceLogin), protocol.ServiceLogin, s.onEnvelope)
	if err != nil {
		return err
	}
	s.sub = sub
	return s.cluster.Registry().Register(ctx, protocol.ServiceLogin, s.cluster.NodeID())
}

func (s *Service) Stop(ctx context.Context) error {
	_ = s.cluster.Registry().Deregister(ctx, protocol.ServiceLogin, s.cluster.NodeID())
	if s.sub != nil {
		_ = s.sub.Unsubscribe()
	}
	// Wait for in-flight logins by acquiring every slot.
	for range cap(s.sem) {
		select {
		case s.sem <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (s *Service) onEnvelope(env *clusterpb.Envelope) {
	if env.Kind != clusterpb.Kind_KIND_FORWARD {
		return
	}
	s.sem <- struct{}{} // backpressure: block the bus callback when saturated
	go func() {
		defer func() { <-s.sem }()
		ctx, cancel := context.WithTimeout(context.Background(), s.cfg.RequestTimeout)
		defer cancel()
		s.router.Dispatch(ctx, env, nil)
	}()
}

func (s *Service) handleLogin(c *router.Context, req *pb.LoginReq) (proto.Message, error) {
	if c.UID() != 0 {
		return nil, errAlreadyLoggedIn
	}
	if !accountPattern.MatchString(req.Account) {
		return nil, errInvalidAccount
	}
	if n := len(req.Password); n < 6 || n > 64 {
		return nil, errInvalidPassword
	}

	acc, isNew, err := s.findOrCreate(c, req)
	if err != nil {
		return nil, err
	}

	node, err := s.pickGameNode(c, acc.UID)
	if err != nil {
		return nil, err
	}
	// Bind first, then reply: both go to the same gate subject from this
	// goroutine, so the gate has the binding before the client sees LoginResp.
	if err := s.router.Bind(c.Session, acc.UID, map[string]string{protocol.ServiceGame: node}); err != nil {
		return nil, err
	}
	s.logger.Info("login ok", "account", acc.Name, "uid", acc.UID, "game_node", node, "new", isNew)
	return &pb.LoginResp{Uid: acc.UID, NewAccount: isNew, ServerTimeMs: time.Now().UnixMilli()}, nil
}

// pickGameNode chooses the game node for uid:
//
//  1. If the player is online, the node that owns it (active or draining), so
//     a reconnect finds the in-memory state and never loads a second copy.
//  2. Otherwise rendezvous hashing over active nodes only: draining nodes
//     receive no new players.
func (s *Service) pickGameNode(ctx context.Context, uid int64) (string, error) {
	owner, err := s.presence.Owner(ctx, uid)
	if err != nil {
		// Without the directory we cannot rule out a second copy: fail closed.
		s.logger.Error("presence lookup failed", "uid", uid, "err", err)
		return "", errcode.ErrServiceUnavailable
	}
	if owner != "" && presence.GameNodeAlive(s.cluster.Registry(), owner) {
		return owner, nil
	}
	node, ok := s.cluster.PickNode(protocol.ServiceGame, uint64(uid))
	if !ok {
		return "", errcode.ErrServiceUnavailable
	}
	return node, nil
}

// findOrCreate loads the account, auto-registering unknown names (demo behaviour;
// production games usually authenticate against a platform SDK instead).
func (s *Service) findOrCreate(ctx context.Context, req *pb.LoginReq) (*model.Account, bool, error) {
	acc, err := s.accounts.Get(ctx, req.Account)
	if errors.Is(err, repo.ErrNotFound) {
		hash, herr := bcrypt.GenerateFromPassword([]byte(req.Password), s.cfg.BcryptCost)
		if herr != nil {
			return nil, false, herr
		}
		acc = &model.Account{Name: req.Account, PasswordHash: hash, UID: s.ids.Next(), CreatedAt: time.Now()}
		err = s.accounts.Create(ctx, acc)
		if err == nil {
			return acc, true, nil
		}
		if !errors.Is(err, repo.ErrDuplicate) {
			return nil, false, err
		}
		// Lost a registration race with a concurrent login: use the winner's account.
		acc, err = s.accounts.Get(ctx, req.Account)
	}
	if err != nil {
		return nil, false, err
	}
	if bcrypt.CompareHashAndPassword(acc.PasswordHash, []byte(req.Password)) != nil {
		return nil, false, errWrongPassword
	}
	return acc, false, nil
}
