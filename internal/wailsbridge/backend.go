package wailsbridge

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
)

// BackendContract is the supervisor contract the desktop shell (nested
// cmd/canopy-desktop) drives.
//
// Only ONE backend runs per desktop session; the shell owns its lifecycle:
// start (StartBackend), readiness (PollBackendHealth), graceful stop (Stop),
// and the exit signal (Exited).
type BackendContract interface {
	// Addr returns the loopback host:port the backend serves on.
	Addr() string
	// Exited returns a channel closed once the backend process has exited.
	Exited() <-chan struct{}
	// Stop terminates the backend: SIGTERM, then SIGKILL after the grace
	// period. Safe to call more than once.
	Stop() error
}

// BackendOptions configures StartBackend.
type BackendOptions struct {
	// Binary is the canopyd executable. Empty resolves "canopyd" from PATH.
	Binary string

	// Addr is the loopback host:port for the backend to serve on. Empty
	// defaults to 127.0.0.1:8091 (the project's dev default). The address
	// must be loopback: the desktop shell wraps a local server, and a wider
	// bind would expose the API beyond the machine by accident.
	Addr string

	// Env carries extra environment entries for the child (DB connection,
	// JWT secret, …) in KEY=VALUE form, appended after the parent env so
	// they win. canopyd's configuration contract (HTTP_ADDR, DB env, …)
	// applies unchanged — the desktop shell is a supervisor, not a second
	// config surface.
	Env []string
}

// defaultBackendAddr is the project's dev default (Makefile HTTP_ADDR).
const defaultBackendAddr = "127.0.0.1:8091"

// backendHealthTimeout bounds how long StartBackend waits for the child's
// /health to answer before declaring the desktop launch failed.
const backendHealthTimeout = 30 * time.Second

// backendStopGrace bounds the SIGTERM→SIGKILL window in Stop.
const backendStopGrace = 10 * time.Second

// StartBackend resolves the canopyd binary, starts it as a supervised child
// (args: serve, HTTP_ADDR=<addr>), and health-polls /health until it answers
// 2xx. The child inherits the parent environment plus opts.Env, and is
// started with update checks disabled: canopyd's updater is opt-in via
// -update-interval, which the desktop shell never passes — exactly one
// updater runs per binary in desktop mode (SPEC-FTR-06 risk 14).
func StartBackend(ctx context.Context, opts BackendOptions) (*SupervisedBackend, error) {
	addr := opts.Addr
	if strings.TrimSpace(addr) == "" {
		addr = defaultBackendAddr
	}
	if err := ValidateBackendAddr(addr); err != nil {
		return nil, err
	}
	binary, err := ResolveBackendBinary(opts.Binary)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(binary, backendServeArgs()...) //nolint:gosec // binary resolved through PATH/explicit flag by design
	cmd.Env = append(os.Environ(), opts.Env...)
	cmd.Env = append(cmd.Env, "HTTP_ADDR="+addr)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	// WaitDelay unblocks Wait() when the child exits but grandchildren keep
	// the inherited pipes open (a crashed backend leaving orphans behind
	// must not wedge the desktop shell's supervisor goroutine).
	cmd.WaitDelay = 5 * time.Second
	setBackendProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("wailsbridge: start %s: %w", binary, err)
	}
	backend := &SupervisedBackend{
		addr:   addr,
		binary: binary,
		cmd:    cmd,
		exited: make(chan struct{}),
	}
	go func() {
		_ = cmd.Wait()
		close(backend.exited)
	}()
	if err := PollBackendHealth(ctx, addr, backendHealthTimeout); err != nil {
		_ = backend.Stop()
		return nil, err
	}
	return backend, nil
}

// backendServeArgs builds the canopyd child argument list. Deliberately
// minimal: `serve` only. No -update-interval — canopyd's updater is flag
// opt-in, so the desktop child never runs update checks and exactly ONE
// updater (the Wails-side feed) is active per desktop binary (SPEC-FTR-06
// risk 14). TestDesktopRunsExactlyOneUpdater pins this.
func backendServeArgs() []string {
	return []string{"serve"}
}

// SupervisedBackend is the running canopyd child. It implements
// BackendContract.
type SupervisedBackend struct {
	addr   string
	binary string
	cmd    *exec.Cmd
	exited chan struct{}

	mu      sync.Mutex
	stopped bool
}

func (b *SupervisedBackend) Addr() string            { return b.addr }
func (b *SupervisedBackend) Exited() <-chan struct{} { return b.exited }

// Binary reports the resolved child executable (for logs and tests).
func (b *SupervisedBackend) Binary() string { return b.binary }

