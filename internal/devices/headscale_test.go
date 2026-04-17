package devices

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestStubHeadscaler_MintPreAuthKey(t *testing.T) {
	var hs Headscaler = StubHeadscaler{}

	out, err := hs.MintPreAuthKey(context.Background(), PreAuthKeyInput{
		AccountID: "acct1",
		UserID:    "u1",
		DeviceID:  "dev1",
		TTL:       5 * time.Minute,
	})
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(out.Key, "stub-"), "got %q", out.Key)
	require.NotEmpty(t, out.ControlURL)
	require.NotEmpty(t, out.DERPMapJSON)
	require.WithinDuration(t, time.Now().Add(5*time.Minute), out.ExpiresAt, 5*time.Second)
}
