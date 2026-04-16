package auth

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
)

const jwtKeyMetaName = "jwt_signing_key_v1"

// LoadOrCreateJWTKey returns the server's HS256 signing key. If the key is
// absent, it is generated from crypto/rand and stored in app_meta. The key
// is 32 bytes (256 bits) — HS256 input.
//
// Rotation in later plans will write a new versioned key (e.g. v2), verify
// against both, and then retire v1. Plan 02 ships only v1.
func LoadOrCreateJWTKey(ctx context.Context, db *sql.DB) ([]byte, error) {
	var encoded string
	row := db.QueryRowContext(ctx, "SELECT value FROM app_meta WHERE key = ?", jwtKeyMetaName)
	switch err := row.Scan(&encoded); {
	case err == nil:
		b, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, fmt.Errorf("decode jwt key: %w", err)
		}
		if len(b) != 32 {
			return nil, fmt.Errorf("jwt key wrong length: %d", len(b))
		}
		return b, nil
	case errors.Is(err, sql.ErrNoRows):
		// Fall through to create.
	default:
		return nil, fmt.Errorf("lookup jwt key: %w", err)
	}

	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate jwt key: %w", err)
	}

	_, err := db.ExecContext(ctx,
		"INSERT INTO app_meta (key, value) VALUES (?, ?)",
		jwtKeyMetaName, base64.StdEncoding.EncodeToString(key),
	)
	if err != nil {
		return nil, fmt.Errorf("store jwt key: %w", err)
	}
	return key, nil
}
