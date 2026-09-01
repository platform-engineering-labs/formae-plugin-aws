// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package config

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewAuthPolicy_RejectsUnknownConfiguredMethod(t *testing.T) {
	_, err := NewAuthPolicy([]string{"StaticCredentials"})

	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown auth method "StaticCredentials"`)
}

func TestEffectiveAuth_EnforcesAuthPolicy(t *testing.T) {
	oidcOnly, err := NewAuthPolicy([]string{AuthTypeOidc})
	require.NoError(t, err)

	tests := []struct {
		name    string
		config  Config
		wantErr string
	}{
		{
			name: "empty policy leaves default chain unrestricted",
			config: Config{
				authPolicy: AuthPolicy{},
			},
		},
		{
			name: "OIDC-only policy accepts OIDC",
			config: Config{
				Auth:       json.RawMessage(`{"Type":"Oidc","RoleArn":"arn:aws:iam::123456789012:role/formae-agent"}`),
				authPolicy: oidcOnly,
			},
		},
		{
			name: "OIDC-only policy rejects explicit default chain",
			config: Config{
				Auth:       json.RawMessage(`{"Type":"DefaultChain"}`),
				authPolicy: oidcOnly,
			},
			wantErr: `auth method "DefaultChain" is not allowed; allowed methods: "Oidc"`,
		},
		{
			name: "OIDC-only policy rejects omitted auth",
			config: Config{
				authPolicy: oidcOnly,
			},
			wantErr: `auth method "DefaultChain" is not allowed; allowed methods: "Oidc"`,
		},
		{
			name: "OIDC-only policy rejects deprecated flat profile",
			config: Config{
				Profile:    "legacy-profile",
				authPolicy: oidcOnly,
			},
			wantErr: `auth method "DefaultChain" is not allowed; allowed methods: "Oidc"`,
		},
		{
			name: "restrictive policy rejects unknown target discriminator",
			config: Config{
				Auth:       json.RawMessage(`{"Type":"FutureAuth"}`),
				authPolicy: oidcOnly,
			},
			wantErr: `auth method "FutureAuth" is not allowed; allowed methods: "Oidc"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := tt.config.effectiveAuth()

			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.EqualError(t, err, "config: "+tt.wantErr)
		})
	}
}
