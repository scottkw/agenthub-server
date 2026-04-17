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

func TestDevicesAndSessions_EndToEnd(t *testing.T) {
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
		"AGENTHUB_HTTP_PORT=18184",
		"AGENTHUB_DATA_DIR="+dataDir,
		"AGENTHUB_LOG_LEVEL=warn",
		"AGENTHUB_MAIL_PROVIDER=smtp",
		"AGENTHUB_MAIL_SMTP_HOST=127.0.0.1",
		"AGENTHUB_MAIL_SMTP_PORT="+smtp.Port,
		"AGENTHUB_VERIFY_URL_PREFIX=http://127.0.0.1:18184/api/auth/verify",
		"AGENTHUB_RESET_URL_PREFIX=http://127.0.0.1:18184/api/auth/reset",
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

	base := "http://127.0.0.1:18184"
	waitReady(t, base+"/healthz")

	// 1. Signup + verify + login (device A).
	_ = postExpect(t, base+"/api/auth/signup", map[string]string{
		"email": "e2e-dev@example.com", "password": "topsecretpw", "account_name": "E2ED",
	}, 200)
	vTok := smtp.WaitForToken(t, "/api/auth/verify", 5*time.Second)
	_ = postExpect(t, base+"/api/auth/verify", map[string]string{"token": vTok}, 200)
	loginBody := postExpect(t, base+"/api/auth/login", map[string]string{
		"email": "e2e-dev@example.com", "password": "topsecretpw",
	}, 200)
	var login struct {
		Token string `json:"token"`
	}
	require.NoError(t, json.Unmarshal(loginBody, &login))

	bearer := func(req *http.Request) { req.Header.Set("Authorization", "Bearer "+login.Token) }

	// 2. Issue pair code from device A.
	req, _ := http.NewRequest("POST", base+"/api/devices/pair-code", nil)
	bearer(req)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, 200, resp.StatusCode)
	var pair struct {
		Code string `json:"code"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&pair))
	require.NotEmpty(t, pair.Code)

	// 3. Claim from device B (no auth).
	claimBody := postExpect(t, base+"/api/devices/claim", map[string]string{
		"code": pair.Code, "name": "laptop-B", "platform": "darwin", "app_version": "0.1.0",
	}, 200)
	var claim struct {
		DeviceID  string `json:"device_id"`
		APIToken  string `json:"api_token"`
		Tailscale struct {
			ControlURL string `json:"control_url"`
			PreAuthKey string `json:"pre_auth_key"`
		} `json:"tailscale"`
	}
	require.NoError(t, json.Unmarshal(claimBody, &claim))
	require.True(t, strings.HasPrefix(claim.APIToken, "ahs_"))
	require.True(t, strings.HasPrefix(claim.Tailscale.PreAuthKey, "stub-"))

	deviceTok := func(req *http.Request) { req.Header.Set("Authorization", "Token "+claim.APIToken) }

	// 4. Report tailscale-info as device B.
	tsReq, _ := http.NewRequest("POST",
		base+"/api/devices/"+claim.DeviceID+"/tailscale-info",
		strings.NewReader(`{"tailscale_node_id":"ts-node-e2e"}`))
	deviceTok(tsReq)
	tsReq.Header.Set("Content-Type", "application/json")
	tsResp, err := http.DefaultClient.Do(tsReq)
	require.NoError(t, err)
	defer tsResp.Body.Close()
	require.Equal(t, 204, tsResp.StatusCode)

	// 5. Create a session via device B.
	sReq, _ := http.NewRequest("POST", base+"/api/sessions",
		strings.NewReader(`{"label":"e2e","cwd":"/tmp"}`))
	deviceTok(sReq)
	sReq.Header.Set("Content-Type", "application/json")
	sResp, err := http.DefaultClient.Do(sReq)
	require.NoError(t, err)
	defer sResp.Body.Close()
	require.Equal(t, 200, sResp.StatusCode)
	var created struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.NewDecoder(sResp.Body).Decode(&created))

	// 6. List sessions via bearer JWT (device A's UI).
	listReq, _ := http.NewRequest("GET", base+"/api/sessions", nil)
	bearer(listReq)
	listResp, err := http.DefaultClient.Do(listReq)
	require.NoError(t, err)
	defer listResp.Body.Close()
	require.Equal(t, 200, listResp.StatusCode)
	var list struct {
		Sessions []map[string]any `json:"sessions"`
	}
	require.NoError(t, json.NewDecoder(listResp.Body).Decode(&list))
	require.Len(t, list.Sessions, 1)
	require.Equal(t, "running", list.Sessions[0]["status"])

	// 7. End the session via device B.
	endReq, _ := http.NewRequest("POST", base+"/api/sessions/"+created.ID+"/end", nil)
	deviceTok(endReq)
	endResp, err := http.DefaultClient.Do(endReq)
	require.NoError(t, err)
	defer endResp.Body.Close()
	require.Equal(t, 204, endResp.StatusCode)
}
