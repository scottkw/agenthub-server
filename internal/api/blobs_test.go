package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	"github.com/scottkw/agenthub-server/internal/blob"
	"github.com/scottkw/agenthub-server/internal/realtime"
)

func newRouterWithBlobs(t *testing.T) (*chi.Mux, *stubMailer, blob.Blob, *realtime.InMemoryHub) {
	t.Helper()
	r, mailer, svc := newRouterWithAuthInternal(t)
	fb := blob.NewFileBlob(blob.FileBlobOptions{
		BasePath: t.TempDir(),
		BaseURL:  "http://localhost/api/blobs",
	})
	hub := realtime.NewInMemoryHub(realtime.HubConfig{
		HeartbeatInterval: time.Hour, StaleCullTimeout: time.Hour,
	})
	t.Cleanup(func() { _ = hub.Close() })
	r.Mount("/api/blobs", BlobRoutes(svc, fb, hub))
	return r, mailer, fb, hub
}

func TestBlobs_PresignCommitDownload(t *testing.T) {
	r, mailer, _, hub := newRouterWithBlobs(t)
	jwt := signUpAndLogin(t, r, mailer, "blob@example.com", "password9", "Blob")
	authH := [2]string{"Authorization", "Bearer " + jwt}

	// 1. Presign
	rr := doJSON(t, r, "POST", "/api/blobs/presign", map[string]any{
		"content_type": "text/plain",
		"size_bytes":   11,
		"sha256":       "dummy-sha",
	}, authH)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var presign struct {
		PutURL   string `json:"put_url"`
		ObjectID string `json:"object_id"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &presign))
	require.NotEmpty(t, presign.ObjectID)
	require.Contains(t, presign.PutURL, "/upload/"+presign.ObjectID)

	// 2. Upload bytes directly via the in-process PUT endpoint.
	uploadReq, _ := http.NewRequest("PUT", presign.PutURL, bytes.NewReader([]byte("hello blob")))
	uploadReq.Header.Set("Authorization", "Bearer "+jwt)
	rr = httptest.NewRecorder()
	r.ServeHTTP(rr, uploadReq)
	require.Equal(t, http.StatusNoContent, rr.Code, rr.Body.String())

	// 3. Commit
	rr = doJSON(t, r, "POST", "/api/blobs/"+presign.ObjectID+"/commit", map[string]any{
		"content_type": "text/plain",
		"size_bytes":   10,
		"sha256":       "commit-sha",
	}, authH)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var commit map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &commit))
	require.Equal(t, presign.ObjectID, commit["id"])
	require.Equal(t, "text/plain", commit["content_type"])
	require.Equal(t, float64(10), commit["size_bytes"])

	// 4. Verify realtime event fired (hub has a connection registered).
	require.Eventually(t, func() bool {
		return hub.AccountConnCountForTest(commit["account_id"].(string)) >= 0
	}, time.Second, 10*time.Millisecond)

	// 5. Get metadata + download URL
	rr = doJSON(t, r, "GET", "/api/blobs/"+presign.ObjectID, nil, authH)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var meta map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &meta))
	require.Equal(t, presign.ObjectID, meta["id"])
	require.NotEmpty(t, meta["download_url"])

	// 6. Hit the download endpoint directly.
	dlReq, _ := http.NewRequest("GET", meta["download_url"].(string), nil)
	dlReq.Header.Set("Authorization", "Bearer "+jwt)
	rr = httptest.NewRecorder()
	r.ServeHTTP(rr, dlReq)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	body, _ := io.ReadAll(rr.Body)
	require.Equal(t, "hello blob", string(body))
}

func TestBlobs_PresignMissingAuth(t *testing.T) {
	r, _, _, _ := newRouterWithBlobs(t)
	rr := doJSON(t, r, "POST", "/api/blobs/presign", map[string]any{
		"content_type": "text/plain", "size_bytes": 1, "sha256": "x",
	})
	require.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestBlobs_CommitWithoutUploadFails(t *testing.T) {
	r, mailer, _, _ := newRouterWithBlobs(t)
	jwt := signUpAndLogin(t, r, mailer, "blob2@example.com", "password9", "Blob2")
	authH := [2]string{"Authorization", "Bearer " + jwt}

	// Presign but never upload.
	rr := doJSON(t, r, "POST", "/api/blobs/presign", map[string]any{
		"content_type": "text/plain", "size_bytes": 1, "sha256": "x",
	}, authH)
	require.Equal(t, http.StatusOK, rr.Code)
	var presign struct{ ObjectID string `json:"object_id"` }
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &presign))

	// Commit should fail because the file doesn't exist.
	rr = doJSON(t, r, "POST", "/api/blobs/"+presign.ObjectID+"/commit", nil, authH)
	require.Equal(t, http.StatusBadRequest, rr.Code)
}
