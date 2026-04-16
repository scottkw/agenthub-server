package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

type fakePinger struct{ err error }

func (f fakePinger) Ping(ctx context.Context) error { return f.err }

func TestHealth_OK(t *testing.T) {
	h := NewHealthHandler(fakePinger{nil}, "test-1.2.3")

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	h.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)

	var body map[string]any
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
	require.Equal(t, "ok", body["status"])
	require.Equal(t, "test-1.2.3", body["version"])
	require.Equal(t, "ok", body["db"])
}

func TestHealth_DBFailure_Returns503(t *testing.T) {
	h := NewHealthHandler(fakePinger{errors.New("connection refused")}, "v1")

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	h.ServeHTTP(rr, req)

	require.Equal(t, http.StatusServiceUnavailable, rr.Code)

	var body map[string]any
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
	require.Equal(t, "degraded", body["status"])
	require.Equal(t, "down", body["db"])
}
