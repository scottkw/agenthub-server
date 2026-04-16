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

type Config struct {
	Mode     Mode       `yaml:"mode"`
	Hostname string     `yaml:"hostname"`
	DataDir  string     `yaml:"data_dir"`
	HTTP     HTTPConfig `yaml:"http"`
	TLS      TLSConfig  `yaml:"tls"`
	DB       DBConfig   `yaml:"db"`
	Obs      ObsConfig  `yaml:"observability"`
	Mail     MailConfig `yaml:"mail"`
	Auth     AuthConfig `yaml:"auth"`
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

	if len(errs) == 0 {
		return nil
	}
	return errors.New("config invalid: " + strings.Join(errs, "; "))
}
