// Package config defines the typed server configuration and its loaders.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Mode string

const (
	ModeSolo   Mode = "solo"
	ModeHosted Mode = "hosted"
)

type Driver string

const (
	DriverSQLite   Driver = "sqlite"
	DriverPostgres Driver = "postgres"
)

type TLSMode string

const (
	TLSModeAuto TLSMode = "auto"
	TLSModeFile TLSMode = "file"
	TLSModeOff  TLSMode = "off"
)

type OAuthConfigGroup struct {
	Google OAuthProviderConfig `yaml:"google"`
	GitHub OAuthProviderConfig `yaml:"github"`
}

type OAuthProviderConfig struct {
	ClientID        string `yaml:"client_id"`
	ClientSecret    string `yaml:"client_secret"`
	ClientSecretEnv string `yaml:"client_secret_env"`
	RedirectURL     string `yaml:"redirect_url"`
}

type RateLimitConfig struct {
	RequestsPerSecond float64 `yaml:"requests_per_second"`
	Burst             int     `yaml:"burst"`
}

type HeadscaleConfig struct {
	Enabled           bool          `yaml:"enabled"`
	BinaryPath        string        `yaml:"binary_path"` // path to the `headscale` executable
	DataDir           string        `yaml:"data_dir"`    // where Headscale stores its own SQLite + noise keys
	ServerURL         string        `yaml:"server_url"`  // e.g. https://<hostname>/headscale — public URL embedded in tailnet configs
	ListenAddr        string        `yaml:"listen_addr"` // Headscale's HTTP listen — loopback only
	MetricsListenAddr string        `yaml:"metrics_listen_addr"`
	GRPCListenAddr    string        `yaml:"grpc_listen_addr"` // Required even with UNIX socket; loopback + grpc_allow_insecure=true
	UnixSocket        string        `yaml:"unix_socket"`
	ShutdownTimeout   time.Duration `yaml:"shutdown_timeout"`
	ReadyTimeout      time.Duration `yaml:"ready_timeout"`

	DERPEnabled        bool   `yaml:"derp_enabled"`
	DERPRegionID       int    `yaml:"derp_region_id"`
	DERPRegionCode     string `yaml:"derp_region_code"`
	DERPRegionName     string `yaml:"derp_region_name"`
	DERPSTUNListenAddr string `yaml:"derp_stun_listen_addr"`
	DERPHostname       string `yaml:"derp_hostname"` // public hostname clients use to reach /derp; defaults to top-level Hostname
	DERPPort           int    `yaml:"derp_port"`     // usually 443 (HTTPS frontend)
	DERPIPv4           string `yaml:"derp_ipv4"`
	DERPIPv6           string `yaml:"derp_ipv6"`
	DERPVerifyClients  bool   `yaml:"derp_verify_clients"`
}

type Config struct {
	Mode      Mode             `yaml:"mode"`
	Hostname  string           `yaml:"hostname"`
	DataDir   string           `yaml:"data_dir"`
	HTTP      HTTPConfig       `yaml:"http"`
	TLS       TLSConfig        `yaml:"tls"`
	DB        DBConfig         `yaml:"db"`
	Obs       ObsConfig        `yaml:"observability"`
	Mail      MailConfig       `yaml:"mail"`
	Auth      AuthConfig       `yaml:"auth"`
	OAuth     OAuthConfigGroup `yaml:"oauth"`
	RateLimit RateLimitConfig  `yaml:"rate_limit"`
	Headscale HeadscaleConfig  `yaml:"headscale"`
}

type HTTPConfig struct {
	Port     int `yaml:"port"`
	HTTPPort int `yaml:"http_port"` // for ACME / redirect
}

type TLSConfig struct {
	Mode     TLSMode `yaml:"mode"`
	Email    string  `yaml:"email"`
	CertFile string  `yaml:"cert_file"`
	KeyFile  string  `yaml:"key_file"`
}

type DBConfig struct {
	Driver Driver `yaml:"driver"`
	URL    string `yaml:"url"`
}

type ObsConfig struct {
	LogLevel  string `yaml:"log_level"`
	LogFormat string `yaml:"log_format"`
}

type MailConfig struct {
	Provider string     `yaml:"provider"` // "noop" | "smtp"
	From     string     `yaml:"from"`
	SMTP     SMTPConfig `yaml:"smtp"`
}

type SMTPConfig struct {
	Host        string `yaml:"host"`
	Port        int    `yaml:"port"`
	Username    string `yaml:"username"`
	Password    string `yaml:"password"`
	PasswordEnv string `yaml:"password_env"`
}