// Stop terminates the backend gracefully: SIGTERM to the process group (or
// taskkill /T on Windows), then SIGKILL after the grace window. Idempotent.
func (b *SupervisedBackend) Stop() error {
	b.mu.Lock()
	if b.stopped {
		b.mu.Unlock()
		return nil
	}
	b.stopped = true
	b.mu.Unlock()

	if b.cmd.Process != nil {
		if runtime.GOOS != "windows" {
			_ = signalBackendGroup(b.cmd, syscall.SIGTERM)
		} else {
			_ = signalBackendGroup(b.cmd, syscall.SIGKILL)
		}
	}
	select {
	case <-b.exited:
		return nil
	case <-time.After(backendStopGrace):
		if b.cmd.Process != nil {
			_ = b.cmd.Process.Kill()
		}
		<-b.exited
	}
	return nil
}

// ValidateBackendAddr enforces the loopback-only backend posture. Both IP
// loopback literals (127.0.0.0/8, ::1) and the "localhost" name are
// accepted; any non-loopback bind is refused.
func ValidateBackendAddr(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("wailsbridge: backend address %q: %w", addr, err)
	}
	loopback := false
	if ip := net.ParseIP(host); ip != nil {
		loopback = ip.IsLoopback()
	} else if strings.EqualFold(host, "localhost") {
		loopback = true
	}
	if !loopback {
		return fmt.Errorf("wailsbridge: backend address must be loopback, got %q", addr)
	}
	if _, err := net.LookupPort("tcp", port); err != nil {
		return fmt.Errorf("wailsbridge: backend port %q: %w", port, err)
	}
	return nil
}

// PollBackendHealth GETs /health until it answers 2xx or the timeout
// elapses. Exported so the readiness logic is exercised by repository-owned
// tests rather than only by the live desktop launch path.
func PollBackendHealth(ctx context.Context, addr string, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = backendHealthTimeout
	}
	deadline := time.Now().Add(timeout)
	url := "http://" + addr + "/health"
	client := &http.Client{Timeout: 2 * time.Second}
	lastErr := error(errors.New("health not polled"))
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return fmt.Errorf("wailsbridge: backend health poll cancelled: %w", ctx.Err())
		default:
		}
		resp, err := client.Get(url) //nolint:noctx // bounded client + deadline loop
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return nil
			}
			lastErr = fmt.Errorf("health endpoint returned HTTP %s", resp.Status)
		} else {
			lastErr = err
		}
		time.Sleep(150 * time.Millisecond)
	}
	return fmt.Errorf("wailsbridge: backend %s not healthy within %s: %w", addr, timeout, lastErr)
}

// ResolveBackendBinary is the binary-resolution seam for the shell: an
// explicit path wins, else "canopyd" (canopyd.exe on Windows) is looked up
// on PATH. An explicit path must exist, be a regular file, and be
// executable; a bad path is refused here with a descriptive error instead
// of surfacing later as an opaque exec failure from cmd.Start.
func ResolveBackendBinary(explicit string) (string, error) {
	name := strings.TrimSpace(explicit)
	if name == "" {
		if runtime.GOOS == "windows" {
			name = "canopyd.exe"
		} else {
			name = "canopyd"
		}
	}
	if strings.ContainsRune(name, '/') || strings.ContainsRune(name, '\\') {
		if err := validateExplicitBinary(name); err != nil {
			return "", err
		}
		return name, nil
	}
	path, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("wailsbridge: resolve %s: %w (install canopyd on PATH or pass -canopyd)", name, err)
	}
	return path, nil
}

// validateExplicitBinary checks an explicit (path-containing) binary
// argument: it must resolve to a regular, executable file. The executability
// check is POSIX-mode based; on Windows the exec permission bits are not
// meaningful, so existence and regular-file checks are what gate the path
// there (the OS refuses a non-executable at CreateProcess time regardless).
func validateExplicitBinary(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("wailsbridge: backend binary %q: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("wailsbridge: backend binary %q is not a regular file (mode %s)", path, info.Mode().Type())
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("wailsbridge: backend binary %q is not executable", path)
	}
	return nil
}

// DesktopService is the IPC surface exposed to the frontend through the
// Wails service binding (application.NewService). Public methods become
// callable from JS; the surface is deliberately tiny — status reflection
// only, no control verbs — so a compromised renderer cannot steer the
// backend beyond closing the window.
type DesktopService struct {
	backend BackendContract
	version string
}

// NewDesktopService constructs the IPC surface for the shell.
func NewDesktopService(backend BackendContract, version string) *DesktopService {
	return &DesktopService{backend: backend, version: version}
}

// Status reflects the desktop shell's view of its backend. Exposed to the
// frontend as `Status()` via the Wails service binding.
func (s *DesktopService) Status() map[string]any {
	addr := ""
	if s.backend != nil {
		addr = s.backend.Addr()
	}
	return map[string]any{
		"version": s.version,
		"backend": addr,
	}
}
