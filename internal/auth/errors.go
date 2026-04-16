package auth

import "errors"

var (
	ErrInvalidCredentials = errors.New("auth: invalid credentials")
	ErrEmailNotVerified   = errors.New("auth: email not verified")
	ErrSessionRevoked     = errors.New("auth: session revoked")
	ErrSessionExpired     = errors.New("auth: session expired")
	ErrTokenExpired       = errors.New("auth: verification token expired or consumed")
	ErrTokenNotFound      = errors.New("auth: verification token not found")
)
