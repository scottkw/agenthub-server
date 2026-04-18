package headscale

import (
	"context"
	"net"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	v1 "github.com/juanfont/headscale/gen/go/headscale/v1"
)

// fakeHeadscaleServer implements just enough of HeadscaleService to answer
// a ListUsers round-trip from our wrapper.
type fakeHeadscaleServer struct {
	v1.UnimplementedHeadscaleServiceServer
	lastListUsers *v1.ListUsersRequest
}

func (f *fakeHeadscaleServer) ListUsers(_ context.Context, in *v1.ListUsersRequest) (*v1.ListUsersResponse, error) {
	f.lastListUsers = in
	if in.GetName() == "exists" {
		return &v1.ListUsersResponse{Users: []*v1.User{{Id: 42, Name: "exists"}}}, nil
	}
	return &v1.ListUsersResponse{}, nil
}

func TestClient_FindUserByName(t *testing.T) {
	lis := bufconn.Listen(1024 * 1024)
	srv := grpc.NewServer()
	fake := &fakeHeadscaleServer{}
	v1.RegisterHeadscaleServiceServer(srv, fake)
	go srv.Serve(lis)
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	c := &Client{conn: conn, rpc: v1.NewHeadscaleServiceClient(conn)}

	user, err := c.FindUserByName(context.Background(), "exists")
	require.NoError(t, err)
	require.NotNil(t, user)
	require.Equal(t, uint64(42), user.GetId())

	missing, err := c.FindUserByName(context.Background(), "nobody")
	require.NoError(t, err)
	require.Nil(t, missing)
}
