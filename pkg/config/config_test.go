// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package config

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFromTargetConfig_Nil(t *testing.T) {
	c := FromTargetConfig(nil)
	require.NotNil(t, c)
	require.Empty(t, c.Region)
	require.Empty(t, c.Profile)
}

func TestFromTargetConfig_ParsesRegionAndProfile(t *testing.T) {
	raw := json.RawMessage(`{"Region":"us-west-2","Profile":"dev-role"}`)

	c := FromTargetConfig(raw)
	require.Equal(t, "us-west-2", c.Region)
	require.Equal(t, "dev-role", c.Profile)
}

func TestToAwsConfig_SetsRegion(t *testing.T) {
	c := &Config{Region: "eu-central-1"}

	cfg, err := c.ToAwsConfig(context.Background())
	require.NoError(t, err)
	require.Equal(t, "eu-central-1", cfg.Region)
}

func TestIsTerminal_NonTerminalFile(t *testing.T) {
	// A regular file is not a character device, so it is not a terminal.
	f, err := os.CreateTemp(t.TempDir(), "not-a-tty")
	require.NoError(t, err)
	defer f.Close()

	require.False(t, isTerminal(f))
}
