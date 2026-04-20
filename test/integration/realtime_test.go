package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/require"
)

// TestRealtime_DeviceCreatedEventE2E boots the real binary, opens a
// WebSocket as device A, claims a device B, and asserts the WS client
// receives the device.created event.
func TestRealtime_DeviceCreatedEventE2E(t *testing.T) {
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
		"AGENTHUB_HTTP_PORT=18187",
		"AGENTHUB_DATA_DIR="+dataDir,
		"AGENTHUB_LOG_LEVEL=warn",
		"AGENTHUB_MAIL_PROVIDER=smtp",
		"AGENTHUB_MAIL_SMTP_HOST=127.0.0.1",
		"AGENTHUB_MAIL_SMTP_PORT="+smtp.Port,
		"AGENTHUB_VERIFY_URL_PREFIX=http://127.0.0.1:18187/api/auth/verify",
		"AGENTHUB_RESET_URL_PREFIX=http://127.0.0.1:18187/api/auth/reset",
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

	base := "http://127.0.0.1:18187"
	waitReady(t, base+"/healthz")

	// Signup + verify + login.
	_ = postExpect(t, base+"/api/auth/signup", map[string]string{
		"email": "rt-e2e@example.com", "password": "topsecretpw", "account_name": "RT",
	}, 200)
	vTok := smtp.WaitForToken(t, "/api/auth/verify", 5*time.Second)
	_ = postExpect(t, base+"/api/auth/verify", map[string]string{"token": vTok}, 200)
	loginBody := postExpect(t, base+"/api/auth/login", map[string]string{
		"email": "rt-e2e@example.com", "password": "topsecretpw",
	}, 200)
	var login struct {
		Token string `json:"token"`
	}
	require.NoError(t, json.Unmarshal(loginBody, &login))

	// Open /ws with the token in the query param.
	u, err := url.Parse(base)
	require.NoError(t, err)
	u.Scheme = "ws"
	u.Path = "/ws"
	q := u.Query()
	q.Set("token", login.Token)
	u.RawQuery = q.Encode()

	wsCtx, wsCancel := context.WithCancel(context.Background())
	t.Cleanup(wsCancel)
	wsc, _, err := websocket.Dial(wsCtx, u.String(), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = wsc.Close(websocket.StatusNormalClosure, "test done") })

	// Small delay so the hub Register finishes before we publish.
	time.Sleep(200 * time.Millisecond)

	// Issue a pair code and claim a device — this triggers device.created.
	req, _ := http.NewRequest("POST", base+"/api/devices/pair-code", nil)
	req.Header.Set("Authorization", "Bearer "+login.Token)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	var pair struct {
		Code string `json:"code"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&pair))

	_ = postExpect(t, base+"/api/devices/claim", map[string]string{
		"code": pair.Code, "name": "rt-laptop",
	}, 200)

	// Expect device.created on the WebSocket.
	readCtx, readCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer readCancel()
	typ, data, err := wsc.Read(readCtx)
	require.NoError(t, err)
	require.Equal(t, websocket.MessageText, typ)

	var event struct {
		Type string                 `json:"type"`
		Data map[string]interface{} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(data, &event))
	require.Equal(t, "device.created", event.Type)
	require.Equal(t, "rt-laptop", event.Data["name"])
}
