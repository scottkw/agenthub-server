// Package headscale owns the subprocess lifecycle, gRPC admin client, and
// (account_id, user_id) ↔ headscale user bridge used by the claim flow.
// The real Plan-05 implementation of the devices.Headscaler interface
// lives here; StubHeadscaler in internal/devices stays in the tree for
// tests and for the default (opt-in) disabled mode.
package headscale

import "time"

// Link is one row of the headscale_user_links bridge table.
type Link struct {
	AccountID         string
	UserID            string
	HeadscaleUserID   uint64
	HeadscaleUserName string
	CreatedAt         time.Time
}

// Options configures the Service + supervisor. It mirrors the
// HeadscaleConfig fields the implementation actually needs so the
// package doesn't import config directly.
type Options struct {
	BinaryPath      string
	DataDir         string
	ServerURL       string
	ListenAddr      string
	GRPCListenAddr  string
	UnixSocket      string
	ShutdownTimeout time.Duration
	ReadyTimeout    time.Duration

	DERPEnabled        bool
	DERPRegionID       int
	DERPRegionCode     string
	DERPRegionName     string
	DERPSTUNListenAddr string
	DERPVerifyClients  bool
	DERPIPv4           string
	DERPIPv6           string
}
