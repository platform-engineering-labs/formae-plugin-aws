// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package main

// These tests exercise aws#Config's PluginConfig — the agent-side plugin
// configuration — rather than a target's Config. The distinction matters:
// the rest of this suite renders formas, which never instantiate
// PluginConfig, so a PluginConfig that cannot be evaluated passes every
// other test here and only fails when an agent boots.
//
// They need a formae binary (FORMAE_BINARY) and the plugin installed via
// `make install`, exactly as schema_auth_test.go does.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// evalAgentConfig writes profileBody as a formae profile and runs a command
// that loads it, returning the combined output. Any command that resolves a
// profile evaluates its PKL, so a schema that cannot be instantiated surfaces
// here regardless of whether the agent is reachable.
func evalAgentConfig(t *testing.T, binary, profileBody string) string {
	t.Helper()

	dir := t.TempDir()
	profiles := filepath.Join(dir, "profiles")
	require.NoError(t, os.MkdirAll(profiles, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(profiles, "fixture.pkl"), []byte(profileBody), 0o644))

	cmd := exec.Command(binary, "agent", "status", "--profile", "fixture") //nolint:gosec // test-only
	cmd.Env = append(os.Environ(), "FORMAE_CONFIG_DIR="+dir, "FORMAE_TEST_RUN_ID=unit")
	out, _ := cmd.CombinedOutput() // a connection failure is fine; a PKL error is not
	return string(out)
}

func TestPluginConfig_AllowedAuthMethodsIsAmendable(t *testing.T) {
	binary := os.Getenv("FORMAE_BINARY")
	if binary == "" {
		t.Skip("set FORMAE_BINARY to run")
	}

	// The hosted installation config restricts AWS targets to OIDC exactly
	// this way. It must evaluate; the agent cannot start otherwise.
	const profile = `amends "formae:/Config.pkl"

import "plugins:/Aws.pkl" as Aws

agent {
    server {
        nodename = "formae"
        hostname = "localhost"
    }
    synchronization { enabled = false }
    discovery { enabled = false }
    resourcePlugins {
        new Aws.PluginConfig {
            allowedAuthMethods { "Oidc" }
        }
    }
}

cli {
    api {
        url = "http://localhost"
        port = 49684
    }
}
`
	out := evalAgentConfig(t, binary, profile)
	if strings.Contains(out, "Pkl Error") {
		t.Fatalf("restricting allowedAuthMethods must evaluate, got a PKL error:\n%s", out)
	}
}

func TestPluginConfig_AllowedAuthMethodsDefaultsToUnrestricted(t *testing.T) {
	binary := os.Getenv("FORMAE_BINARY")
	if binary == "" {
		t.Skip("set FORMAE_BINARY to run")
	}

	// Leaving the field alone must also evaluate. This is the shape every
	// agent that does not restrict auth uses.
	const profile = `amends "formae:/Config.pkl"

import "plugins:/Aws.pkl" as Aws

agent {
    server {
        nodename = "formae"
        hostname = "localhost"
    }
    synchronization { enabled = false }
    discovery { enabled = false }
    resourcePlugins {
        new Aws.PluginConfig {}
    }
}

cli {
    api {
        url = "http://localhost"
        port = 49684
    }
}
`
	out := evalAgentConfig(t, binary, profile)
	if strings.Contains(out, "Pkl Error") {
		t.Fatalf("an unrestricted PluginConfig must evaluate, got a PKL error:\n%s", out)
	}
}
