package headscale

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuildDERPMap_Shape(t *testing.T) {
	raw, err := BuildDERPMap(DERPMapInput{
		RegionID:   999,
		RegionCode: "agenthub",
		RegionName: "AgentHub Embedded DERP",
		Hostname:   "agenthub.example",
		DERPPort:   443,
		STUNPort:   3478,
		IPv4:       "198.51.100.1",
		IPv6:       "2001:db8::1",
	})
	require.NoError(t, err)

	var decoded struct {
		Regions map[string]struct {
			RegionID   int    `json:"RegionID"`
			RegionCode string `json:"RegionCode"`
			RegionName string `json:"RegionName"`
			Nodes      []struct {
				Name     string `json:"Name"`
				RegionID int    `json:"RegionID"`
				HostName string `json:"HostName"`
				IPv4     string `json:"IPv4,omitempty"`
				IPv6     string `json:"IPv6,omitempty"`
				DERPPort int    `json:"DERPPort,omitempty"`
				STUNPort int    `json:"STUNPort,omitempty"`
			} `json:"Nodes"`
		} `json:"Regions"`
	}
	require.NoError(t, json.Unmarshal(raw, &decoded))

	require.Len(t, decoded.Regions, 1)
	r := decoded.Regions["999"]
	require.Equal(t, 999, r.RegionID)
	require.Equal(t, "agenthub", r.RegionCode)
	require.Equal(t, "AgentHub Embedded DERP", r.RegionName)
	require.Len(t, r.Nodes, 1)
	n := r.Nodes[0]
	require.Equal(t, "999a", n.Name)
	require.Equal(t, 999, n.RegionID)
	require.Equal(t, "agenthub.example", n.HostName)
	require.Equal(t, "198.51.100.1", n.IPv4)
	require.Equal(t, "2001:db8::1", n.IPv6)
	require.Equal(t, 443, n.DERPPort)
	require.Equal(t, 3478, n.STUNPort)
}

func TestBuildDERPMap_RejectsMissingRequired(t *testing.T) {
	_, err := BuildDERPMap(DERPMapInput{}) // everything empty
	require.Error(t, err)
}

func TestBuildDERPMap_OmitsEmptyIPs(t *testing.T) {
	raw, err := BuildDERPMap(DERPMapInput{
		RegionID:   999,
		RegionCode: "agenthub",
		RegionName: "AgentHub",
		Hostname:   "agenthub.example",
		DERPPort:   443,
		STUNPort:   3478,
		// no IPv4/IPv6
	})
	require.NoError(t, err)
	require.NotContains(t, string(raw), `"IPv4"`)
	require.NotContains(t, string(raw), `"IPv6"`)
}
