package headscale

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRenderConfig_MatchesGolden(t *testing.T) {
	got, err := RenderConfig(Options{
		BinaryPath:     "/usr/local/bin/headscale",
		DataDir:        "/tmp/fixture/hs",
		ServerURL:      "http://127.0.0.1:18081",
		ListenAddr:     "127.0.0.1:18081",
		GRPCListenAddr: "127.0.0.1:50443",
		UnixSocket:     "/tmp/fixture/hs/headscale.sock",
	})
	require.NoError(t, err)

	want, err := os.ReadFile(filepath.Join("testdata", "expected_config.yaml"))
	require.NoError(t, err)

	require.Equal(t, string(want), string(got))
}

func TestRenderConfig_RejectsEmptyOptions(t *testing.T) {
	_, err := RenderConfig(Options{})
	require.Error(t, err)
}

func TestRenderConfig_DERPEnabled_MatchesGolden(t *testing.T) {
	got, err := RenderConfig(Options{
		BinaryPath:         "/usr/local/bin/headscale",
		DataDir:            "/tmp/fixture/hs",
		ServerURL:          "https://agenthub.example/headscale",
		ListenAddr:         "127.0.0.1:18081",
		GRPCListenAddr:     "127.0.0.1:50443",
		UnixSocket:         "/tmp/fixture/hs/headscale.sock",
		DERPEnabled:        true,
		DERPRegionID:       999,
		DERPRegionCode:     "agenthub",
		DERPRegionName:     "AgentHub Embedded DERP",
		DERPSTUNListenAddr: "0.0.0.0:3478",
		DERPVerifyClients:  true,
		DERPIPv4:           "198.51.100.1",
		DERPIPv6:           "2001:db8::1",
	})
	require.NoError(t, err)

	want, err := os.ReadFile(filepath.Join("testdata", "expected_config_derp.yaml"))
	require.NoError(t, err)

	require.Equal(t, string(want), string(got))
}
