package httpmw

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/scottkw/agenthub-server/internal/db/migrations"
	"github.com/scottkw/agenthub-server/internal/db/sqlite"
	"github.com/stretchr/testify/require"
)

func TestIdempotency_RetryReturnsCachedResponse(t *testing.T) {
	d, err := sqlite.Open(sqlite.Options{Path: filepath.Join(t.TempDir(), "t.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.Close() })
	require.NoError(t, migrations.Apply(context.Background(), d))

	var hits int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"id":"once"}`))
	})

	mw := NewIdempotency(IdempotencyConfig{DB: d.SQL(), TTL: time.Hour})
	h := mw(handler)

	call := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/x", bytes.NewReader([]byte(`{"a":1}`)))
		req.Header.Set("Idempotency-Key", "key-1")
		req.RemoteAddr = "3.3.3.3:1"
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		return rr
	}

	rr1 := call()
	require.Equal(t, 200, rr1.Code)
	require.Equal(t, `{"id":"once"}`, rr1.Body.String())

	rr2 := call()
	require.Equal(t, 200, rr2.Code)
	require.Equal(t, rr1.Body.String(), rr2.Body.String())

	require.Equal(t, int32(1), atomic.LoadInt32(&hits), "handler must be invoked exactly once")
}

func TestIdempotency_WithoutHeaderPassesThrough(t *testing.T) {
	d, err := sqlite.Open(sqlite.Options{Path: filepath.Join(t.TempDir(), "t.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.Close() })
	require.NoError(t, migrations.Apply(context.Background(), d))

	var hits int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`ok`))
	})
	mw := NewIdempotency(IdempotencyConfig{DB: d.SQL(), TTL: time.Hour})
	h := mw(handler)

	for range 3 {
		req := httptest.NewRequest("POST", "/x", bytes.NewReader([]byte(`{"a":1}`)))
		req.RemoteAddr = "4.4.4.4:1"
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		require.Equal(t, 200, rr.Code)
	}
	require.Equal(t, int32(3), atomic.LoadInt32(&hits))
}
