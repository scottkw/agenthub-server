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

func TestAdmin_OperatorAccessE2E(t *testing.T) {
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
		"AGENTHUB_HTTP_PORT=18189",
		"AGENTHUB_DATA_DIR="+dataDir,
		"AGENTHUB_LOG_LEVEL=warn",
		"AGENTHUB_MAIL_PROVIDER=smtp",
		"AGENTHUB_MAIL_SMTP_HOST=127.0.0.1",
		"AGENTHUB_MAIL_SMTP_PORT="+smtp.Port,
		"AGENTHUB_VERIFY_URL_PREFIX=http://127.0.0.1:18189/api/auth/verify",
		"AGENTHUB_RESET_URL_PREFIX=http://127.0.0.1:18189/api/auth/reset",
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

	base := "http://127.0.0.1:18189"
	waitReady(t, base+"/healthz")

	// Signup + verify + login.
	_ = postExpect(t, base+"/api/auth/signup", map[string]string{
		"email": "admin-e2e@example.com", "password": "topsecretpw", "account_name": "ADM",
	}, 200)
	vTok := smtp.WaitForToken(t, "/api/auth/verify", 5*time.Second)
	_ = postExpect(t, base+"/api/auth/verify", map[string]string{"token": vTok}, 200)
	loginBody := postExpect(t, base+"/api/auth/login", map[string]string{
		"email": "admin-e2e@example.com", "password": "topsecretpw",
	}, 200)
	var login struct {
		Token string `json:"token"`
	}
	require.NoError(t, json.Unmarshal(loginBody, &login))

	// Non-operator gets 403 on /api/admin/users.
	req, _ := http.NewRequest("GET", base+"/api/admin/users", nil)
	req.Header.Set("Authorization", "Bearer "+login.Token)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
	resp.Body.Close()

	// /admin SPA returns HTML (the index.html fallback).
	resp2, err := http.Get(base + "/admin")
	require.NoError(t, err)
	defer resp2.Body.Close()
	require.Equal(t, http.StatusOK, resp2.StatusCode)
	require.Contains(t, resp2.Header.Get("Content-Type"), "text/html")
}
