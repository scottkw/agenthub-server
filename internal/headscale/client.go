package headscale

import (
	"context"
	"fmt"
	"net"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	v1 "github.com/juanfont/headscale/gen/go/headscale/v1"
)

// Client is a thin wrapper around the generated HeadscaleServiceClient.
// It connects over Headscale's UNIX socket (no TLS, no API key — socket
// permissions are the gate) and exposes the narrow surface our claim
// flow actually uses: FindUserByName, CreateUser, CreatePreAuthKey.
type Client struct {
	conn *grpc.ClientConn
	rpc  v1.HeadscaleServiceClient
}

// Dial connects to the UNIX socket at socketPath. The caller is
// responsible for eventually calling Close.
func Dial(socketPath string) (*Client, error) {
	conn, err := grpc.NewClient(
		"passthrough:///unix",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socketPath)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil, fmt.Errorf("headscale.Dial: %w", err)
	}
	return &Client{conn: conn, rpc: v1.NewHeadscaleServiceClient(conn)}, nil
}

// Close shuts the underlying gRPC connection.
func (c *Client) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

// FindUserByName returns the first User matching name, or nil if no user
// matches. Headscale has no GetUser RPC — ListUsers with a name filter is
// the documented idiom.
func (c *Client) FindUserByName(ctx context.Context, name string) (*v1.User, error) {
	resp, err := c.rpc.ListUsers(ctx, &v1.ListUsersRequest{Name: name})
	if err != nil {
		return nil, fmt.Errorf("headscale.FindUserByName: %w", err)
	}
	for _, u := range resp.GetUsers() {
		if u.GetName() == name {
			return u, nil
		}
	}
	return nil, nil
}

// CreateUser creates a Headscale user.
func (c *Client) CreateUser(ctx context.Context, name, displayName, email string) (*v1.User, error) {
	resp, err := c.rpc.CreateUser(ctx, &v1.CreateUserRequest{
		Name:        name,
		DisplayName: displayName,
		Email:       email,
	})
	if err != nil {
		return nil, fmt.Errorf("headscale.CreateUser: %w", err)
	}
	return resp.GetUser(), nil
}

// CreatePreAuthKey mints a one-shot, non-reusable, non-ephemeral pre-auth
// key bound to the given Headscale user id, valid for ttl.
func (c *Client) CreatePreAuthKey(ctx context.Context, userID uint64, ttl time.Duration) (*v1.PreAuthKey, error) {
	resp, err := c.rpc.CreatePreAuthKey(ctx, &v1.CreatePreAuthKeyRequest{
		User:       userID,
		Reusable:   false,
		Ephemeral:  false,
		Expiration: timestampFromTime(time.Now().Add(ttl)),
	})
	if err != nil {
		return nil, fmt.Errorf("headscale.CreatePreAuthKey: %w", err)
	}
	return resp.GetPreAuthKey(), nil
}
