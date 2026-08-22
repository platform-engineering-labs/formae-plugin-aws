// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package config

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// countingHandler is a slog.Handler that counts the records routed to it, so
// a test can assert a warning fired a specific number of times without
// inspecting message text.
type countingHandler struct{ count *int }

func (h countingHandler) Enabled(context.Context, slog.Level) bool  { return true }
func (h countingHandler) Handle(context.Context, slog.Record) error { *h.count++; return nil }
func (h countingHandler) WithAttrs([]slog.Attr) slog.Handler        { return h }
func (h countingHandler) WithGroup(string) slog.Handler             { return h }

// stubTokenSource is a plugin.OidcTokenSource that always mints the same
// token, so a Config can carry non-nil OidcDeps with a non-nil Source and
// exercise the wired path rather than the "nothing wired at all" fail-closed
// path.
type stubTokenSource struct{}

func (stubTokenSource) IdentityToken(context.Context, string) (string, error) {
	return "stub-token", nil
}

func TestEffectiveAuth(t *testing.T) {
	tests := []struct {
		name     string
		config   Config
		wantType string
		wantAuth string // JSON to compare against, empty means "don't check"
		wantErr  string
	}{
		{
			name:     "flat profile synthesises DefaultChain",
			config:   Config{Profile: "legacy-profile"},
			wantType: AuthTypeDefaultChain,
			wantAuth: `{"Type":"DefaultChain","Profile":"legacy-profile"}`,
		},
		{
			name:     "no profile and no auth synthesises DefaultChain with an empty profile",
			config:   Config{},
			wantType: AuthTypeDefaultChain,
			wantAuth: `{"Type":"DefaultChain","Profile":""}`,
		},
		{
			name:     "explicit DefaultChain passes through unchanged",
			config:   Config{Auth: json.RawMessage(`{"Type":"DefaultChain","Profile":"chain-profile"}`)},
			wantType: AuthTypeDefaultChain,
			wantAuth: `{"Type":"DefaultChain","Profile":"chain-profile"}`,
		},
		{
			name:     "explicit Oidc passes through unchanged",
			config:   Config{Auth: json.RawMessage(`{"Type":"Oidc","RoleArn":"arn:aws:iam::123456789012:role/formae-agent"}`)},
			wantType: AuthTypeOidc,
			wantAuth: `{"Type":"Oidc","RoleArn":"arn:aws:iam::123456789012:role/formae-agent"}`,
		},
		{
			name: "explicit Auth and flat Profile both set is rejected",
			config: Config{
				Profile: "legacy-profile",
				Auth:    json.RawMessage(`{"Type":"DefaultChain","Profile":"chain-profile"}`),
			},
			wantErr: "mutually exclusive",
		},
		{
			name:     "null Auth literal is treated as absent",
			config:   Config{Profile: "legacy-profile", Auth: json.RawMessage(`null`)},
			wantType: AuthTypeDefaultChain,
			wantAuth: `{"Type":"DefaultChain","Profile":"legacy-profile"}`,
		},
		{
			name:     "whitespace-only Auth is treated as absent",
			config:   Config{Profile: "legacy-profile", Auth: json.RawMessage("   \n\t")},
			wantType: AuthTypeDefaultChain,
			wantAuth: `{"Type":"DefaultChain","Profile":"legacy-profile"}`,
		},
		{
			name:    "malformed Auth JSON errors",
			config:  Config{Auth: json.RawMessage(`{not valid json`)},
			wantErr: "malformed Auth block",
		},
		{
			name:    "Auth object missing its Type discriminator errors",
			config:  Config{Auth: json.RawMessage(`{"RoleArn":"arn:aws:iam::123456789012:role/formae-agent"}`)},
			wantErr: "missing its Type discriminator",
		},
		{
			name:    "empty Auth object errors",
			config:  Config{Auth: json.RawMessage(`{}`)},
			wantErr: "missing its Type discriminator",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotType, gotAuth, err := tt.config.effectiveAuth()

			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.wantType, gotType)
			assert.JSONEq(t, tt.wantAuth, string(gotAuth))
		})
	}
}

func TestToAwsConfig_UnknownAuthType(t *testing.T) {
	c := &Config{Region: "us-east-1", Auth: json.RawMessage(`{"Type":"Bogus"}`)}

	_, err := c.ToAwsConfig(context.Background())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown Auth type")
}

func TestToAwsConfig_OidcMissingRoleArn(t *testing.T) {
	for _, auth := range []string{
		`{"Type":"Oidc"}`,
		`{"Type":"Oidc","RoleArn":""}`,
	} {
		c := &Config{Region: "us-east-1", Auth: json.RawMessage(auth)}

		_, err := c.ToAwsConfig(context.Background())

		require.Error(t, err)
		assert.Contains(t, err.Error(), "requires RoleArn")
	}
}

func TestToAwsConfig_OidcWithNoDepsFailsClosed(t *testing.T) {
	c := &Config{
		Region: "us-east-1",
		Auth:   json.RawMessage(`{"Type":"Oidc","RoleArn":"arn:aws:iam::123456789012:role/formae-agent"}`),
	}

	_, err := c.ToAwsConfig(context.Background())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "OIDC token source")
}

