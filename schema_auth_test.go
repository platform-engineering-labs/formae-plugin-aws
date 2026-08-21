// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package main

// These tests exercise the polymorphic Auth schema on aws#Config through
// the real formae eval path: a forma file is rendered by a live formae
// binary against the plugin as installed by `make install`, and the
// resulting target Config JSON is asserted against its expected shape.
//
// They only run when FORMAE_BINARY points at a formae binary (they are
// skipped otherwise, e.g. in CI, which has no formae binary available).
// Before running locally:
//
//	make install
//	FORMAE_BINARY=/path/to/formae go test -tags=unit -run TestAuthSchema -v .

import (
	"encoding/json"
	"os"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type evalResult struct {
	Targets []struct {
		Config json.RawMessage `json:"Config"`
	} `json:"Targets"`
}

// runEval evaluates a testdata fixture with the formae binary named by
// FORMAE_BINARY and returns the target Config JSON of the first target.
// It fails the (sub)test if the binary is missing from FORMAE_BINARY, and
// returns the raw stderr and a non-nil error when evaluation itself fails
// so callers can assert on rejection.
func runEval(t *testing.T, binary, fixture string) (json.RawMessage, []byte, error) {
	t.Helper()

	cmd := exec.Command(binary, "eval", //nolint:gosec // test-only, binary path comes from a trusted env var
		"--output-consumer", "machine",
		"--schema-location", "local",
		fixture,
	)
	cmd.Env = append(os.Environ(), "FORMAE_TEST_RUN_ID=unit")

	stdout, err := cmd.Output()
	if err != nil {
		var stderr []byte
		if exitErr, ok := err.(*exec.ExitError); ok {
			stderr = exitErr.Stderr
		}
		return nil, stderr, err
	}

	var result evalResult
	require.NoError(t, json.Unmarshal(stdout, &result), "eval output was not valid JSON: %s", stdout)
	require.Len(t, result.Targets, 1, "expected exactly one target in eval output")

	return result.Targets[0].Config, nil, nil
}

func TestAuthSchema(t *testing.T) {
	binary := os.Getenv("FORMAE_BINARY")
	if binary == "" {
		t.Skip("FORMAE_BINARY not set; skipping formae-eval-backed schema tests")
	}

	t.Run("flat profile with no auth block renders Profile and omits Auth", func(t *testing.T) {
		config, _, err := runEval(t, binary, "testdata/aws-config-auth-flat.pkl")
		require.NoError(t, err)

		assert.JSONEq(t, `{"Type":"AWS","Profile":"legacy-profile","Region":"us-east-1"}`, string(config))
	})

	t.Run("auth = DefaultChainAuth renders a nested Auth object", func(t *testing.T) {
		config, _, err := runEval(t, binary, "testdata/aws-config-auth-defaultchain.pkl")
		require.NoError(t, err)

		assert.JSONEq(t, `{
			"Type":"AWS",
			"Region":"us-east-1",
			"Auth":{"Type":"DefaultChain","Profile":"chain-profile"}
		}`, string(config))
	})

	t.Run("auth = OidcAuth renders a nested Auth object", func(t *testing.T) {
		config, _, err := runEval(t, binary, "testdata/aws-config-auth-oidc.pkl")
		require.NoError(t, err)

		assert.JSONEq(t, `{
			"Type":"AWS",
			"Region":"us-east-1",
			"Auth":{"Type":"Oidc","RoleArn":"arn:aws:iam::123456789012:role/formae-agent"}
		}`, string(config))
	})

	t.Run("setting both profile and auth is rejected at eval", func(t *testing.T) {
		_, stderr, err := runEval(t, binary, "testdata/aws-config-auth-both-set-rejected.pkl")
		require.Error(t, err)
		assert.Contains(t, string(stderr), "Type constraint")
		assert.Contains(t, string(stderr), "this == null || profile == null")
	})
}
