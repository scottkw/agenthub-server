package httpmw

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRateLimit_BlocksAfterBurst(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	mw := NewRateLimit(RateLimitConfig{
		RequestsPerSecond: 1,
		Burst:             3,
		TTL:               time.Minute,
	})
	h := mw(next)

	makeReq := func(ip string) int {
		req := httptest.NewRequest("GET", "/x", nil)
		req.RemoteAddr = ip + ":1234"
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		return rr.Code
	}

	require.Equal(t, 200, makeReq("1.1.1.1"))
	require.Equal(t, 200, makeReq("1.1.1.1"))
	require.Equal(t, 200, makeReq("1.1.1.1"))
	require.Equal(t, http.StatusTooManyRequests, makeReq("1.1.1.1"))

	require.Equal(t, 200, makeReq("2.2.2.2"))
}
