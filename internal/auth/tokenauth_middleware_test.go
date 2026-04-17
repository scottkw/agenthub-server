package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRequireAuthOrToken_AcceptsJWT(t *testing.T) {
	db := withTestDB(t)
	key := make([]byte, 32)
	signer := NewJWTSigner(key, "agenthub-server")

	seedActiveSession(t, db, "jti1")
	jwt, _ := signer.Sign(Claims{SessionID: "jti1", UserID: "u1", AccountID: "acct1", TTL: time.Hour})

	h := RequireAuthOrToken(signer, db)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "u1", UserID(r.Context()))
		w.WriteHeader(200)
	}))

	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set("Authorization", "Bearer "+jwt)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	require.Equal(t, 200, rr.Code)
}

func TestRequireAuthOrToken_AcceptsAPIToken(t *testing.T) {
	db := withTestDB(t)
	key := make([]byte, 32)
	signer := NewJWTSigner(key, "agenthub-server")

	raw, _, err := CreateAPIToken(context.Background(), db, APITokenInput{
		ID: "t1", AccountID: "acct1", UserID: "u1", Name: "cli",
	})
	require.NoError(t, err)

	h := RequireAuthOrToken(signer, db)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "u1", UserID(r.Context()))
		require.Equal(t, "acct1", AccountID(r.Context()))
		require.Equal(t, "api-token:t1", SessionID(r.Context()), "api token marks session as api-token:<id>")
		w.WriteHeader(200)
	}))

	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set("Authorization", "Token "+raw)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	require.Equal(t, 200, rr.Code)
}

func TestRequireAuthOrToken_RejectsNeither(t *testing.T) {
	db := withTestDB(t)
	signer := NewJWTSigner(make([]byte, 32), "agenthub-server")

	h := RequireAuthOrToken(signer, db)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	req := httptest.NewRequest("GET", "/x", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	require.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestRequireAuthOrToken_InjectsDeviceID(t *testing.T) {
	db := withTestDB(t)
	key, err := LoadOrCreateJWTKey(context.Background(), db)
	require.NoError(t, err)
	signer := NewJWTSigner(key, "test")

	raw, rec, err := CreateAPIToken(context.Background(), db, APITokenInput{
		ID: "tok1", AccountID: "acct1", UserID: "u1", DeviceID: "dev1", Name: "d",
	})
	require.NoError(t, err)
	require.Equal(t, "dev1", rec.DeviceID)

	var sawDeviceID string
	h := RequireAuthOrToken(signer, db)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawDeviceID = DeviceID(r.Context())
	}))

	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Token "+raw)
	h.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	require.Equal(t, "dev1", sawDeviceID)
}
