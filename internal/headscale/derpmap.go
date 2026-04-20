package headscale

import (
	"encoding/json"
	"fmt"
)

// DERPMapInput is the caller-supplied data for BuildDERPMap. All fields
// except IPv4 and IPv6 are required.
type DERPMapInput struct {
	RegionID   int
	RegionCode string
	RegionName string
	Hostname   string // public hostname the node is reachable at
	DERPPort   int    // typically 443 (we share the HTTPS frontend)
	STUNPort   int    // typically 3478
	IPv4       string // optional — helps NAT traversal when DNS is iffy
	IPv6       string // optional
}

// BuildDERPMap returns a tailcfg.DERPMap-shaped JSON document describing
// our single embedded region. Clients use this as their initial DERP map
// at claim time, before connecting to the control plane.
//
// We don't import tailscale.com/tailcfg to get the struct definitions —
// that would drag in a large dep tree for a single JSON document. The
// local derpMap/derpRegion/derpNode types mirror the tailcfg shapes
// exactly, field names and all, so the JSON is wire-compatible.
func BuildDERPMap(in DERPMapInput) ([]byte, error) {
	if in.RegionID == 0 || in.RegionCode == "" || in.RegionName == "" ||
		in.Hostname == "" || in.DERPPort == 0 || in.STUNPort == 0 {
		return nil, fmt.Errorf("headscale.BuildDERPMap: RegionID, RegionCode, RegionName, Hostname, DERPPort, STUNPort required")
	}

	m := derpMap{
		Regions: map[string]derpRegion{
			fmt.Sprintf("%d", in.RegionID): {
				RegionID:   in.RegionID,
				RegionCode: in.RegionCode,
				RegionName: in.RegionName,
				Nodes: []derpNode{{
					Name:     fmt.Sprintf("%da", in.RegionID),
					RegionID: in.RegionID,
					HostName: in.Hostname,
					IPv4:     in.IPv4,
					IPv6:     in.IPv6,
					DERPPort: in.DERPPort,
					STUNPort: in.STUNPort,
				}},
			},
		},
	}
	return json.Marshal(m)
}

// derpMap / derpRegion / derpNode mirror the Tailscale tailcfg shapes on
// the wire. Field names and JSON tags must stay exact — Tailscale parses
// these via encoding/json with field-name-is-JSON-key semantics.
type derpMap struct {
	Regions map[string]derpRegion `json:"Regions"`
}

type derpRegion struct {
	RegionID   int        `json:"RegionID"`
	RegionCode string     `json:"RegionCode"`
	RegionName string     `json:"RegionName"`
	Nodes      []derpNode `json:"Nodes"`
}

type derpNode struct {
	Name     string `json:"Name"`
	RegionID int    `json:"RegionID"`
	HostName string `json:"HostName"`
	IPv4     string `json:"IPv4,omitempty"`
	IPv6     string `json:"IPv6,omitempty"`
	DERPPort int    `json:"DERPPort,omitempty"`
	STUNPort int    `json:"STUNPort,omitempty"`
}
