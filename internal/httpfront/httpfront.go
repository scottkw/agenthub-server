// Package httpfront wires the TLS/routing frontend for the server. It
// exposes a single Start/Stop lifecycle over either plain HTTP (for tests
// and solo mode with tls.mode: off) or HTTPS via certmagic (solo/hosted auto).
package httpfront

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/caddyserver/certmagic"
)

type Mode string

const (
	ModePlain Mode = "plain"
	ModeAuto  Mode = "auto"
	ModeFile  Mode = "file"
)

type Options struct {
	Mode     Mode
	Address  string   // for ModePlain and ModeFile ("host:port"); "" for ModeAuto
	Handler  http.Handler
	CertFile string   // ModeFile
	KeyFile  string   // ModeFile
	Email    string   // ModeAuto — ACME registration
	Domains  []string // ModeAuto — served hostnames
}

type Server struct {
	opts Options
	srv  *http.Server
	addr atomic.Pointer[string]
}

func New(opts Options) (*Server, error) {
	if opts.Handler == nil {
		return nil, fmt.Errorf("httpfront.New: Handler is required")
	}
	switch opts.Mode {
	case ModePlain, ModeFile:
		if opts.Address == "" {
			return nil, fmt.Errorf("httpfront.New: Address required for mode %q", opts.Mode)
		}
	case ModeAuto:
		if opts.Email == "" || len(opts.Domains) == 0 {
			return nil, fmt.Errorf("httpfront.New: Email and Domains required for auto TLS")
		}
	default:
		return nil, fmt.Errorf("httpfront.New: unknown mode %q", opts.Mode)
	}
	return &Server{opts: opts}, nil
}

// Addr returns the bound address (useful when Address used ":0" for a
// random port). Empty until Start has bound the listener.
func (s *Server) Addr() string {
	if p := s.addr.Load(); p != nil {
		return *p
	}
	return ""
}

// Start binds the listener and serves until ctx is cancelled, then shuts
// down gracefully with a 30s drain.
func (s *Server) Start(ctx context.Context) error {
	handler := s.opts.Handler

	switch s.opts.Mode {
	case ModePlain:
		return s.servePlain(ctx, handler)
	case ModeFile:
		return s.serveFile(ctx, handler)
	case ModeAuto:
		return s.serveAuto(ctx, handler)
	}
	return fmt.Errorf("httpfront.Start: unreachable")
}

func (s *Server) servePlain(ctx context.Context, h http.Handler) error {
	ln, err := net.Listen("tcp", s.opts.Address)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	addr := ln.Addr().String()
	s.addr.Store(&addr)

	s.srv = &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
	}
	return s.runAndShutdown(ctx, func() error { return s.srv.Serve(ln) })
}

func (s *Server) serveFile(ctx context.Context, h http.Handler) error {
	ln, err := net.Listen("tcp", s.opts.Address)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	addr := ln.Addr().String()
	s.addr.Store(&addr)

	s.srv = &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
	}
	return s.runAndShutdown(ctx, func() error {
		return s.srv.ServeTLS(ln, s.opts.CertFile, s.opts.KeyFile)
	})
}

func (s *Server) serveAuto(ctx context.Context, h http.Handler) error {
	certmagic.DefaultACME.Email = s.opts.Email
	certmagic.DefaultACME.Agreed = true

	// HTTPS on 443, HTTP-01 challenges + redirect on 80.
	addr := "0.0.0.0:443"
	s.addr.Store(&addr)

	// HTTPS returns only when the underlying server errors or is stopped.
	errCh := make(chan error, 1)
	go func() {
		errCh <- certmagic.HTTPS(s.opts.Domains, h)
	}()

	select {
	case <-ctx.Done():
		// certmagic.HTTPS has no graceful stop hook; exit when ctx is done.
		return nil
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}

func (s *Server) runAndShutdown(ctx context.Context, run func() error) error {
	errCh := make(chan error, 1)
	go func() { errCh <- run() }()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = s.srv.Shutdown(shutdownCtx)
		<-errCh
		return nil
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
