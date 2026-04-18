package headscale

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

// Supervisor runs `headscale serve` as a managed subprocess. It writes a
// config.yaml into DataDir at Start, spawns the child, polls the health
// endpoint until ready, and on context cancel sends SIGINT followed by
// (after ShutdownTimeout) SIGKILL.
type Supervisor struct {
	opts      Options
	baseURL   string // e.g. http://127.0.0.1:18081 — where /health lives
	cmd       *exec.Cmd
	waitErrCh chan error
	log       *slog.Logger

	// argsForTest overrides the normal ["serve", "-c", <path>] args.
	// Unexported; test-only.
	argsForTest []string
}

// NewSupervisor returns a configured supervisor. Call Start to spawn,
// Wait to block for exit, or rely on context cancellation to trigger
// graceful shutdown.
func NewSupervisor(opts Options, baseURL string) *Supervisor {
	if opts.ShutdownTimeout == 0 {
		opts.ShutdownTimeout = 10 * time.Second
	}
	if opts.ReadyTimeout == 0 {
		opts.ReadyTimeout = 10 * time.Second
	}
	return &Supervisor{opts: opts, baseURL: baseURL, log: slog.Default()}
}

// WithLogger wires a logger in. Otherwise slog.Default is used.
func (s *Supervisor) WithLogger(l *slog.Logger) *Supervisor { s.log = l; return s }

// Start generates the config, spawns the subprocess, and blocks until the
// /health endpoint responds 200 or ReadyTimeout elapses. Returns a fatal
// error if the binary is missing, the config can't be written, or the
// subprocess exits before becoming ready.
func (s *Supervisor) Start(ctx context.Context) error {
	if _, err := os.Stat(s.opts.BinaryPath); err != nil {
		return fmt.Errorf("headscale supervisor: binary %q: %w", s.opts.BinaryPath, err)
	}

	if err := os.MkdirAll(s.opts.DataDir, 0o750); err != nil {
		return fmt.Errorf("headscale supervisor: mkdir datadir: %w", err)
	}
	cfgBytes, err := RenderConfig(s.opts)
	if err != nil {
		return fmt.Errorf("headscale supervisor: render config: %w", err)
	}
	cfgPath := filepath.Join(s.opts.DataDir, "config.yaml")
	if err := os.WriteFile(cfgPath, cfgBytes, 0o640); err != nil {
		return fmt.Errorf("headscale supervisor: write config: %w", err)
	}

	args := s.argsForTest
	if args == nil {
		args = []string{"serve", "-c", cfgPath}
	}
	s.cmd = exec.CommandContext(ctx, s.opts.BinaryPath, args...)
	s.cmd.Stdout = logWriter{log: s.log, level: slog.LevelInfo, source: "headscale"}
	s.cmd.Stderr = logWriter{log: s.log, level: slog.LevelWarn, source: "headscale"}
	// Ensure SIGINT on our Cancel propagates cleanly (default is SIGKILL).
	s.cmd.Cancel = func() error { return s.cmd.Process.Signal(syscall.SIGINT) }
	s.cmd.WaitDelay = s.opts.ShutdownTimeout

	if err := s.cmd.Start(); err != nil {
		return fmt.Errorf("headscale supervisor: start: %w", err)
	}

	s.waitErrCh = make(chan error, 1)
	go func() { s.waitErrCh <- s.cmd.Wait() }()

	// Poll /health.
	deadline := time.Now().Add(s.opts.ReadyTimeout)
	for {
		if time.Now().After(deadline) {
			_ = s.signalShutdown()
			return fmt.Errorf("headscale supervisor: not ready within %s", s.opts.ReadyTimeout)
		}
		select {
		case err := <-s.waitErrCh:
			return fmt.Errorf("headscale supervisor: subprocess exited before ready: %w", err)
		default:
		}
		resp, err := http.Get(s.baseURL + "/health")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				s.log.Info("headscale ready", "url", s.baseURL)
				return nil
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// Wait blocks until the subprocess exits. Returns the exit error if any.
// If ctx is cancelled before the subprocess exits, Wait returns ctx.Err().
func (s *Supervisor) Wait(ctx context.Context) error {
	if s.waitErrCh == nil {
		return nil
	}
	select {
	case err := <-s.waitErrCh:
		if err == nil {
			return nil
		}
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return fmt.Errorf("headscale exited: %s", ee.Error())
		}
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Supervisor) signalShutdown() error {
	if s.cmd == nil || s.cmd.Process == nil {
		return nil
	}
	return s.cmd.Process.Signal(syscall.SIGINT)
}

// logWriter routes subprocess stdout/stderr into a structured logger.
type logWriter struct {
	log    *slog.Logger
	level  slog.Level
	source string
}

func (w logWriter) Write(p []byte) (int, error) {
	msg := string(p)
	w.log.Log(context.Background(), w.level, msg, "source", w.source)
	return len(p), nil
}

// compile-time assertion that logWriter satisfies io.Writer
var _ io.Writer = logWriter{}
