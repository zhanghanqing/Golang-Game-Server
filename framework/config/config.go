// Package config loads YAML configuration files with environment variable expansion.
//
// The framework does not impose one global configuration struct. Instead every
// component exposes its own Config type and the application composes them into
// a root struct that is passed to Load.
//
// Values may reference environment variables using ${NAME} or ${NAME:-default}.
// This makes the same file usable on a developer machine and inside containers.
package config

import (
	"bytes"
	"fmt"
	"os"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Load reads the YAML file at path, expands environment variables and decodes it into out.
// Unknown keys are rejected so that typos in configuration files are caught at startup.
func Load(path string, out any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("config: read %s: %w", path, err)
	}
	return Decode(raw, out)
}

// Decode expands environment variables in raw and decodes the YAML document into out.
func Decode(raw []byte, out any) error {
	expanded := ExpandEnv(string(raw))
	dec := yaml.NewDecoder(bytes.NewReader([]byte(expanded)))
	dec.KnownFields(true)
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("config: decode: %w", err)
	}
	if v, ok := out.(interface{ Validate() error }); ok {
		if err := v.Validate(); err != nil {
			return fmt.Errorf("config: invalid: %w", err)
		}
	}
	return nil
}

// ExpandEnv replaces ${NAME} and ${NAME:-default} with values from the environment.
// $NAME without braces is also supported (standard os.Expand behaviour).
func ExpandEnv(s string) string {
	return os.Expand(s, func(key string) string {
		name, def, hasDefault := strings.Cut(key, ":-")
		if v, ok := os.LookupEnv(name); ok && v != "" {
			return v
		}
		if hasDefault {
			return def
		}
		return ""
	})
}