func TestToAwsConfig_OidcWithDepsButNilSourceFailsClosed(t *testing.T) {
	c := (&Config{
		Region: "us-east-1",
		Auth:   json.RawMessage(`{"Type":"Oidc","RoleArn":"arn:aws:iam::123456789012:role/formae-agent"}`),
	}).WithOidcDeps(&OidcDeps{})

	_, err := c.ToAwsConfig(context.Background())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "OIDC token source")
}

func TestAwsConfigOptions_OidcAppliesACredentialsCache(t *testing.T) {
	c := (&Config{
		Region: "us-east-1",
		Auth:   json.RawMessage(`{"Type":"Oidc","RoleArn":"arn:aws:iam::123456789012:role/formae-agent"}`),
	}).WithOidcDeps(NewOidcDeps(stubTokenSource{}))

	optFns, err := c.awsConfigOptions()
	require.NoError(t, err)

	var lo awsconfig.LoadOptions
	for _, fn := range optFns {
		require.NoError(t, fn(&lo))
	}

	assert.Equal(t, "us-east-1", lo.Region)
	assert.IsType(t, &aws.CredentialsCache{}, lo.Credentials)
}

func TestAwsConfigOptions_FlatProfileAppliesSharedConfigProfile(t *testing.T) {
	// Carries its own OidcDeps purely so the deprecation warning this path
	// triggers lands on a per-test sync.Once rather than the process-wide
	// warnFlatFallback, which TestDeprecationWarning_FallsBackToProcessOnceWhenDepsAreNil
	// owns exclusively for the life of the test binary.
	c := (&Config{Region: "us-east-1", Profile: "legacy-profile"}).WithOidcDeps(NewOidcDeps(nil))

	optFns, err := c.awsConfigOptions()
	require.NoError(t, err)

	var lo awsconfig.LoadOptions
	for _, fn := range optFns {
		require.NoError(t, fn(&lo))
	}

	assert.Equal(t, "us-east-1", lo.Region)
	assert.Equal(t, "legacy-profile", lo.SharedConfigProfile)
}

func TestAwsConfigOptions_NoProfileLeavesSharedConfigProfileUnset(t *testing.T) {
	c := &Config{Region: "us-east-1"}

	optFns, err := c.awsConfigOptions()
	require.NoError(t, err)

	var lo awsconfig.LoadOptions
	for _, fn := range optFns {
		require.NoError(t, fn(&lo))
	}

	assert.Equal(t, "us-east-1", lo.Region)
	assert.Empty(t, lo.SharedConfigProfile)
}

func TestAwsConfigOptions_ExplicitDefaultChainAppliesItsProfile(t *testing.T) {
	c := &Config{
		Region: "us-east-1",
		Auth:   json.RawMessage(`{"Type":"DefaultChain","Profile":"chain-profile"}`),
	}

	optFns, err := c.awsConfigOptions()
	require.NoError(t, err)

	var lo awsconfig.LoadOptions
	for _, fn := range optFns {
		require.NoError(t, fn(&lo))
	}

	assert.Equal(t, "chain-profile", lo.SharedConfigProfile)
}

// withCountingSlogDefault swaps the package-level slog default for the
// duration of the test, restoring it on cleanup, and returns a pointer to
// the record count the swapped-in handler increments.
func withCountingSlogDefault(t *testing.T) *int {
	t.Helper()

	count := 0
	prev := slog.Default()
	slog.SetDefault(slog.New(countingHandler{count: &count}))
	t.Cleanup(func() { slog.SetDefault(prev) })

	return &count
}

func TestDeprecationWarning_FiresOncePerOidcDeps(t *testing.T) {
	count := withCountingSlogDefault(t)

	deps := NewOidcDeps(nil)
	c1 := (&Config{Region: "us-east-1", Profile: "legacy-profile"}).WithOidcDeps(deps)
	c2 := (&Config{Region: "us-east-1", Profile: "legacy-profile"}).WithOidcDeps(deps)

	_, err := c1.awsConfigOptions()
	require.NoError(t, err)
	_, err = c2.awsConfigOptions()
	require.NoError(t, err)

	assert.Equal(t, 1, *count)
}

func TestDeprecationWarning_DoesNotFireForExplicitDefaultChainProfile(t *testing.T) {
	count := withCountingSlogDefault(t)

	deps := NewOidcDeps(nil)
	c := (&Config{
		Region: "us-east-1",
		Auth:   json.RawMessage(`{"Type":"DefaultChain","Profile":"chain-profile"}`),
	}).WithOidcDeps(deps)

	_, err := c.awsConfigOptions()
	require.NoError(t, err)

	assert.Equal(t, 0, *count)
}

func TestDeprecationWarning_DoesNotFireWhenNoProfileIsSet(t *testing.T) {
	count := withCountingSlogDefault(t)

	deps := NewOidcDeps(nil)
	c := (&Config{Region: "us-east-1"}).WithOidcDeps(deps)

	_, err := c.awsConfigOptions()
	require.NoError(t, err)

	assert.Equal(t, 0, *count)
}

func TestDeprecationWarning_FallsBackToProcessOnceWhenDepsAreNil(t *testing.T) {
	count := withCountingSlogDefault(t)

	c1 := &Config{Region: "us-east-1", Profile: "legacy-profile"}
	c2 := &Config{Region: "us-east-1", Profile: "legacy-profile"}

	_, err := c1.awsConfigOptions()
	require.NoError(t, err)
	_, err = c2.awsConfigOptions()
	require.NoError(t, err)

	assert.Equal(t, 1, *count)
}
