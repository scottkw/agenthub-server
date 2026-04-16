package obs

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewLogger_JSONFormat(t *testing.T) {
	var buf bytes.Buffer
	l := newWithWriter(&buf, Options{Format: FormatJSON, Level: slog.LevelInfo})

	l.Info("hello", "key", "value")

	var obj map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &obj))
	require.Equal(t, "hello", obj["msg"])
	require.Equal(t, "value", obj["key"])
}

func TestNewLogger_TextFormat(t *testing.T) {
	var buf bytes.Buffer
	l := newWithWriter(&buf, Options{Format: FormatText, Level: slog.LevelInfo})

	l.Info("hello", "key", "value")

	require.Contains(t, buf.String(), "hello")
	require.Contains(t, buf.String(), "key=value")
}

func TestNewLogger_LevelFilters(t *testing.T) {
	var buf bytes.Buffer
	l := newWithWriter(&buf, Options{Format: FormatJSON, Level: slog.LevelWarn})

	l.Info("filtered")
	l.Warn("kept")

	out := buf.String()
	require.NotContains(t, out, "filtered")
	require.Contains(t, strings.ToLower(out), "kept")
}
