//go:build linux

package container

import (
	"golang.org/x/exp/slog"
)

// NewDefaultBackend creates the default container backend for the current platform.
// On Linux, this returns a LocalBackend that spawns containers directly.
func NewDefaultBackend(log *slog.Logger) (Backend, error) {
	return NewLocalBackend(log), nil
}
