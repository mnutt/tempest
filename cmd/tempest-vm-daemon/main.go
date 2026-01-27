// Command tempest-vm-daemon runs inside a Linux VM and manages grain containers.
// The host connects via virtio-vsock to spawn and manage grains.
//
// The daemon:
// 1. Listens on vsock port 5000 for host connections
// 2. Manages grain lifecycle (spawn/kill)
// 3. Allocates vsock ports per grain (5001+)
// 4. Bridges host vsock to grain's Unix socket
// 5. Runs sandbox-launcher for each grain
package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"capnproto.org/go/capnp/v3"
	"capnproto.org/go/capnp/v3/rpc"
	"capnproto.org/go/capnp/v3/rpc/transport"
	"golang.org/x/exp/slog"
	"golang.org/x/sys/unix"

	"sandstorm.org/go/tempest/internal/capnp/vmdaemon"
	"sandstorm.org/go/tempest/internal/config"
)

const (
	// DaemonVsockPort is the port the daemon listens on for host connections
	DaemonVsockPort = 5000

	// FirstGrainPort is the first vsock port allocated to grains
	FirstGrainPort = 5001
)

// getLibexecdir returns the libexec directory, checking the environment first.
func getLibexecdir() string {
	if dir := os.Getenv("TEMPEST_LIBEXECDIR"); dir != "" {
		return dir
	}
	return config.Libexecdir
}

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr))

	log.Info("Starting tempest-vm-daemon",
		"daemonPort", DaemonVsockPort,
	)

	daemon := &vmDaemon{
		log:      log,
		grains:   make(map[string]*grainProcess),
		nextPort: FirstGrainPort,
	}

	// Listen on vsock for host connections
	listener, err := listenVsock(DaemonVsockPort)
	if err != nil {
		log.Error("Failed to listen on vsock", "error", err)
		os.Exit(1)
	}
	defer listener.Close()

	log.Info("Listening for host connections", "port", DaemonVsockPort)

	for {
		conn, err := listener.Accept()
		if err != nil {
			log.Error("Failed to accept connection", "error", err)
			continue
		}

		log.Debug("Accepted host connection")
		go daemon.handleConnection(conn)
	}
}

// vmDaemon implements the VmDaemon Cap'n Proto interface
type vmDaemon struct {
	log      *slog.Logger
	mu       sync.Mutex
	grains   map[string]*grainProcess
	nextPort uint32
}

// grainProcess tracks a running grain
type grainProcess struct {
	grainID   string
	packageID string
	vsockPort uint32
	pid       int
	cmd       *exec.Cmd
	sockFile  *os.File
	cancel    context.CancelFunc
}

func (d *vmDaemon) handleConnection(conn net.Conn) {
	defer conn.Close()

	trans := transport.NewStream(conn)
	server := vmdaemon.VmDaemon_ServerToClient(d)
	rpcConn := rpc.NewConn(trans, &rpc.Options{
		BootstrapClient: capnp.Client(server),
	})
	defer rpcConn.Close()

	<-rpcConn.Done()
	d.log.Debug("Host connection closed")
}

