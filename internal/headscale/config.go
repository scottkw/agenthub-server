package headscale

import (
	"bytes"
	"fmt"
	"path/filepath"
	"text/template"
)

// RenderConfig returns the YAML that Headscale's `serve` subcommand should
// load. Every value is plumbed from Options — no hidden environment
// lookups, no reads from the caller's Config struct — so the output is a
// pure function of the input and trivially testable.
//
// When Options.DERPEnabled is true, the rendered config enables
// Headscale's embedded DERP server (Plan 06) and drops the external
// Tailscale DERP URLs. When false, the map uses Tailscale's public DERP.
func RenderConfig(opts Options) ([]byte, error) {
	if opts.DataDir == "" || opts.ServerURL == "" || opts.ListenAddr == "" ||
		opts.GRPCListenAddr == "" || opts.UnixSocket == "" {
		return nil, fmt.Errorf("headscale.RenderConfig: DataDir, ServerURL, ListenAddr, GRPCListenAddr, UnixSocket are all required")
	}
	if opts.DERPEnabled && opts.DERPSTUNListenAddr == "" {
		return nil, fmt.Errorf("headscale.RenderConfig: DERPSTUNListenAddr is required when DERPEnabled")
	}

	data := map[string]any{
		"ServerURL":          opts.ServerURL,
		"ListenAddr":         opts.ListenAddr,
		"GRPCListenAddr":     opts.GRPCListenAddr,
		"UnixSocket":         opts.UnixSocket,
		"NoiseKeyPath":       filepath.Join(opts.DataDir, "noise_private.key"),
		"DBPath":             filepath.Join(opts.DataDir, "db.sqlite"),
		"DERPEnabled":        opts.DERPEnabled,
		"DERPRegionID":       opts.DERPRegionID,
		"DERPRegionCode":     opts.DERPRegionCode,
		"DERPRegionName":     opts.DERPRegionName,
		"DERPVerifyClients":  opts.DERPVerifyClients,
		"DERPSTUNListenAddr": opts.DERPSTUNListenAddr,
		"DERPKeyPath":        filepath.Join(opts.DataDir, "derp_server_private.key"),
		"DERPIPv4":           opts.DERPIPv4,
		"DERPIPv6":           opts.DERPIPv6,
	}

	var buf bytes.Buffer
	if err := configTmpl.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("headscale.RenderConfig: %w", err)
	}
	return buf.Bytes(), nil
}

var configTmpl = template.Must(template.New("headscale-config").Parse(`server_url: {{.ServerURL}}
listen_addr: {{.ListenAddr}}
metrics_listen_addr: ""
grpc_listen_addr: {{.GRPCListenAddr}}
grpc_allow_insecure: true
unix_socket: {{.UnixSocket}}
unix_socket_permission: "0770"
disable_check_updates: true
noise:
  private_key_path: {{.NoiseKeyPath}}
prefixes:
  v4: 100.64.0.0/10
  v6: fd7a:115c:a1e0::/48
derp:
{{- if .DERPEnabled }}
  server:
    enabled: true
    region_id: {{.DERPRegionID}}
    region_code: {{.DERPRegionCode}}
    region_name: {{.DERPRegionName}}
    verify_clients: {{.DERPVerifyClients}}
    stun_listen_addr: {{.DERPSTUNListenAddr}}
    private_key_path: {{.DERPKeyPath}}
    automatically_add_embedded_derp_region: true
{{- if .DERPIPv4 }}
    ipv4: {{.DERPIPv4}}
{{- end }}
{{- if .DERPIPv6 }}
    ipv6: {{.DERPIPv6}}
{{- end }}
  urls: []
  auto_update_enabled: false
{{- else }}
  urls:
    - https://controlplane.tailscale.com/derpmap/default
  auto_update_enabled: true
{{- end }}
  update_frequency: 24h
database:
  type: sqlite
  sqlite:
    path: {{.DBPath}}
    write_ahead_log: true
log:
  level: info
  format: json
policy:
  mode: file
  path: ""
dns:
  magic_dns: false
  base_domain: ""
  override_local_dns: false
  nameservers:
    global: []
`))
