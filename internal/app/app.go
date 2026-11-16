// Package app holds the startup sequence shared by macrod and macroctl.
package app

import (
	"fmt"
	"io"
	"log/slog"

	"macro-collector/internal/config"
	"macro-collector/internal/logging"
)

// Env is what every binary needs after startup.
type Env struct {
	Config *config.Config
	Root   *slog.Logger
	closer io.Closer
}

// Setup loads .env, the config and the loggers. name is the binary name and
// is used for the log file and as the source of startup records.
func Setup(name, configPath, envPath, dataDir string) (*Env, error) {
	if err := config.LoadDotEnv(envPath); err != nil {
		return nil, err
	}
	cfg, err := config.LoadWithDataDir(configPath, dataDir)
	if err != nil {
		return nil, err
	}
	root, closer, err := logging.New(logging.Options{Dir: cfg.LogDir(), App: name, Level: slog.LevelInfo, Location: cfg.Location})
	if err != nil {
		return nil, fmt.Errorf("%s setup: %w", name, err)
	}
	for _, d := range cfg.Deprecations() {
		logging.For(root, name).Warn("config: " + d)
	}
	return &Env{Config: cfg, Root: root, closer: closer}, nil
}

// Close releases the log file.
func (e *Env) Close() error {
	return e.closer.Close()
}
