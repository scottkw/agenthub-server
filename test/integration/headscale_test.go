package integration

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	v1 "github.com/juanfont/headscale/gen/go/headscale/v1"
)

// TestHeadscale_EndToEnd boots our binary with Headscale enabled, drives
// pair + claim, and verifies the returned pre-auth key is visible via
// Headscale's own admin gRPC API. The test is skipped if the headscale
// binary isn't on disk; CI runs `scripts/fetch-headscale.sh` in setup.
func TestHeadscale_EndToEnd(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no Windows Headscale binary")
	}

	hsBin := findHeadscaleBinary(t)

	smtp := newMiniSMTP(t)
	binary := buildBinary(t)
	dataDir := t.TempDir()

	// macOS caps UNIX socket paths at ~104 chars. t.TempDir() can blow past
	// that, so we put just the socket in a short /tmp dir (still cleaned up
	// via t.Cleanup).
	shortSockDir, err := os.MkdirTemp("/tmp", "ahs-e2e-*")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(shortSockDir) })
	hsSocket := filepath.Join(shortSockDir, "hs.sock")

	cmd := exec.Command(binary)
	cmd.Env = append(os.Environ(),
		"AGENTHUB_MODE=solo",
		"AGENTHUB_TLS_MODE=off",
		"AGENTHUB_HTTP_PORT=18185",
		"AGENTHUB_DATA_DIR="+dataDir,
		"AGENTHUB_LOG_LEVEL=warn",
		"AGENTHUB_MAIL_PROVIDER=smtp",
		"AGENTHUB_MAIL_SMTP_HOST=127.0.0.1",
		"AGENTHUB_MAIL_SMTP_PORT="+smtp.Port,
		"AGENTHUB_VERIFY_URL_PREFIX=http://127.0.0.1:18185/api/auth/verify",
		"AGENTHUB_RESET_URL_PREFIX=http://127.0.0.1:18185/api/auth/reset",
		"AGENTHUB_HEADSCALE_ENABLED=1",
		"AGENTHUB_HEADSCALE_BINARY_PATH="+hsBin,
		"AGENTHUB_HEADSCALE_SERVER_URL=http://127.0.0.1:18285",
		"AGENTHUB_HEADSCALE_LISTEN_ADDR=127.0.0.1:18285",
		"AGENTHUB_HEADSCALE_UNIX_SOCKET="+hsSocket,
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
		case <-time.After(15 * time.Second):
			_ = cmd.Process.Kill()
		}
	})

	base := "http://127.0.0.1:18185"
	waitReady(t, base+"/healthz")

	// Signup + verify + login.
	_ = postExpect(t, base+"/api/auth/signup", map[string]string{
		"email": "hs-e2e@example.com", "password": "topsecretpw", "account_name": "HSE2E",
	}, 200)
	vTok := smtp.WaitForToken(t, "/api/auth/verify", 5*time.Second)
	_ = postExpect(t, base+"/api/auth/verify", map[string]string{"token": vTok}, 200)
	loginBody := postExpect(t, base+"/api/auth/login", map[string]string{
		"email": "hs-e2e@example.com", "password": "topsecretpw",
	}, 200)
	var login struct {
		Token string `json:"token"`
	}
	require.NoError(t, json.Unmarshal(loginBody, &login))

	// Pair code.
	req, _ := http.NewRequest("POST", base+"/api/devices/pair-code", nil)
	req.Header.Set("Authorization", "Bearer "+login.Token)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	var pair struct {
		Code string `json:"code"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&pair))

	// Claim.
	claimBody := postExpect(t, base+"/api/devices/claim", map[string]string{
		"code": pair.Code, "name": "hs-laptop", "platform": "darwin", "app_version": "0.1.0",
	}, 200)
	var claim struct {
		APIToken  string `json:"api_token"`
		Tailscale struct {
			PreAuthKey string `json:"pre_auth_key"`
			ControlURL string `json:"control_url"`
		} `json:"tailscale"`
	}
	require.NoError(t, json.Unmarshal(claimBody, &claim))
	require.True(t, strings.HasPrefix(claim.APIToken, "ahs_"))
	require.False(t, strings.HasPrefix(claim.Tailscale.PreAuthKey, "stub-"),
		"expected real headscale key, got stub: %q", claim.Tailscale.PreAuthKey)
	require.NotEmpty(t, claim.Tailscale.PreAuthKey)
	require.Equal(t, "http://127.0.0.1:18285", claim.Tailscale.ControlURL)

	// Verify the key exists in Headscale via its own gRPC API.
	hsConn, err := grpc.NewClient(
		"passthrough:///unix",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", hsSocket)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	defer hsConn.Close()

	hs := v1.NewHeadscaleServiceClient(hsConn)
	users, err := hs.ListUsers(context.Background(), &v1.ListUsersRequest{})
	require.NoError(t, err)
	require.NotEmpty(t, users.GetUsers(), "Headscale should have one user after claim")

	keys, err := hs.ListPreAuthKeys(context.Background(), &v1.ListPreAuthKeysRequest{})
	require.NoError(t, err)

	// Headscale v0.28.0 masks the listed key to "hskey-auth-<prefix>-***" and
	// only returns the full "hskey-auth-<prefix>-<secret>" on creation. Match
	// by the common "hskey-auth-<prefix>-" portion instead of exact equality.
	// See hscontrol/types/preauth_key.go:(*PreAuthKey).Proto().
	require.True(t, strings.HasPrefix(claim.Tailscale.PreAuthKey, "hskey-auth-"),
		"expected hskey-auth- prefix in returned key: %q", claim.Tailscale.PreAuthKey)
	parts := strings.SplitN(claim.Tailscale.PreAuthKey, "-", 4)
	require.Len(t, parts, 4, "expected hskey-auth-<prefix>-<secret>, got %q", claim.Tailscale.PreAuthKey)
	wantPrefix := "hskey-auth-" + parts[2] + "-"

	found := false
	for _, k := range keys.GetPreAuthKeys() {
		if strings.HasPrefix(k.GetKey(), wantPrefix) {
			found = true
			break
		}
	}
	require.True(t, found, "claim pre-auth key (prefix %q) should be present in Headscale", wantPrefix)

	// Verify /headscale proxy is wired (GET /headscale/health should 200 since
	// it forwards to Headscale's own /health).
	proxyResp, err := http.Get(base + "/headscale/health")
	require.NoError(t, err)
	defer proxyResp.Body.Close()
	require.Equal(t, http.StatusOK, proxyResp.StatusCode)
}

// findHeadscaleBinary locates ./bin/headscale relative to the repo root.
// The test's working directory is test/integration/, so exec.LookPath("./bin/headscale")
// from there misses the binary. We walk up from cwd looking for bin/headscale,
// then fall back to $PATH, and finally skip the test.
func findHeadscaleBinary(t *testing.T) string {
	t.Helper()

	cwd, err := os.Getwd()
	if err == nil {
		dir := cwd
		for i := 0; i < 6; i++ {
			candidate := filepath.Join(dir, "bin", "headscale")
			if info, statErr := os.Stat(candidate); statErr == nil && !info.IsDir() {
				abs, absErr := filepath.Abs(candidate)
				if absErr == nil {
					return abs
				}
				return candidate
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}

	if hsBin, lookErr := exec.LookPath("headscale"); lookErr == nil {
		return hsBin
	}

	t.Skip("headscale binary not found; run `./scripts/fetch-headscale.sh`")
	return ""
}

func TestHeadscale_DERP_EndToEnd(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no Windows Headscale binary")
	}

	hsBin := findHeadscaleBinary(t)

	smtp := newMiniSMTP(t)
	binary := buildBinary(t)
	dataDir := t.TempDir()

	// Short socket path (macOS sun_path 104-char limit) — mirror Plan 05 pattern.
	shortSockDir, err := os.MkdirTemp("", "ahs-derp-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(shortSockDir) })
	hsSocket := filepath.Join(shortSockDir, "hs.sock")

	cmd := exec.Command(binary)
	cmd.Env = append(os.Environ(),
		"AGENTHUB_MODE=solo",
		"AGENTHUB_HOSTNAME=127.0.0.1",
		"AGENTHUB_TLS_MODE=off",
		"AGENTHUB_HTTP_PORT=18186",
		"AGENTHUB_DATA_DIR="+dataDir,
		"AGENTHUB_LOG_LEVEL=warn",
		"AGENTHUB_MAIL_PROVIDER=smtp",
		"AGENTHUB_MAIL_SMTP_HOST=127.0.0.1",
		"AGENTHUB_MAIL_SMTP_PORT="+smtp.Port,
		"AGENTHUB_VERIFY_URL_PREFIX=http://127.0.0.1:18186/api/auth/verify",
		"AGENTHUB_RESET_URL_PREFIX=http://127.0.0.1:18186/api/auth/reset",
		"AGENTHUB_HEADSCALE_ENABLED=1",
		"AGENTHUB_HEADSCALE_BINARY_PATH="+hsBin,
		"AGENTHUB_HEADSCALE_SERVER_URL=http://127.0.0.1:18286",
		"AGENTHUB_HEADSCALE_LISTEN_ADDR=127.0.0.1:18286",
		"AGENTHUB_HEADSCALE_UNIX_SOCKET="+hsSocket,
		"AGENTHUB_HEADSCALE_DERP_ENABLED=1",
		"AGENTHUB_HEADSCALE_DERP_HOSTNAME=127.0.0.1",
		"AGENTHUB_HEADSCALE_DERP_PORT=18186",
		"AGENTHUB_HEADSCALE_DERP_STUN_LISTEN_ADDR=127.0.0.1:3479", // non-standard port to avoid collision
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
		case <-time.After(15 * time.Second):
			_ = cmd.Process.Kill()
		}
	})

	base := "http://127.0.0.1:18186"
	waitReady(t, base+"/healthz")

	// Signup + verify + login.
	_ = postExpect(t, base+"/api/auth/signup", map[string]string{
		"email": "derp-e2e@example.com", "password": "topsecretpw", "account_name": "DERP",
	}, 200)
	vTok := smtp.WaitForToken(t, "/api/auth/verify", 5*time.Second)
	_ = postExpect(t, base+"/api/auth/verify", map[string]string{"token": vTok}, 200)
	loginBody := postExpect(t, base+"/api/auth/login", map[string]string{
		"email": "derp-e2e@example.com", "password": "topsecretpw",
	}, 200)
	var login struct {
		Token string `json:"token"`
	}
	require.NoError(t, json.Unmarshal(loginBody, &login))

	req, _ := http.NewRequest("POST", base+"/api/devices/pair-code", nil)
	req.Header.Set("Authorization", "Bearer "+login.Token)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	var pair struct {
		Code string `json:"code"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&pair))

	claimBody := postExpect(t, base+"/api/devices/claim", map[string]string{
		"code": pair.Code, "name": "derp-laptop",
	}, 200)
	var claim struct {
		Tailscale struct {
			DERPMapJSON string `json:"derp_map_json"`
		} `json:"tailscale"`
	}
	require.NoError(t, json.Unmarshal(claimBody, &claim))

	// DERP map must NOT be the empty stub.
	require.NotEqual(t, `{"Regions":{}}`, claim.Tailscale.DERPMapJSON)
	require.Contains(t, claim.Tailscale.DERPMapJSON, `"RegionID":999`)
	require.Contains(t, claim.Tailscale.DERPMapJSON, `"HostName":"127.0.0.1"`)

	// /derp proxy passthrough — any 2xx/3xx response proves the proxy reached
	// Headscale. (The exact path Headscale serves under /derp varies; "/derp/probe"
	// is the conventional Tailscale probe path. Any non-error response means the
	// proxy is wired, which is all we're validating.)
	probeResp, err := http.Get(base + "/derp/probe")
	require.NoError(t, err)
	defer probeResp.Body.Close()
	require.Less(t, probeResp.StatusCode, 400, "expected /derp/probe to be proxied to Headscale; got %d", probeResp.StatusCode)
}