func (d *vmDaemon) SpawnGrain(ctx context.Context, call vmdaemon.VmDaemon_spawnGrain) error {
	args := call.Args()
	packageID, err := args.PackageId()
	if err != nil {
		return err
	}
	grainID, err := args.GrainId()
	if err != nil {
		return err
	}
	argsList, err := args.Args()
	if err != nil {
		return err
	}

	// Convert args list
	launchArgs := make([]string, argsList.Len())
	for i := 0; i < argsList.Len(); i++ {
		launchArgs[i], _ = argsList.At(i)
	}

	d.log.Info("Spawning grain",
		"grainID", grainID,
		"packageID", packageID,
	)

	// Allocate a vsock port for this grain
	d.mu.Lock()
	vsockPort := d.nextPort
	d.nextPort++
	d.mu.Unlock()

	// Create socketpair for grain communication
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		return fmt.Errorf("socketpair failed: %w", err)
	}

	grainSock := os.NewFile(uintptr(fds[0]), "grain socket")
	bridgeSock := os.NewFile(uintptr(fds[1]), "bridge socket")

	// Pipe to communicate the grain's PID
	pidR, pidW, err := os.Pipe()
	if err != nil {
		grainSock.Close()
		bridgeSock.Close()
		return fmt.Errorf("pipe failed: %w", err)
	}

	// Build command arguments
	cmdArgs := append([]string{packageID, grainID}, launchArgs...)
	cmd := exec.Command(getLibexecdir()+"/tempest/tempest-sandbox-launcher", cmdArgs...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.ExtraFiles = []*os.File{grainSock, pidW}

	if err := cmd.Start(); err != nil {
		grainSock.Close()
		bridgeSock.Close()
		pidR.Close()
		pidW.Close()
		return fmt.Errorf("failed to start sandbox-launcher: %w", err)
	}

	grainSock.Close()
	pidW.Close()

	// Read grain PID
	pidBuf, err := io.ReadAll(pidR)
	pidR.Close()
	if err != nil {
		cmd.Process.Kill()
		bridgeSock.Close()
		return fmt.Errorf("failed to read grain PID: %w", err)
	}

	grainPid, err := strconv.Atoi(string(pidBuf))
	if err != nil {
		cmd.Process.Kill()
		bridgeSock.Close()
		return fmt.Errorf("invalid grain PID: %w", err)
	}

	d.log.Debug("Grain process started",
		"grainID", grainID,
		"pid", grainPid,
		"vsockPort", vsockPort,
	)

	// Start listening on the vsock port for this grain
	ctx, cancel := context.WithCancel(context.Background())
	proc := &grainProcess{
		grainID:   grainID,
		packageID: packageID,
		vsockPort: vsockPort,
		pid:       grainPid,
		cmd:       cmd,
		sockFile:  bridgeSock,
		cancel:    cancel,
	}

	d.mu.Lock()
	d.grains[grainID] = proc
	d.mu.Unlock()

	// Start vsock listener for this grain
	go d.serveGrainVsock(ctx, proc)

	// Set result
	results, err := call.AllocResults()
	if err != nil {
		return err
	}
	results.SetVsockPort(vsockPort)

	return nil
}

func (d *vmDaemon) serveGrainVsock(ctx context.Context, proc *grainProcess) {
	listener, err := listenVsock(int(proc.vsockPort))
	if err != nil {
		d.log.Error("Failed to listen on grain vsock",
			"grainID", proc.grainID,
			"port", proc.vsockPort,
			"error", err,
		)
		return
	}
	defer listener.Close()

	d.log.Debug("Listening for grain connections",
		"grainID", proc.grainID,
		"port", proc.vsockPort,
	)

	// Accept ONE connection and bridge to grain socket.
	// Cap'n Proto RPC uses a single bidirectional connection,
	// so we only accept one vsock connection per grain.
	select {
	case <-ctx.Done():
		return
	default:
	}

	conn, err := listener.Accept()
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		d.log.Error("Failed to accept grain connection",
			"grainID", proc.grainID,
			"error", err,
		)
		return
	}

	d.log.Debug("Accepted grain vsock connection",
		"grainID", proc.grainID,
		"port", proc.vsockPort,
	)

	// Bridge the vsock connection to the grain's unix socket
	d.bridgeConnection(ctx, conn, proc.sockFile, proc.grainID)
}

func (d *vmDaemon) bridgeConnection(ctx context.Context, vsockConn net.Conn, grainSock *os.File, grainID string) {
	defer vsockConn.Close()
	// Note: grainSock is NOT closed here - it's owned by the grainProcess
	// and will be closed when KillGrain is called

	// Bridge data in both directions
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		io.Copy(vsockConn, grainSock)
	}()

	go func() {
		defer wg.Done()
		io.Copy(grainSock, vsockConn)
	}()

	// Wait for either direction to complete (connection closed)
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		d.log.Debug("Grain connection closed", "grainID", grainID)
	case <-ctx.Done():
		d.log.Debug("Grain context cancelled", "grainID", grainID)
	}
}

