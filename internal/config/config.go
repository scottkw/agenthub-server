// Package config defines the typed server configuration and its loaders.
package config

import (
	"os"
	"path/filepath"
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
