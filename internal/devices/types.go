// Package devices owns the device registry and the pair-code / claim
// onboarding flow. Each device is bound to an account and a user, and
// (after Plan 05) to a Headscale node via tailscale_node_id.
package devices

import "time"

// Device is a single registered AgentHub client (desktop, CLI, etc).
type Device struct {
	ID              string
	AccountID       string
	UserID          string
	Name            string
	Platform        string
	AppVersion      string
	TailscaleNodeID string
	LastSeenAt      time.Time
	CreatedAt       time.Time
}

// PairCode is a short-lived, single-use code a signed-in device issues so
// another device can attach itself to the same account.
type PairCode struct {
	Code      string
	AccountID string
	UserID    string
	ExpiresAt time.Time
}

// ClaimInput is the payload a not-yet-authenticated device sends to redeem
// a pair code. The pair code itself authenticates the request.
type ClaimInput struct {
	Code       string
	Name       string
	Platform   string
	AppVersion string
}

// ClaimOutput is what the server returns after a successful claim: a fresh
// device row, the (one-shot-visible) API token raw value, and a Tailscale
// pre-auth-key payload for the client to join the tailnet.
type ClaimOutput struct {
	Device     Device
	APIToken   string // raw ahs_ token — caller's only view
	APITokenID string
	PreAuthKey PreAuthKey
}
