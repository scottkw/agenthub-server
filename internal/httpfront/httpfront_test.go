package httpfront

import (
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"
)

func TestServer_PlainHTTP_ServesRoutes(t *testing.T) {
	r := chi.NewRouter()
	r.Get("/ping", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("pong"))
	})

	srv, err := New(Options{
		Mode:    ModePlain,
		Address: "127.0.0.1:0",
		Handler: r,
	})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Start(ctx) }()

	// Give the listener a moment to bind.
	require.Eventually(t, func() bool {
		return srv.Addr() != ""
	}, time.Second, 10*time.Millisecond)

	resp, err := http.Get("http://" + srv.Addr() + "/ping")
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })

	body, _ := io.ReadAll(resp.Body)
	require.Equal(t, "pong", string(body))
	require.Equal(t, http.StatusOK, resp.StatusCode)

	cancel()
	require.NoError(t, <-errCh)
}
