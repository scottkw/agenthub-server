// Package config defines the typed server configuration and its loaders.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"

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
}
