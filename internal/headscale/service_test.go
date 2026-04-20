package headscale

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/juanfont/headscale/gen/go/headscale/v1"

	"github.com/scottkw/agenthub-server/internal/devices"
)

// fakeHSClient implements the subset of *Client methods Service calls.
type fakeHSClient struct {
	users      map[string]*v1.User // by name
	nextUserID uint64
	preauths   []fakeMintCall
}

type fakeMintCall struct {
	UserID uint64
	TTL    time.Duration
}

func (f *fakeHSClient) FindUserByName(_ context.Context, name string) (*v1.User, error) {
	return f.users[name], nil
}

func (f *fakeHSClient) CreateUser(_ context.Context, name, _, _ string) (*v1.User, error) {
	f.nextUserID++
	u := &v1.User{Id: f.nextUserID, Name: name}
	if f.users == nil {
		f.users = map[string]*v1.User{}
	}
	f.users[name] = u
	return u, nil
}

func (f *fakeHSClient) CreatePreAuthKey(_ context.Context, userID uint64, ttl time.Duration) (*v1.PreAuthKey, error) {
	f.preauths = append(f.preauths, fakeMintCall{UserID: userID, TTL: ttl})
	return &v1.PreAuthKey{Key: "hs-preauth-" + time.Now().UTC().Format("150405.000")}, nil
}

func TestService_MintPreAuthKey_CreatesUserOnFirstCall(t *testing.T) {
	db := withBridgeTestDB(t)
	fake := &fakeHSClient{}
	svc := &Service{
		DB:         db,
		Client:     fake,
		ServerURL:  "http://127.0.0.1:18081",
		UserPrefix: "u-",
	}

	out, err := svc.MintPreAuthKey(context.Background(), devices.PreAuthKeyInput{
		AccountID: "acct1", UserID: "u1", DeviceID: "dev1", TTL: 5 * time.Minute,
	})
	require.NoError(t, err)
	require.NotEmpty(t, out.Key)
	require.Equal(t, "http://127.0.0.1:18081", out.ControlURL)
	require.NotEmpty(t, out.DERPMapJSON)
	require.WithinDuration(t, time.Now().Add(5*time.Minute), out.ExpiresAt, 10*time.Second)

	link, err := GetLink(context.Background(), db, "acct1", "u1")
	require.NoError(t, err)
	require.Equal(t, "u-u1", link.HeadscaleUserName)
	require.Equal(t, uint64(1), link.HeadscaleUserID)

	require.Len(t, fake.preauths, 1)
	require.Equal(t, uint64(1), fake.preauths[0].UserID)
}

func TestService_MintPreAuthKey_ReusesLinkedUser(t *testing.T) {
	db := withBridgeTestDB(t)
	fake := &fakeHSClient{}
	// Pre-seed link + Headscale user.
	require.NoError(t, CreateLink(context.Background(), db, Link{
		AccountID: "acct1", UserID: "u1",
		HeadscaleUserID: 42, HeadscaleUserName: "u-u1",
	}))
	fake.users = map[string]*v1.User{"u-u1": {Id: 42, Name: "u-u1"}}
	fake.nextUserID = 42

	svc := &Service{DB: db, Client: fake, ServerURL: "http://x", UserPrefix: "u-"}

	_, err := svc.MintPreAuthKey(context.Background(), devices.PreAuthKeyInput{
		AccountID: "acct1", UserID: "u1", DeviceID: "dev1", TTL: time.Minute,
	})
	require.NoError(t, err)

	// Didn't create a second link row.
	var rows int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM headscale_user_links WHERE account_id='acct1' AND user_id='u1'`).Scan(&rows))
	require.Equal(t, 1, rows)

	// Minted against the linked user id.
	require.Len(t, fake.preauths, 1)
	require.Equal(t, uint64(42), fake.preauths[0].UserID)
}

func TestService_MintPreAuthKey_UsesDERPMapJSON(t *testing.T) {
	db := withBridgeTestDB(t)
	fake := &fakeHSClient{}
	svc := &Service{
		DB:          db,
		Client:      fake,
		ServerURL:   "https://agenthub.example/headscale",
		UserPrefix:  "u-",
		DERPMapJSON: `{"Regions":{"999":{"RegionID":999,"RegionCode":"agenthub","RegionName":"x","Nodes":[]}}}`,
	}

	out, err := svc.MintPreAuthKey(context.Background(), devices.PreAuthKeyInput{
		AccountID: "acct1", UserID: "u1", DeviceID: "dev1", TTL: time.Minute,
	})
	require.NoError(t, err)
	require.Equal(t, svc.DERPMapJSON, out.DERPMapJSON)
}
