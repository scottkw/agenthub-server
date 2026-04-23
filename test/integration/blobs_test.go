package integration

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// TestBlobs_PresignUploadCommitDownloadE2E boots the real binary, runs the
// full two-phase upload flow, and verifies download returns the same bytes.
func TestBlobs_PresignUploadCommitDownloadE2E(t *testing.T) {
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
		"AGENTHUB_HTTP_PORT=18188",
		"AGENTHUB_DATA_DIR="+dataDir,
		"AGENTHUB_LOG_LEVEL=warn",
		"AGENTHUB_MAIL_PROVIDER=smtp",
		"AGENTHUB_MAIL_SMTP_HOST=127.0.0.1",
		"AGENTHUB_MAIL_SMTP_PORT="+smtp.Port,
		"AGENTHUB_VERIFY_URL_PREFIX=http://127.0.0.1:18188/api/auth/verify",
		"AGENTHUB_RESET_URL_PREFIX=http://127.0.0.1:18188/api/auth/reset",
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

	base := "http://127.0.0.1:18188"
	waitReady(t, base+"/healthz")

	// Signup + verify + login.
	_ = postExpect(t, base+"/api/auth/signup", map[string]string{
		"email": "blob-e2e@example.com", "password": "topsecretpw", "account_name": "BLOB",
	}, 200)
	vTok := smtp.WaitForToken(t, "/api/auth/verify", 5*time.Second)
	_ = postExpect(t, base+"/api/auth/verify", map[string]string{"token": vTok}, 200)
	loginBody := postExpect(t, base+"/api/auth/login", map[string]string{
		"email": "blob-e2e@example.com", "password": "topsecretpw",
	}, 200)
	var login struct {
		Token string `json:"token"`
	}
	require.NoError(t, json.Unmarshal(loginBody, &login))

	bearer := func(req *http.Request) { req.Header.Set("Authorization", "Bearer "+login.Token) }

	// 1. Presign.
	psReq, _ := http.NewRequest("POST", base+"/api/blobs/presign",
		bytes.NewReader(mustJSON(map[string]any{
			"content_type": "text/plain",
			"size_bytes":   12,
			"sha256":       "e2e-dummy-sha",
		})))
	psReq.Header.Set("Content-Type", "application/json")
	bearer(psReq)
	psResp, err := http.DefaultClient.Do(psReq)
	require.NoError(t, err)
	defer psResp.Body.Close()
	require.Equal(t, http.StatusOK, psResp.StatusCode)
	var presign struct {
		PutURL   string `json:"put_url"`
		ObjectID string `json:"object_id"`
	}
	require.NoError(t, json.NewDecoder(psResp.Body).Decode(&presign))
	require.NotEmpty(t, presign.ObjectID)

	// 2. Upload (put_url is already absolute from the file backend).
	upReq, _ := http.NewRequest("PUT", presign.PutURL, bytes.NewReader([]byte("hello e2e!")))
	bearer(upReq)
	upResp, err := http.DefaultClient.Do(upReq)
	require.NoError(t, err)
	require.Equal(t, http.StatusNoContent, upResp.StatusCode)
	upResp.Body.Close()

	// 3. Commit.
	cmReq, _ := http.NewRequest("POST", base+"/api/blobs/"+presign.ObjectID+"/commit",
		bytes.NewReader(mustJSON(map[string]any{
			"content_type": "text/plain",
			"size_bytes":   12,
			"sha256":       "e2e-dummy-sha",
		})))
	cmReq.Header.Set("Content-Type", "application/json")
	bearer(cmReq)
	cmResp, err := http.DefaultClient.Do(cmReq)
	require.NoError(t, err)
	defer cmResp.Body.Close()
	require.Equal(t, http.StatusOK, cmResp.StatusCode)
	var commit struct {
		ID          string `json:"id"`
		DownloadURL string `json:"download_url"`
	}
	require.NoError(t, json.NewDecoder(cmResp.Body).Decode(&commit))
	require.Equal(t, presign.ObjectID, commit.ID)

	// 4. Download via the returned URL (already absolute).
	dlReq, _ := http.NewRequest("GET", commit.DownloadURL, nil)
	dlReq.Header.Set("Authorization", "Bearer "+login.Token)
	dlResp, err := http.DefaultClient.Do(dlReq)
	require.NoError(t, err)
	defer dlResp.Body.Close()
	require.Equal(t, http.StatusOK, dlResp.StatusCode)
	body, err := io.ReadAll(dlResp.Body)
	require.NoError(t, err)
	require.Equal(t, "hello e2e!", string(body))
}
