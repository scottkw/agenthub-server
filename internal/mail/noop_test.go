package mail

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNoop_Send_LogsAndDiscards(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	m := NewNoop(logger)
	err := m.Send(context.Background(), Message{
		To:      "user@example.com",
		Subject: "hello",
		Text:    "world",
	})
	require.NoError(t, err)
	require.Contains(t, buf.String(), "user@example.com")
	require.Contains(t, buf.String(), "hello")
}
