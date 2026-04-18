package headscale

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSupervisor_FailsFastOnMissingBinary(t *testing.T) {
	sv := NewSupervisor(Options{
		BinaryPath:   "/nope/does-not-exist",
		DataDir:      t.TempDir(),
		ReadyTimeout: time.Second,
	}, "http://127.0.0.1:1")
	err := sv.Start(context.Background())
	require.Error(t, err)
}

func TestSupervisor_WaitsForHealthEndpoint(t *testing.T) {
	ready := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-ready:
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	t.Cleanup(srv.Close)

	// Use `sh` + an infinite `sleep` as a stand-in for the headscale binary.
	// The supervisor polls srv.URL/health regardless of what the child does.
	sh, err := exec.LookPath("sh")
	require.NoError(t, err)

	// The supervisor needs a plausible Options struct to render config.yaml,
	// even though this fake child doesn't read it.
	dataDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, "config.yaml"), []byte("# placeholder\n"), 0o644))

	sv := NewSupervisor(Options{
		BinaryPath:     sh,
		DataDir:        dataDir,
		ServerURL:      "http://fake",
		ListenAddr:     "127.0.0.1:1",
		GRPCListenAddr: "127.0.0.1:2",
		UnixSocket:     filepath.Join(dataDir, "hs.sock"),
		ReadyTimeout:   3 * time.Second,
	}, srv.URL)
	sv.argsForTest = []string{"-c", "sleep 30"}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	go func() {
		time.Sleep(200 * time.Millisecond)
		close(ready)
	}()

	require.NoError(t, sv.Start(ctx))
	cancel()
	_ = sv.Wait(context.Background())
}
