package integration

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestOAuth_EndToEnd is skipped because driving the full OAuth redirect
// through the binary would require making AuthURL runtime-overridable via
// env var. The unit tests in internal/api/oauth_test.go cover the HTTP
// route logic with a fake OAuth provider.
func TestOAuth_EndToEnd(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("signal shutdown differs on Windows")
	}
	t.Skip("OAuth integration requires a runtime AuthURL override not in config yet; covered by unit tests in internal/api/oauth_test.go.")
}

func TestAPIToken_EndToEnd(t *testing.T) {
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
		"AGENTHUB_HTTP_PORT=18183",
		"AGENTHUB_DATA_DIR="+dataDir,
		"AGENTHUB_LOG_LEVEL=warn",
		"AGENTHUB_MAIL_PROVIDER=smtp",
		"AGENTHUB_MAIL_SMTP_HOST=127.0.0.1",
		"AGENTHUB_MAIL_SMTP_PORT="+smtp.Port,
		"AGENTHUB_VERIFY_URL_PREFIX=http://127.0.0.1:18183/api/auth/verify",
		"AGENTHUB_RESET_URL_PREFIX=http://127.0.0.1:18183/api/auth/reset",
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

	base := "http://127.0.0.1:18183"
	waitReady(t, base+"/healthz")

	_ = postExpect(t, base+"/api/auth/signup", map[string]string{
		"email":        "tok-e2e@example.com",
		"password":     "topsecretpw",
		"account_name": "TokE2E",
	}, 200)
	verifyToken := smtp.WaitForToken(t, "/api/auth/verify", 5*time.Second)
	_ = postExpect(t, base+"/api/auth/verify", map[string]string{"token": verifyToken}, 200)
	loginBody := postExpect(t, base+"/api/auth/login", map[string]string{
		"email": "tok-e2e@example.com", "password": "topsecretpw",
	}, 200)
	var login struct {
		Token string `json:"token"`
	}
	require.NoError(t, json.Unmarshal(loginBody, &login))

	// Create API token (POST /api/tokens).
	req, _ := http.NewRequest("POST", base+"/api/tokens", strings.NewReader(`{"name":"cli"}`))
	req.Header.Set("Authorization", "Bearer "+login.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, 200, resp.StatusCode)

	var tokResp struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&tokResp))
	require.True(t, strings.HasPrefix(tokResp.Token, "ahs_"))

	// Use the API token to list tokens.
	req, _ = http.NewRequest("GET", base+"/api/tokens", nil)
	req.Header.Set("Authorization", "Token "+tokResp.Token)
	resp, err = http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, 200, resp.StatusCode)
}
