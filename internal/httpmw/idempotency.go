package httpmw

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"time"
)

type IdempotencyConfig struct {
	DB  *sql.DB
	TTL time.Duration
}

// NewIdempotency returns middleware that, when the client sends
// "Idempotency-Key: <key>", caches the first response (status + body) for
// TTL and replays it on subsequent requests with the same key + same
// request body hash. Requests without the header pass through untouched.
//
// Scope for Plan 03 is per-IP ("ip:<addr>"). Once auth-aware middlewares
// are layered in, the wrapping router can pre-populate an account scope
// via request context.
func NewIdempotency(cfg IdempotencyConfig) func(http.Handler) http.Handler {
	if cfg.TTL == 0 {
		cfg.TTL = 24 * time.Hour
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := r.Header.Get("Idempotency-Key")
			if key == "" {
				next.ServeHTTP(w, r)
				return
			}

			var bodyBytes []byte
			if r.Body != nil {
				bodyBytes, _ = io.ReadAll(r.Body)
				r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
			}
			hash := sha256.Sum256(bodyBytes)
			reqHash := hex.EncodeToString(hash[:])
			scope := "ip:" + clientIP(r)

			var code int
			var body []byte
			row := cfg.DB.QueryRowContext(r.Context(),
				`SELECT response_code, response_body FROM idempotency_keys
				 WHERE key = ? AND scope = ? AND request_hash = ? AND expires_at > datetime('now')`,
				key, scope, reqHash)
			if err := row.Scan(&code, &body); err == nil {
				w.WriteHeader(code)
				_, _ = w.Write(body)
				return
			}

			rec := &capturingWriter{ResponseWriter: w, code: 200}
			next.ServeHTTP(rec, r)

			if rec.code >= 200 && rec.code < 300 {
				_, _ = cfg.DB.ExecContext(r.Context(), `
					INSERT INTO idempotency_keys
					  (key, scope, method, path, request_hash, response_code, response_body, expires_at)
					VALUES (?, ?, ?, ?, ?, ?, ?, datetime('now', ?))
					ON CONFLICT(key, scope) DO NOTHING`,
					key, scope, r.Method, r.URL.Path, reqHash,
					rec.code, rec.buf.Bytes(),
					fmt.Sprintf("+%d seconds", int(cfg.TTL.Seconds())),
				)
			}
		})
	}
}

type capturingWriter struct {
	http.ResponseWriter
	code int
	buf  bytes.Buffer
}

func (c *capturingWriter) WriteHeader(code int) {
	c.code = code
	c.ResponseWriter.WriteHeader(code)
}

func (c *capturingWriter) Write(b []byte) (int, error) {
	c.buf.Write(b)
	return c.ResponseWriter.Write(b)
}