type AuthConfig struct {
	Issuer           string        `yaml:"issuer"`
	SessionTTL       time.Duration `yaml:"session_ttl"`
	EmailVerifyTTL   time.Duration `yaml:"email_verify_ttl"`
	PasswordResetTTL time.Duration `yaml:"password_reset_ttl"`
	VerifyURLPrefix  string        `yaml:"verify_url_prefix"`
	ResetURLPrefix   string        `yaml:"reset_url_prefix"`
}

func Default() Config {
	return Config{
		Mode:     ModeSolo,
		Hostname: "localhost",
		DataDir:  defaultDataDir(),
		HTTP: HTTPConfig{
			Port:     443,
			HTTPPort: 80,
		},
		TLS: TLSConfig{
			Mode: TLSModeAuto,
		},
		DB: DBConfig{
			Driver: DriverSQLite,
		},
		Obs: ObsConfig{
			LogLevel:  "info",
			LogFormat: "json",
		},
		Mail: MailConfig{
			Provider: "noop",
		},
		Auth: AuthConfig{
			Issuer:           "agenthub-server",
			SessionTTL:       24 * time.Hour,
			EmailVerifyTTL:   24 * time.Hour,
			PasswordResetTTL: time.Hour,
		},
		RateLimit: RateLimitConfig{
			RequestsPerSecond: 5,
			Burst:             20,
		},
		Headscale: HeadscaleConfig{
			Enabled:           false, // opt-in for Plan 05; Plan 04 StubHeadscaler is the default
			BinaryPath:        "./bin/headscale",
			DataDir:           "", // derived from top-level DataDir at load time
			ServerURL:         "http://127.0.0.1:18081",
			ListenAddr:        "127.0.0.1:18081",
			MetricsListenAddr: "", // disabled by default
			GRPCListenAddr:    "127.0.0.1:50443",
			UnixSocket:        "", // derived from DataDir at load time
			ShutdownTimeout:   10 * time.Second,
			ReadyTimeout:      10 * time.Second,

			DERPEnabled:        false,
			DERPRegionID:       999,
			DERPRegionCode:     "agenthub",
			DERPRegionName:     "AgentHub Embedded DERP",
			DERPSTUNListenAddr: "0.0.0.0:3478",
			DERPHostname:       "", // derived from top-level Hostname at load time
			DERPPort:           443,
			DERPIPv4:           "",
			DERPIPv6:           "",
			DERPVerifyClients:  true,
		},
	}
}

func defaultDataDir() string {
	if dir := os.Getenv("XDG_DATA_HOME"); dir != "" {
		return filepath.Join(dir, "agenthub-server")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "./data"
	}
	return filepath.Join(home, ".local", "share", "agenthub-server")
}

type LoadOptions struct {
	ConfigPath string
}

// Load resolves configuration from defaults, optional YAML, and environment.
// Precedence (highest wins): env → YAML → defaults.
func Load(opts LoadOptions) (Config, error) {
	c := Default()

	if opts.ConfigPath != "" {
		b, err := os.ReadFile(opts.ConfigPath)
		if err != nil {
			return Config{}, fmt.Errorf("read config: %w", err)
		}
		if err := yaml.Unmarshal(b, &c); err != nil {
			return Config{}, fmt.Errorf("parse config: %w", err)
		}
	}

	applyEnv(&c)

	// Derive Headscale paths from DataDir if the caller didn't override them.
	if c.Headscale.DataDir == "" {
		c.Headscale.DataDir = filepath.Join(c.DataDir, "headscale")
	}
	if c.Headscale.UnixSocket == "" {
		c.Headscale.UnixSocket = filepath.Join(c.Headscale.DataDir, "headscale.sock")
	}
	if c.Headscale.DERPHostname == "" {
		c.Headscale.DERPHostname = c.Hostname
	}

	return c, nil
}

