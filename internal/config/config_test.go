package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDefault_SoloMode(t *testing.T) {
	c := Default()
	require.Equal(t, ModeSolo, c.Mode)
	require.Equal(t, DriverSQLite, c.DB.Driver)
	require.NotEmpty(t, c.DataDir)
	require.Equal(t, 443, c.HTTP.Port)
	require.Equal(t, TLSModeAuto, c.TLS.Mode)
	require.Equal(t, "info", c.Obs.LogLevel)
}

func TestLoad_YAMLAndEnvPrecedence(t *testing.T) {
	yamlPath := filepath.Join(t.TempDir(), "c.yaml")
	require.NoError(t, os.WriteFile(yamlPath, []byte(
		"mode: hosted\nhostname: example.test\ndb:\n  driver: postgres\n  url: postgres://yaml\n",
	), 0o600))

	t.Setenv("AGENTHUB_DB_URL", "postgres://env")
	t.Setenv("AGENTHUB_HOSTNAME", "")

	c, err := Load(LoadOptions{ConfigPath: yamlPath})
	require.NoError(t, err)

	// YAML overrides defaults.
	require.Equal(t, ModeHosted, c.Mode)
	require.Equal(t, "example.test", c.Hostname)
	// Env overrides YAML.
	require.Equal(t, "postgres://env", c.DB.URL)
	require.Equal(t, DriverPostgres, c.DB.Driver)
}

func TestLoad_NoConfigFile_ReturnsDefaults(t *testing.T) {
	c, err := Load(LoadOptions{ConfigPath: ""})
	require.NoError(t, err)
	require.Equal(t, ModeSolo, c.Mode)
}
