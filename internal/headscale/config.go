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
// Plan 06 will extend this to enable embedded DERP; Plan 05 uses
// Tailscale's public DERP map only.
func RenderConfig(opts Options) ([]byte, error) {
	if opts.DataDir == "" || opts.ServerURL == "" || opts.ListenAddr == "" ||
		opts.GRPCListenAddr == "" || opts.UnixSocket == "" {
		return nil, fmt.Errorf("headscale.RenderConfig: DataDir, ServerURL, ListenAddr, GRPCListenAddr, UnixSocket are all required")
	}

	data := map[string]string{
		"ServerURL":      opts.ServerURL,
		"ListenAddr":     opts.ListenAddr,
		"GRPCListenAddr": opts.GRPCListenAddr,
		"UnixSocket":     opts.UnixSocket,
		"NoiseKeyPath":   filepath.Join(opts.DataDir, "noise_private.key"),
		"DBPath":         filepath.Join(opts.DataDir, "db.sqlite"),
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
  urls:
    - https://controlplane.tailscale.com/derpmap/default
  auto_update_enabled: true
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
  override_local_dns: false
  base_domain: ""
  nameservers:
    global: []
`))
