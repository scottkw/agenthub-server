package auth

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Claims is the set of facts we put into every AgentHub JWT.
type Claims struct {
	SessionID string        // token jti; matches auth_sessions.id
	UserID    string        // sub
	AccountID string        // aid
	TTL       time.Duration // time to live (from now) — input to Sign
	ExpiresAt time.Time     // output from Parse
	IssuedAt  time.Time
}

// JWTSigner wraps a symmetric HS256 key + an issuer string.
type JWTSigner struct {
	key    []byte
	issuer string
}

// NewJWTSigner builds a signer for a single HS256 key.
func NewJWTSigner(key []byte, issuer string) *JWTSigner {
	return &JWTSigner{key: key, issuer: issuer}
}

type innerClaims struct {
	AccountID string `json:"aid"`
	jwt.RegisteredClaims
}

// Sign returns a signed JWT string. ExpiresAt on the returned token equals now+TTL.
func (s *JWTSigner) Sign(c Claims) (string, error) {
	if c.SessionID == "" || c.UserID == "" || c.AccountID == "" {
		return "", errors.New("Sign: SessionID, UserID, and AccountID are required")
	}
	if c.TTL <= 0 {
		return "", errors.New("Sign: TTL must be > 0")
	}
	now := time.Now().UTC()
	claims := innerClaims{
		AccountID: c.AccountID,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    s.issuer,
			Subject:   c.UserID,
			ID:        c.SessionID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(c.TTL)),
		},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return tok.SignedString(s.key)
}

// Parse verifies the signature, algorithm, issuer, and expiry, and returns
// the facts. Returns an error on any verification failure.
func (s *JWTSigner) Parse(token string) (Claims, error) {
	var out innerClaims
	_, err := jwt.ParseWithClaims(token, &out, func(t *jwt.Token) (any, error) {
		if t.Method != jwt.SigningMethodHS256 {
			return nil, fmt.Errorf("unexpected signing method %q", t.Method.Alg())
		}
		return s.key, nil
	},
		jwt.WithIssuer(s.issuer),
		jwt.WithExpirationRequired(),
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
	)
	if err != nil {
		return Claims{}, err
	}
	return Claims{
		SessionID: out.ID,
		UserID:    out.Subject,
		AccountID: out.AccountID,
		ExpiresAt: out.ExpiresAt.Time,
		IssuedAt:  out.IssuedAt.Time,
	}, nil
}