func applyEnv(c *Config) {
	if v := os.Getenv("AGENTHUB_MODE"); v != "" {
		c.Mode = Mode(v)
	}
	if v := os.Getenv("AGENTHUB_HOSTNAME"); v != "" {
		c.Hostname = v
	}
	if v := os.Getenv("AGENTHUB_DATA_DIR"); v != "" {
		c.DataDir = v
	}
	if v := os.Getenv("AGENTHUB_HTTP_PORT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.HTTP.Port = n
		}
	}
	if v := os.Getenv("AGENTHUB_TLS_MODE"); v != "" {
		c.TLS.Mode = TLSMode(v)
	}
	if v := os.Getenv("AGENTHUB_TLS_EMAIL"); v != "" {
		c.TLS.Email = v
	}
	if v := os.Getenv("AGENTHUB_DB_DRIVER"); v != "" {
		c.DB.Driver = Driver(v)
	}
	if v := os.Getenv("AGENTHUB_DB_URL"); v != "" {
		c.DB.URL = v
		if c.DB.Driver == "" {
			c.DB.Driver = DriverPostgres
		}
	}
	if v := os.Getenv("AGENTHUB_LOG_LEVEL"); v != "" {
		c.Obs.LogLevel = v
	}
	if v := os.Getenv("AGENTHUB_LOG_FORMAT"); v != "" {
		c.Obs.LogFormat = v
	}
	if v := os.Getenv("AGENTHUB_MAIL_PROVIDER"); v != "" {
		c.Mail.Provider = v
	}
	if v := os.Getenv("AGENTHUB_MAIL_FROM"); v != "" {
		c.Mail.From = v
	}
	if v := os.Getenv("AGENTHUB_MAIL_SMTP_HOST"); v != "" {
		c.Mail.SMTP.Host = v
	}
	if v := os.Getenv("AGENTHUB_MAIL_SMTP_PORT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.Mail.SMTP.Port = n
		}
	}
	if v := os.Getenv("AGENTHUB_MAIL_SMTP_USER"); v != "" {
		c.Mail.SMTP.Username = v
	}
	if v := os.Getenv("AGENTHUB_MAIL_SMTP_PASS"); v != "" {
		c.Mail.SMTP.Password = v
	}
	if v := os.Getenv("AGENTHUB_VERIFY_URL_PREFIX"); v != "" {
		c.Auth.VerifyURLPrefix = v
	}
	if v := os.Getenv("AGENTHUB_RESET_URL_PREFIX"); v != "" {
		c.Auth.ResetURLPrefix = v
	}
	if c.Mail.SMTP.PasswordEnv != "" {
		if v := os.Getenv(c.Mail.SMTP.PasswordEnv); v != "" {
			c.Mail.SMTP.Password = v
		}
	}
	if v := os.Getenv("AGENTHUB_OAUTH_GOOGLE_CLIENT_ID"); v != "" {
		c.OAuth.Google.ClientID = v
	}
	if v := os.Getenv("AGENTHUB_OAUTH_GOOGLE_CLIENT_SECRET"); v != "" {
		c.OAuth.Google.ClientSecret = v
	}
	if v := os.Getenv("AGENTHUB_OAUTH_GOOGLE_REDIRECT_URL"); v != "" {
		c.OAuth.Google.RedirectURL = v
	}
	if v := os.Getenv("AGENTHUB_OAUTH_GITHUB_CLIENT_ID"); v != "" {
		c.OAuth.GitHub.ClientID = v
	}
	if v := os.Getenv("AGENTHUB_OAUTH_GITHUB_CLIENT_SECRET"); v != "" {
		c.OAuth.GitHub.ClientSecret = v
	}
	if v := os.Getenv("AGENTHUB_OAUTH_GITHUB_REDIRECT_URL"); v != "" {
		c.OAuth.GitHub.RedirectURL = v
	}
	if c.OAuth.Google.ClientSecretEnv != "" {
		if v := os.Getenv(c.OAuth.Google.ClientSecretEnv); v != "" {
			c.OAuth.Google.ClientSecret = v
		}
	}
	if c.OAuth.GitHub.ClientSecretEnv != "" {
		if v := os.Getenv(c.OAuth.GitHub.ClientSecretEnv); v != "" {
			c.OAuth.GitHub.ClientSecret = v
		}
	}
	if v := os.Getenv("AGENTHUB_HEADSCALE_ENABLED"); v != "" {
		c.Headscale.Enabled = v == "1" || strings.EqualFold(v, "true")
	}
	if v := os.Getenv("AGENTHUB_HEADSCALE_BINARY_PATH"); v != "" {
		c.Headscale.BinaryPath = v
	}
	if v := os.Getenv("AGENTHUB_HEADSCALE_DATA_DIR"); v != "" {
		c.Headscale.DataDir = v
	}
	if v := os.Getenv("AGENTHUB_HEADSCALE_SERVER_URL"); v != "" {
		c.Headscale.ServerURL = v
	}
	if v := os.Getenv("AGENTHUB_HEADSCALE_LISTEN_ADDR"); v != "" {
		c.Headscale.ListenAddr = v
	}
	if v := os.Getenv("AGENTHUB_HEADSCALE_UNIX_SOCKET"); v != "" {
		c.Headscale.UnixSocket = v
	}
	if v := os.Getenv("AGENTHUB_HEADSCALE_DERP_ENABLED"); v != "" {
		c.Headscale.DERPEnabled = v == "1" || strings.EqualFold(v, "true")
	}
	if v := os.Getenv("AGENTHUB_HEADSCALE_DERP_REGION_ID"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.Headscale.DERPRegionID = n
		}
	}
	if v := os.Getenv("AGENTHUB_HEADSCALE_DERP_REGION_CODE"); v != "" {
		c.Headscale.DERPRegionCode = v
	}
	if v := os.Getenv("AGENTHUB_HEADSCALE_DERP_HOSTNAME"); v != "" {
		c.Headscale.DERPHostname = v
	}
	if v := os.Getenv("AGENTHUB_HEADSCALE_DERP_PORT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.Headscale.DERPPort = n
		}
	}
	if v := os.Getenv("AGENTHUB_HEADSCALE_DERP_IPV4"); v != "" {
		c.Headscale.DERPIPv4 = v
	}
	if v := os.Getenv("AGENTHUB_HEADSCALE_DERP_IPV6"); v != "" {
		c.Headscale.DERPIPv6 = v
	}
	if v := os.Getenv("AGENTHUB_HEADSCALE_DERP_STUN_LISTEN_ADDR"); v != "" {
		c.Headscale.DERPSTUNListenAddr = v
	}
}