func (d *vmDaemon) KillGrain(ctx context.Context, call vmdaemon.VmDaemon_killGrain) error {
	args := call.Args()
	grainID, err := args.GrainId()
	if err != nil {
		return err
	}

	d.log.Info("Killing grain", "grainID", grainID)

	d.mu.Lock()
	proc, ok := d.grains[grainID]
	if ok {
		delete(d.grains, grainID)
	}
	d.mu.Unlock()

	if !ok {
		return fmt.Errorf("grain not found: %s", grainID)
	}

	proc.cancel()
	proc.sockFile.Close()

	if proc.cmd.Process != nil {
		proc.cmd.Process.Kill()
		proc.cmd.Wait()
	}

	return nil
}

func (d *vmDaemon) ListGrains(ctx context.Context, call vmdaemon.VmDaemon_listGrains) error {
	d.mu.Lock()
	grainList := make([]*grainProcess, 0, len(d.grains))
	for _, g := range d.grains {
		grainList = append(grainList, g)
	}
	d.mu.Unlock()

	results, err := call.AllocResults()
	if err != nil {
		return err
	}

	list, err := results.NewGrains(int32(len(grainList)))
	if err != nil {
		return err
	}

	for i, g := range grainList {
		info := list.At(i)
		info.SetGrainId(g.grainID)
		info.SetPackageId(g.packageID)
		info.SetVsockPort(g.vsockPort)
		info.SetPid(int32(g.pid))
	}

	return nil
}

// vsockListener wraps a vsock socket for accepting connections
type vsockListener struct {
	fd   int
	port int
}

var vsockListenerCounter atomic.Int64

func listenVsock(port int) (*vsockListener, error) {
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM, 0)
	if err != nil {
		return nil, fmt.Errorf("socket: %w", err)
	}

	addr := &unix.SockaddrVM{
		CID:  unix.VMADDR_CID_ANY,
		Port: uint32(port),
	}

	if err := unix.Bind(fd, addr); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("bind: %w", err)
	}

	if err := unix.Listen(fd, 128); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("listen: %w", err)
	}

	return &vsockListener{fd: fd, port: port}, nil
}

func (l *vsockListener) Accept() (net.Conn, error) {
	connFd, _, err := unix.Accept(l.fd)
	if err != nil {
		return nil, err
	}

	file := os.NewFile(uintptr(connFd), fmt.Sprintf("vsock-%d-%d", l.port, vsockListenerCounter.Add(1)))
	return &vsockConn{file: file}, nil
}

func (l *vsockListener) Close() error {
	return unix.Close(l.fd)
}

func (l *vsockListener) Addr() net.Addr {
	return &vsockAddr{port: l.port}
}

// vsockConn wraps a vsock connection
type vsockConn struct {
	file *os.File
}

func (c *vsockConn) Read(b []byte) (int, error)  { return c.file.Read(b) }
func (c *vsockConn) Write(b []byte) (int, error) { return c.file.Write(b) }
func (c *vsockConn) Close() error                { return c.file.Close() }
func (c *vsockConn) LocalAddr() net.Addr         { return &vsockAddr{} }
func (c *vsockConn) RemoteAddr() net.Addr        { return &vsockAddr{} }

func (c *vsockConn) SetDeadline(t time.Time) error      { return c.file.SetDeadline(t) }
func (c *vsockConn) SetReadDeadline(t time.Time) error  { return c.file.SetReadDeadline(t) }
func (c *vsockConn) SetWriteDeadline(t time.Time) error { return c.file.SetWriteDeadline(t) }

// vsockAddr implements net.Addr for vsock
type vsockAddr struct {
	cid  uint32
	port int
}

func (a *vsockAddr) Network() string { return "vsock" }
func (a *vsockAddr) String() string  { return fmt.Sprintf("vsock:%d:%d", a.cid, a.port) }
