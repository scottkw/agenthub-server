package headscale

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	v1 "github.com/juanfont/headscale/gen/go/headscale/v1"

	"github.com/scottkw/agenthub-server/internal/devices"
)

// clientAPI is the subset of *Client methods Service uses. Declared as an
// interface so tests can swap a fake without a real gRPC connection.
type clientAPI interface {
	FindUserByName(ctx context.Context, name string) (*v1.User, error)
	CreateUser(ctx context.Context, name, displayName, email string) (*v1.User, error)
	CreatePreAuthKey(ctx context.Context, userID uint64, ttl time.Duration) (*v1.PreAuthKey, error)
}

// Service glues the bridge table, the Headscale gRPC client, and the
// devices.Headscaler interface together. Construct one and pass it to
// api.DeviceRoutes.
type Service struct {
	DB          *sql.DB
	Client      clientAPI
	ServerURL   string // what we hand to the claiming device as control_url
	UserPrefix  string // name prefix for Headscale users — e.g. "u-" → "u-<our-user-id>"
	DERPMapJSON string // JSON-encoded tailcfg.DERPMap; if empty, a stub "{"Regions":{}}" is returned (Plan 04 behavior)
}

// Compile-time check that *Service satisfies devices.Headscaler.
var _ devices.Headscaler = (*Service)(nil)

// MintPreAuthKey ensures our user has a linked Headscale user and returns
// a fresh one-shot pre-auth key for that user. Atomic-enough: if the
// Headscale CreateUser call succeeds but the link INSERT fails, we'll see
// the Headscale user on the next attempt via FindUserByName and link it
// then — duplicate-safe.
func (s *Service) MintPreAuthKey(ctx context.Context, in devices.PreAuthKeyInput) (devices.PreAuthKey, error) {
	link, err := s.ensureLink(ctx, in.AccountID, in.UserID)
	if err != nil {
		return devices.PreAuthKey{}, err
	}

	pak, err := s.Client.CreatePreAuthKey(ctx, link.HeadscaleUserID, in.TTL)
	if err != nil {
		return devices.PreAuthKey{}, fmt.Errorf("MintPreAuthKey: %w", err)
	}

	derpMap := s.DERPMapJSON
	if derpMap == "" {
		derpMap = `{"Regions":{}}`
	}
	return devices.PreAuthKey{
		Key:         pak.GetKey(),
		ControlURL:  s.ServerURL,
		DERPMapJSON: derpMap,
		ExpiresAt:   time.Now().Add(in.TTL),
	}, nil
}

// ensureLink returns an existing link row, or creates the Headscale user
// and the link row if needed.
func (s *Service) ensureLink(ctx context.Context, accountID, userID string) (Link, error) {
	existing, err := GetLink(ctx, s.DB, accountID, userID)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Link{}, fmt.Errorf("ensureLink: lookup: %w", err)
	}

	name := s.UserPrefix + userID

	// If Headscale already has a user with our name (e.g. leaked from an
	// earlier aborted attempt), reuse it instead of double-creating.
	found, err := s.Client.FindUserByName(ctx, name)
	if err != nil {
		return Link{}, fmt.Errorf("ensureLink: find: %w", err)
	}
	var hsUser *v1.User
	if found != nil {
		hsUser = found
	} else {
		hsUser, err = s.Client.CreateUser(ctx, name, "", "")
		if err != nil {
			return Link{}, fmt.Errorf("ensureLink: create: %w", err)
		}
	}

	link := Link{
		AccountID:         accountID,
		UserID:            userID,
		HeadscaleUserID:   hsUser.GetId(),
		HeadscaleUserName: hsUser.GetName(),
	}
	if err := CreateLink(ctx, s.DB, link); err != nil {
		return Link{}, fmt.Errorf("ensureLink: link: %w", err)
	}
	return link, nil
}