// Validate returns an error describing any invalid or inconsistent config.
func (c Config) Validate() error {
	var errs []string

	switch c.Mode {
	case ModeSolo, ModeHosted:
	default:
		errs = append(errs, fmt.Sprintf("mode: invalid value %q", c.Mode))
	}

	switch c.DB.Driver {
	case DriverSQLite:
		// URL optional (defaults to DataDir/agenthub.db at open time).
	case DriverPostgres:
		if c.DB.URL == "" {
			errs = append(errs, "db.url: required when db.driver=postgres")
		}
	default:
		errs = append(errs, fmt.Sprintf("db.driver: invalid value %q", c.DB.Driver))
	}

	if c.Mode == ModeHosted && c.DB.URL == "" {
		errs = append(errs, "db.url: required when mode=hosted")
	}

	switch c.TLS.Mode {
	case TLSModeOff:
	case TLSModeAuto:
		if c.TLS.Email == "" {
			errs = append(errs, "tls.email: required when tls.mode=auto (ACME registration)")
		}
	case TLSModeFile:
		if c.TLS.CertFile == "" || c.TLS.KeyFile == "" {
			errs = append(errs, "tls.cert_file and tls.key_file: required when tls.mode=file")
		}
	default:
		errs = append(errs, fmt.Sprintf("tls.mode: invalid value %q", c.TLS.Mode))
	}

	if c.HTTP.Port <= 0 || c.HTTP.Port > 65535 {
		errs = append(errs, fmt.Sprintf("http.port: invalid port %d", c.HTTP.Port))
	}
	if c.HTTP.HTTPPort < 0 || c.HTTP.HTTPPort > 65535 {
		errs = append(errs, fmt.Sprintf("http.http_port: invalid port %d", c.HTTP.HTTPPort))
	}

	if c.DataDir != "" {
		info, err := os.Stat(c.DataDir)
		if err != nil && !os.IsNotExist(err) {
			errs = append(errs, fmt.Sprintf("data_dir: cannot stat: %v", err))
		} else if err == nil && !info.IsDir() {
			errs = append(errs, "data_dir: exists but is not a directory")
		}
	}

	if c.Headscale.Enabled {
		if c.Headscale.BinaryPath == "" {
			errs = append(errs, "headscale.binary_path: required when headscale.enabled")
		}
		if _, err := os.Stat(c.Headscale.BinaryPath); err != nil {
			errs = append(errs, fmt.Sprintf("headscale.binary_path: cannot stat %q: %v", c.Headscale.BinaryPath, err))
		}
		if c.Headscale.ServerURL == "" {
			errs = append(errs, "headscale.server_url: required when headscale.enabled")
		}
		if c.Headscale.ListenAddr == "" {
			errs = append(errs, "headscale.listen_addr: required when headscale.enabled")
		}
		if c.Headscale.DERPEnabled {
			if c.Headscale.DERPSTUNListenAddr == "" {
				errs = append(errs, "headscale.derp_stun_listen_addr: required when headscale.derp_enabled")
			}
			if c.Headscale.DERPRegionID == 0 {
				errs = append(errs, "headscale.derp_region_id: required (non-zero) when headscale.derp_enabled")
			}
			if c.Headscale.DERPHostname == "" {
				errs = append(errs, "headscale.derp_hostname: required when headscale.derp_enabled (fallback to top-level hostname works if that's set)")
			}
		}
	}

	if len(errs) == 0 {
		return nil
	}
	return errors.New("config invalid: " + strings.Join(errs, "; "))
}
