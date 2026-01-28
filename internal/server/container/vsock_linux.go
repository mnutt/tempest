//go:build linux

package container

import (
	"context"
	"fmt"
	"net"
	"os"
	"time"

	"golang.org/x/exp/slog"
	"golang.org/x/sys/unix"
)

// LinuxVsockDialer implements VsockDialer using Linux vsock.
// This can be used to connect to a VM running on the same host.
type LinuxVsockDialer struct {
	log *slog.Logger
	cid uint32 // VM's Context ID
}

// NewLinuxVsockDialer creates a new vsock dialer for Linux.
func NewLinuxVsockDialer(log *slog.Logger, vmCID uint32) *LinuxVsockDialer {
	return &LinuxVsockDialer{
		log: log,
		cid: vmCID,
	}
}

// DialDaemon connects to the VM daemon's control socket.
func (d *LinuxVsockDialer) DialDaemon(ctx context.Context) (net.Conn, error) {
	return d.dial(ctx, 5000) // DaemonVsockPort
}

// DialGrain connects to a specific grain's vsock port.
func (d *LinuxVsockDialer) DialGrain(ctx context.Context, port uint32) (net.Conn, error) {
	return d.dial(ctx, port)
}

func (d *LinuxVsockDialer) dial(ctx context.Context, port uint32) (net.Conn, error) {
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM, 0)
	if err != nil {
		return nil, fmt.Errorf("socket: %w", err)
	}

	addr := &unix.SockaddrVM{
		CID:  d.cid,
		Port: port,
	}

	// Set non-blocking for context cancellation support
	if err := unix.SetNonblock(fd, true); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("setnonblock: %w", err)
	}

	err = unix.Connect(fd, addr)
	if err != nil && err != unix.EINPROGRESS {
		unix.Close(fd)
		return nil, fmt.Errorf("connect: %w", err)
	}

	// Wait for connection to complete
	if err == unix.EINPROGRESS {
		// Use poll to wait for connection with context
		pollFds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLOUT}}

		for {
			select {
			case <-ctx.Done():
				unix.Close(fd)
				return nil, ctx.Err()
			default:
			}

			n, err := unix.Poll(pollFds, 100) // 100ms timeout
			if err != nil {
				if err == unix.EINTR {
					continue
				}
				unix.Close(fd)
				return nil, fmt.Errorf("poll: %w", err)
			}

			if n > 0 {
				// Check for errors
				val, err := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_ERROR)
				if err != nil {
					unix.Close(fd)
					return nil, fmt.Errorf("getsockopt: %w", err)
				}
				if val != 0 {
					unix.Close(fd)
					return nil, fmt.Errorf("connect failed: %v", unix.Errno(val))
				}
				break
			}
		}
	}

	// Set back to blocking
	if err := unix.SetNonblock(fd, false); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("setnonblock: %w", err)
	}

	file := os.NewFile(uintptr(fd), fmt.Sprintf("vsock:%d:%d", d.cid, port))
	return &linuxVsockConn{file: file, cid: d.cid, port: port}, nil
}

// linuxVsockConn wraps a vsock connection on Linux.
type linuxVsockConn struct {
	file *os.File
	cid  uint32
	port uint32
}

func (c *linuxVsockConn) Read(b []byte) (int, error)  { return c.file.Read(b) }
func (c *linuxVsockConn) Write(b []byte) (int, error) { return c.file.Write(b) }
func (c *linuxVsockConn) Close() error                { return c.file.Close() }

func (c *linuxVsockConn) LocalAddr() net.Addr {
	return &vsockAddr{cid: unix.VMADDR_CID_LOCAL, port: 0}
}

func (c *linuxVsockConn) RemoteAddr() net.Addr {
	return &vsockAddr{cid: c.cid, port: c.port}
}

func (c *linuxVsockConn) SetDeadline(t time.Time) error      { return c.file.SetDeadline(t) }
func (c *linuxVsockConn) SetReadDeadline(t time.Time) error  { return c.file.SetReadDeadline(t) }
func (c *linuxVsockConn) SetWriteDeadline(t time.Time) error { return c.file.SetWriteDeadline(t) }

// vsockAddr implements net.Addr for vsock.
type vsockAddr struct {
	cid  uint32
	port uint32
}

func (a *vsockAddr) Network() string { return "vsock" }
func (a *vsockAddr) String() string  { return fmt.Sprintf("vsock:%d:%d", a.cid, a.port) }

// Ensure LinuxVsockDialer implements VsockDialer
var _ VsockDialer = (*LinuxVsockDialer)(nil)
