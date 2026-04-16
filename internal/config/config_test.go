package config

import (
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
