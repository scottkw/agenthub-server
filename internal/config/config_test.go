package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

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

func TestValidate_HostedRequiresDBURL(t *testing.T) {
	c := Default()
	c.Mode = ModeHosted
	c.DB.Driver = DriverPostgres
	c.DB.URL = "" // missing
	err := c.Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "db.url")
}

func TestValidate_TLSAutoRequiresEmail(t *testing.T) {
	c := Default()
	c.TLS.Mode = TLSModeAuto
	c.TLS.Email = ""
	c.Hostname = "example.com"
	err := c.Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "tls.email")
}

func TestValidate_Solo_AllowsMissingTLSEmail_WhenOff(t *testing.T) {
	c := Default()
	c.TLS.Mode = TLSModeOff
	require.NoError(t, c.Validate())
}

func TestConfig_MailAndAuthDefaults(t *testing.T) {
	c := Default()
	require.Equal(t, "noop", c.Mail.Provider)
	require.Equal(t, "agenthub-server", c.Auth.Issuer)
	require.Equal(t, 24*time.Hour, c.Auth.SessionTTL)
	require.Equal(t, time.Hour, c.Auth.PasswordResetTTL)
}
