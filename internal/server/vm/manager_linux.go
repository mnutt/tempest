//go:build linux

package vm

import (
	"context"
	"fmt"
	"net"

	"golang.org/x/exp/slog"
)

// LinuxManager is a stub VM manager for Linux.
// On Linux, we typically use the local backend instead of a VM,
// so this manager just returns errors.
type LinuxManager struct {
	log *slog.Logger
}

// NewLinuxManager creates a new stub VM manager for Linux.
func NewLinuxManager(log *slog.Logger, cfg Config) *LinuxManager {
	return &LinuxManager{log: log}
}

func (m *LinuxManager) Start(ctx context.Context) error {
	return fmt.Errorf("VM manager not needed on Linux - use local backend")
}

func (m *LinuxManager) Stop(ctx context.Context) error {
	return nil
}

func (m *LinuxManager) DialDaemon(ctx context.Context) (net.Conn, error) {
	return nil, fmt.Errorf("VM manager not available on Linux")
}

func (m *LinuxManager) DialPort(ctx context.Context, port uint32) (net.Conn, error) {
	return nil, fmt.Errorf("VM manager not available on Linux")
}

func (m *LinuxManager) IsRunning() bool {
	return false
}

// Ensure LinuxManager implements Manager
var _ Manager = (*LinuxManager)(nil)
