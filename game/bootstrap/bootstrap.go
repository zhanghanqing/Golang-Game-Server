// Package bootstrap assembles a server process from configuration.
//
// A node runs one or more roles (gate, login, game). In all-in-one mode one
// process runs all three over the in-process bus; in distributed mode each
// process runs a subset and they talk over NATS. Build creates the modules in
// dependency order:
//
//	redis? -> mongo? -> cluster -> admin -> game? -> login? -> gate? -> ready
//
// and app.App stops them in reverse order, so the gate stops accepting
// clients first and storage connections close last.
package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"slices"

	"gameserver/framework/admin"
	"gameserver/framework/app"
	"gameserver/framework/cluster"
	"gameserver/framework/gate"
	"gameserver/framework/idgen"
	"gameserver/framework/logx"
	"gameserver/framework/storage/mongox"
	"gameserver/framework/storage/redisx"
	"gameserver/game/gamedata"
	"gameserver/game/gamesvc"
	"gameserver/game/loginsvc"
	"gameserver/game/presence"
	"gameserver/game/protocol"
	"gameserver/game/repo"
	"gameserver/game/webconsole"
)

// Role names.
const (
	RoleGate  = "gate"
	RoleLogin = "login"
	RoleGame  = "game"
)

// NodeConfig identifies this process.
type NodeConfig struct {
	// ID must be unique in the cluster, e.g. "game-1". Allowed: letters, digits, '-', '_'.
	ID string `yaml:"id"`
	// Roles this process runs: any of gate, login, game.
	Roles []string `yaml:"roles"`
	// WorkerID is the Snowflake worker id (0-1023), unique per process that generates ids.
	WorkerID int64 `yaml:"worker_id"`
}

// StorageConfig selects the persistence backend.
type StorageConfig struct {
	// Driver is "memory" (default, data lost on exit) or "mongo".
	Driver string `yaml:"driver"`
}

// ConsoleConfig controls the browser test console served by the admin server.
type ConsoleConfig struct {
	// Enabled mounts the console at http://<admin.addr>/console/. Development/QA only.
	Enabled bool `yaml:"enabled"`
}

// Config is the root configuration file.
type Config struct {
	Node    NodeConfig      `yaml:"node"`
	Log     logx.Config     `yaml:"log"`
	Cluster cluster.Config  `yaml:"cluster"`
	Storage StorageConfig   `yaml:"storage"`
	Redis   redisx.Config   `yaml:"redis"`
	Mongo   mongox.Config   `yaml:"mongo"`
	Admin   admin.Config    `yaml:"admin"`
	Console ConsoleConfig   `yaml:"console"`
	Gate    gate.Config     `yaml:"gate"`
	Login   loginsvc.Config `yaml:"login"`
	Game    gamesvc.Config  `yaml:"game"`
}

// Has reports whether the node runs role.
func (c *Config) Has(role string) bool { return slices.Contains(c.Node.Roles, role) }

// Validate checks cross-field constraints. It is called by config.Load.
func (c *Config) Validate() error {
	if c.Node.ID == "" {
		return errors.New("node.id is required")
	}
	if len(c.Node.Roles) == 0 {
		return errors.New("node.roles must not be empty")
	}
	for _, r := range c.Node.Roles {
		if r != RoleGate && r != RoleLogin && r != RoleGame {
			return fmt.Errorf("unknown role %q", r)
		}
	}
	if (c.Cluster.Bus == "" || c.Cluster.Bus == "local") &&
		!(c.Has(RoleGate) && c.Has(RoleLogin) && c.Has(RoleGame)) {
		return errors.New("cluster.bus=local requires roles gate, login and game in the same process")
	}
	if c.Cluster.Bus == "nats" {
		// Across processes, memory-based components would silently diverge.
		if c.Cluster.Registry != "redis" {
			return errors.New("cluster.bus=nats requires cluster.registry=redis")
		}
		if (c.Has(RoleLogin) || c.Has(RoleGame)) && c.Storage.Driver != "mongo" {
			return errors.New("cluster.bus=nats requires storage.driver=mongo for login/game roles")
		}
	}
	return nil
}

// Server is the assembled process. Exposed fields help tests and tools.
type Server struct {
	App      *app.App
	Cluster  *cluster.Cluster
	Admin    *admin.Server
	Gate     *gate.Gate
	Login    *loginsvc.Service
	Game     *gamesvc.Service
	Accounts repo.AccountRepo
	Players  repo.PlayerRepo
}

// Option customizes Build.
type Option func(*options)

