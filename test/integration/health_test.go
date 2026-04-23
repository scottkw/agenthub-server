package integration

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestHealth_E2E(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("signal shutdown differs on Windows")
	}

	smtp := newMiniSMTP(t)
	binary := buildBinary(t)
	dataDir := t.TempDir()

	cmd := exec.Command(binary)
	cmd.Env = append(os.Environ(),
		"AGENTHUB_MODE=solo",
		"AGENTHUB_TLS_MODE=off",
		"AGENTHUB_HTTP_PORT=18190",
		"AGENTHUB_DATA_DIR="+dataDir,
		"AGENTHUB_LOG_LEVEL=warn",
		"AGENTHUB_MAIL_PROVIDER=smtp",
		"AGENTHUB_MAIL_SMTP_HOST=127.0.0.1",
		"AGENTHUB_MAIL_SMTP_PORT="+smtp.Port,
	)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr

	require.NoError(t, cmd.Start())

	base := "http://127.0.0.1:18190"
	waitReady(t, base+"/healthz")

	resp, err := http.Get(base + "/healthz")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var body map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.Equal(t, "ok", body["status"])
	require.NotNil(t, body["uptime_sec"])
	require.NotNil(t, body["go"])

	// Graceful shutdown.
	require.NoError(t, cmd.Process.Signal(os.Interrupt))
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("server did not shut down gracefully")
	}
}
