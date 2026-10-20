// Package logx configures structured logging for the whole process.
//
// It is a thin layer over the standard library log/slog package so that every
// module can simply use slog (or a *slog.Logger obtained from this package)
// without depending on a third-party logging library.
package logx

import (
	"io"
	"log/slog"
	"os"
	"strings"
)

// Config controls the global logger.
type Config struct {
	// Level is one of debug, info, warn, error. Default: info.
	Level string `yaml:"level"`
	// Format is "json" (production, machine readable) or "text" (development). Default: text.
	Format string `yaml:"format"`
	// AddSource adds the file:line of the log call to every record.
	AddSource bool `yaml:"add_source"`
}

// ParseLevel converts a textual level into a slog.Level. Unknown values map to info.
func ParseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// New builds a logger writing to w according to cfg.
func New(cfg Config, w io.Writer) *slog.Logger {
	if w == nil {
		w = os.Stdout
	}
	opts := &slog.HandlerOptions{Level: ParseLevel(cfg.Level), AddSource: cfg.AddSource}
	var h slog.Handler
	if strings.EqualFold(cfg.Format, "json") {
		h = slog.NewJSONHandler(w, opts)
	} else {
		h = slog.NewTextHandler(w, opts)
	}
	return slog.New(h)
}

// Setup builds a logger, installs it as the slog default and returns it.
func Setup(cfg Config) *slog.Logger {
	l := New(cfg, os.Stdout)
	slog.SetDefault(l)
	return l
}

// Module returns a child logger tagged with a "module" attribute.
// Every framework component uses it so log lines can be filtered per module.
func Module(name string) *slog.Logger {
	return slog.Default().With(slog.String("module", name))
}