type options struct {
	accounts repo.AccountRepo
	players  repo.PlayerRepo
}

// WithRepos overrides the storage driver with the given repositories. It is
// used by tests (several nodes in one process sharing one store) and by
// projects plugging in their own persistence.
func WithRepos(accounts repo.AccountRepo, players repo.PlayerRepo) Option {
	return func(o *options) { o.accounts, o.players = accounts, players }
}

// Build creates every module required by cfg.
func Build(cfg *Config, opts ...Option) (*Server, error) {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	reg := protocol.Registry()
	s := &Server{App: app.New(app.Options{Logger: logx.Module("app")})}

	var rdb *redisx.Module
	if cfg.Cluster.Registry == "redis" {
		rdb = redisx.New(cfg.Redis)
		s.App.Add(rdb)
	}

	switch {
	case o.accounts != nil && o.players != nil:
		s.Accounts, s.Players = o.accounts, o.players
	case cfg.Storage.Driver == "" || cfg.Storage.Driver == "memory":
		s.Accounts, s.Players = repo.NewMemoryAccounts(), repo.NewMemoryPlayers()
	case cfg.Storage.Driver == "mongo":
		m, err := mongox.New(cfg.Mongo)
		if err != nil {
			return nil, err
		}
		s.App.Add(m)
		s.Accounts, s.Players = repo.NewMongoAccounts(m.DB()), repo.NewMongoPlayers(m.DB())
	default:
		return nil, fmt.Errorf("unknown storage driver %q", cfg.Storage.Driver)
	}

	// Presence must be shared by every login and game node, so it lives in
	// Redis whenever the cluster spans processes.
	var dir presence.Directory
	if rdb != nil {
		s.Cluster = cluster.New(cfg.Node.ID, cfg.Cluster, rdb.Client())
		dir = presence.NewRedis(rdb.Client(), 0)
	} else {
		s.Cluster = cluster.New(cfg.Node.ID, cfg.Cluster, nil)
		dir = presence.NewMemory(0)
	}
	s.Admin = admin.New(cfg.Admin)
	s.App.Add(s.Cluster, s.Admin)

	tables, err := gamedata.Load()
	if err != nil {
		return nil, err
	}
	if cfg.Console.Enabled {
		mountConsole(s.Admin, cfg, tables)
	}

	if cfg.Has(RoleGame) {
		s.Game = gamesvc.New(cfg.Game, s.Cluster, reg, s.Players, dir, tables)
		s.App.Add(s.Game)
		mountGameOps(s.Admin, s.Game)
	}
	if cfg.Has(RoleLogin) {
		ids, err := idgen.NewSnowflake(cfg.Node.WorkerID)
		if err != nil {
			return nil, err
		}
		s.Login = loginsvc.New(cfg.Login, s.Cluster, reg, s.Accounts, dir, ids)
		s.App.Add(s.Login)
	}
	if cfg.Has(RoleGate) {
		s.Gate = gate.New(cfg.Gate, s.Cluster, reg, protocol.GateRoutes())
		s.App.Add(s.Gate)
	}

	// Last module: flip readiness once everything is up, and first thing on shutdown.
	s.App.Add(app.Func{
		ModuleName: "ready",
		OnStart:    func(context.Context) error { s.Admin.SetReady(true); return nil },
		OnStop:     func(context.Context) error { s.Admin.SetReady(false); return nil },
	})
	return s, nil
}

// mountConsole serves the web console on the admin server and redirects "/" to it.
func mountConsole(adm *admin.Server, cfg *Config, tables *gamedata.Tables) {
	opts := webconsole.Options{WSPath: cfg.Gate.WSPath, Tables: tables}
	if _, port, err := net.SplitHostPort(cfg.Gate.WSAddr); err == nil {
		opts.WSPort = port
	}
	adm.Handle(webconsole.Prefix, webconsole.Handler(protocol.Registry(), opts))
	adm.Handle("/{$}", http.RedirectHandler(webconsole.Prefix, http.StatusFound))
}

// mountGameOps adds the endpoints used by the Kubernetes operator:
//
//	GET  /game/status  {"node":"game-1","players":42,"draining":false,"stopping":false}
//	POST /game/drain   stop receiving new players; returns the new status
//
// They live on the internal admin port, like /metrics.
func mountGameOps(adm *admin.Server, g *gamesvc.Service) {
	writeStatus := func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(g.Status())
	}
	adm.Handle("GET /game/status", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeStatus(w)
	}))
	adm.Handle("POST /game/drain", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := g.Drain(r.Context()); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeStatus(w)
	}))
}
