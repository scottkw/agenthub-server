// Package integration boots the binary as a subprocess and exercises real
// HTTP endpoints. Kept out of ./internal so it only runs via `go test ./test/...`.
package integration

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBoot_SoloMode_HealthzOK(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process-signal shutdown differs on Windows; covered by unit tests")
	}

	binary := buildBinary(t)
	dataDir := t.TempDir()

	cmd := exec.Command(binary)
	cmd.Env = append(os.Environ(),
		"AGENTHUB_MODE=solo",
		"AGENTHUB_TLS_MODE=off",
		"AGENTHUB_HTTP_PORT=18181",
		"AGENTHUB_DATA_DIR="+dataDir,
		"AGENTHUB_LOG_LEVEL=warn",
	)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr

	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		_ = cmd.Process.Signal(os.Interrupt)
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = cmd.Process.Kill()
		}
	})

	var resp *http.Response
	var err error
	require.Eventually(t, func() bool {
		resp, err = http.Get("http://127.0.0.1:18181/healthz")
		return err == nil && resp.StatusCode == http.StatusOK
	}, 10*time.Second, 100*time.Millisecond, "server did not become ready")

	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var obj map[string]any
	require.NoError(t, json.Unmarshal(body, &obj))
	require.Equal(t, "ok", obj["status"])
	require.Equal(t, "ok", obj["db"])
}

func buildBinary(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "agenthub-server")
	cmd := exec.Command("go", "build", "-o", out, "./cmd/agenthub-server")
	cmd.Dir = projectRoot(t)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	require.NoError(t, cmd.Run())
	return out
}

func projectRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	require.NoError(t, err)
	// test/integration → project root
	return filepath.Dir(filepath.Dir(wd))
}
