package devices

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"time"
)

// Headscaler is the subset of Headscale functionality the claim flow needs.
// Plan 05 will provide a real implementation backed by the embedded
// Headscale library; Plan 04 uses StubHeadscaler which returns a fake key
// so the rest of the device-onboarding pipeline can be built and tested
// without the Headscale dependency.
type Headscaler interface {
	MintPreAuthKey(ctx context.Context, in PreAuthKeyInput) (PreAuthKey, error)
}

// PreAuthKeyInput describes the pre-auth key the device will use to join
// the tailnet.
type PreAuthKeyInput struct {
	AccountID string
	UserID    string
	DeviceID  string
	TTL       time.Duration
}

// PreAuthKey is what the server hands back to the claiming device so it
// can configure its embedded tsnet / Tailscale client.
type PreAuthKey struct {
	Key         string
	ControlURL  string
	DERPMapJSON string
	ExpiresAt   time.Time
}

// StubHeadscaler returns a fake pre-auth-key payload. It lets us wire and
// test the rest of the claim flow before Plan 05 lands the real Headscale
// integration. Never ship a solo/hosted binary with this in production.
type StubHeadscaler struct{}

func (StubHeadscaler) MintPreAuthKey(_ context.Context, in PreAuthKeyInput) (PreAuthKey, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return PreAuthKey{}, fmt.Errorf("rand: %w", err)
	}
	return PreAuthKey{
		Key:         "stub-" + base64.RawURLEncoding.EncodeToString(buf),
		ControlURL:  "https://stub.invalid/headscale",
		DERPMapJSON: `{"Regions":{}}`,
		ExpiresAt:   time.Now().Add(in.TTL),
	}, nil
}
