// Command server runs a game server node.
//
//	go run ./cmd/server -config configs/allinone.yaml
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"

	"gameserver/framework/config"
	"gameserver/framework/logx"
	"gameserver/game/bootstrap"
)

func main() {
	path := flag.String("config", "configs/allinone.yaml", "path to the YAML configuration file")
	flag.Parse()

	var cfg bootstrap.Config
	if err := config.Load(*path, &cfg); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	logx.Setup(cfg.Log)
	slog.Info("starting node", "id", cfg.Node.ID, "roles", cfg.Node.Roles,
		"bus", cfg.Cluster.Bus, "registry", cfg.Cluster.Registry, "storage", cfg.Storage.Driver)

	srv, err := bootstrap.Build(&cfg)
	if err != nil {
		slog.Error("build failed", "err", err)
		os.Exit(1)
	}
	if err := srv.App.Run(context.Background()); err != nil {
		slog.Error("server exited with error", "err", err)
		os.Exit(1)
	}
	slog.Info("bye")
}
