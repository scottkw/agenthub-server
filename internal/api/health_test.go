package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type fakePinger struct {
	err error
}

func (f *fakePinger) Ping(_ context.Context) error { return f.err }

func TestHealth_OK(t *testing.T) {
	h := NewHealthHandler(&fakePinger{}, "v-test")
	h.StartTime = time.Now().Add(-time.Second)

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/healthz", nil))
	require.Equal(t, http.StatusOK, rr.Code)

	var body map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body))
	require.Equal(t, "ok", body["status"])
	require.Equal(t, "v-test", body["version"])
	require.Greater(t, body["uptime_sec"], float64(0))

	goStats, ok := body["go"].(map[string]any)
	require.True(t, ok)
	require.Greater(t, goStats["goroutines"], float64(0))
}

func TestHealth_DBFailure_Returns503(t *testing.T) {
	h := NewHealthHandler(&fakePinger{err: errors.New("db down")}, "v-test")

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/healthz", nil))
	require.Equal(t, http.StatusServiceUnavailable, rr.Code)

	var body map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body))
	require.Equal(t, "degraded", body["status"])
	require.Equal(t, "down", body["db"])
}
