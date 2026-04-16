// Package obs provides structured logging, metrics, and tracing primitives.
package obs

import (
	"io"
	"log/slog"
	"os"
)

type Format string

const (
	FormatJSON Format = "json"
	FormatText Format = "text"
)

type Options struct {
	Format Format
	Level  slog.Level
}

// NewLogger builds a slog.Logger that writes to stderr with the given options.
func NewLogger(opts Options) *slog.Logger {
	return newWithWriter(os.Stderr, opts)
}

func newWithWriter(w io.Writer, opts Options) *slog.Logger {
	handlerOpts := &slog.HandlerOptions{Level: opts.Level}
	var h slog.Handler
	switch opts.Format {
	case FormatText:
		h = slog.NewTextHandler(w, handlerOpts)
	default:
		h = slog.NewJSONHandler(w, handlerOpts)
	}
	return slog.New(h)
}
