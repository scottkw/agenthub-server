// Package auth implements password hashing, JWT sessions, verification
// tokens, and HTTP auth middleware.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// OWASP 2024 recommendation for argon2id with 64MB memory:
// memory=65536 KiB, iterations=3, parallelism=4.
const (
	argonMemory      uint32 = 64 * 1024
	argonIterations  uint32 = 3
	argonParallelism uint8  = 4
	argonSaltLen     int    = 16
	argonKeyLen      uint32 = 32
)

// HashPassword returns an encoded argon2id hash.
// Format: $argon2id$v=19$m=65536,t=3,p=4$<salt-b64>$<hash-b64>
func HashPassword(pw string) (string, error) {
	if pw == "" {
		return "", errors.New("HashPassword: empty password")
	}
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("HashPassword: read rand: %w", err)
	}
	key := argon2.IDKey([]byte(pw), salt, argonIterations, argonMemory, argonParallelism, argonKeyLen)

	b64 := base64.RawStdEncoding
	return fmt.Sprintf(
		"$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonIterations, argonParallelism,
		b64.EncodeToString(salt), b64.EncodeToString(key),
	), nil
}

// VerifyPassword returns true iff the supplied password matches the encoded hash.
// Non-nil error means the hash was malformed; the boolean is meaningful only
// when err is nil.
func VerifyPassword(pw, encoded string) (bool, error) {
	parts := strings.Split(encoded, "$")
	// ["", "argon2id", "v=19", "m=...,t=...,p=...", "<salt>", "<hash>"]
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, errors.New("VerifyPassword: not an argon2id hash")
	}

	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return false, fmt.Errorf("VerifyPassword: version: %w", err)
	}
	if version != argon2.Version {
		return false, fmt.Errorf("VerifyPassword: unexpected argon2 version %d", version)
	}

	var memory uint32
	var iterations uint32
	var parallelism uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &parallelism); err != nil {
		return false, fmt.Errorf("VerifyPassword: params: %w", err)
	}

	b64 := base64.RawStdEncoding
	salt, err := b64.DecodeString(parts[4])
	if err != nil {
		return false, fmt.Errorf("VerifyPassword: salt: %w", err)
	}
	expected, err := b64.DecodeString(parts[5])
	if err != nil {
		return false, fmt.Errorf("VerifyPassword: hash: %w", err)
	}

	got := argon2.IDKey([]byte(pw), salt, iterations, memory, parallelism, uint32(len(expected)))
	return subtle.ConstantTimeCompare(got, expected) == 1, nil
}
