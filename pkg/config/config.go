// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	"github.com/platform-engineering-labs/formae/pkg/plugin"
)

// Auth discriminator values, matching the Type field the Pkl schema renders
// on the nested Auth object.
const (
	AuthTypeDefaultChain = "DefaultChain"
	AuthTypeOidc         = "Oidc"
)

// deprecatedFlatProfileWarning is logged, once, whenever a target's flat
// Profile field is what produces a DefaultChain auth (as opposed to an
// explicit `auth = DefaultChainAuth { ... }` block). It is the only
// deprecation surface available for this: the plugin does not implement
// ObservablePlugin, so plugin.LoggerFromContext returns an indistinguishable
// no-op, and Pkl's @Deprecated annotation does not print at eval.
const deprecatedFlatProfileWarning = "target config uses the deprecated flat profile; set auth = new DefaultChainAuth"

// warnFlatFallback fires the deprecation warning at most once for Config
// values that carry no OidcDeps (e.g. a plugin instance the agent hasn't
// wired a token source into), so flat-profile users still see it once per
// process rather than not at all.
var warnFlatFallback sync.Once

// OidcDeps is owned by the Plugin instance, never process-global: a global
// would outlive individual plugin instances and make tests order-dependent.
// A Config with nil deps behaves flat-only: Oidc auth fails closed, and the
// deprecation warning falls back to warnFlatFallback.
//
// Build one with NewOidcDeps, never a bare &OidcDeps{...} literal outside
// this package: NewOidcDeps wires the production STS factory. A literal
// leaves stsFactory nil and oidcCredentials falls back to the same factory,
// so it still works, but the seam is then invisible at the construction site.
type OidcDeps struct {
	// Source mints the OIDC identity tokens exchanged for AWS credentials.
	Source plugin.OidcTokenSource

	// caches holds one *aws.CredentialsCache per distinct Oidc auth block,
	// built lazily by credentialsCacheFor.
	caches sync.Map

	// stsFactory builds the STS client used for AssumeRoleWithWebIdentity.
	// A seam for tests; production wiring is sts.NewFromConfig.
	stsFactory func(aws.Config) stscreds.AssumeRoleWithWebIdentityAPIClient

	// warnFlat ensures the flat-profile deprecation warning logs at most
	// once per plugin instance.
	warnFlat sync.Once
}

// defaultSTSFactory is the production STS client factory: the client needs no
// credentials of its own, because AssumeRoleWithWebIdentity is unsigned.
func defaultSTSFactory(cfg aws.Config) stscreds.AssumeRoleWithWebIdentityAPIClient {
	return sts.NewFromConfig(cfg)
}

// NewOidcDeps builds the OidcDeps a Plugin instance owns, wired to mint AWS
// credentials against real STS.
func NewOidcDeps(src plugin.OidcTokenSource) *OidcDeps {
	return &OidcDeps{
		Source:     src,
		stsFactory: defaultSTSFactory,
	}
}

type Config struct {
	Region  string          `json:"Region"`
	Profile string          `json:"Profile"`
	Auth    json.RawMessage `json:"Auth,omitempty"`

	// deps carries what the plugin instance owns: the token source, the
	// per-plugin credentials-cache registry, the STS client factory seam,
	// and the warn-once state. Never serialized; nil deps means flat-only
	// behavior.
	deps *OidcDeps

	// authPolicy is the plugin instance's immutable authentication-method
	// allowlist. Its zero value is unrestricted.
	authPolicy AuthPolicy
}

// WithOidcDeps threads the plugin instance's OidcDeps onto Config without
// changing FromTargetConfig's signature or call sites.
func (c *Config) WithOidcDeps(d *OidcDeps) *Config {
	c.deps = d
	return c
}

// WithAuthPolicy threads the plugin instance's authentication policy onto a
// parsed target configuration.
func (c *Config) WithAuthPolicy(policy AuthPolicy) *Config {
	c.authPolicy = policy
	return c
}

// authDiscriminator is the shape every Auth block variant shares.
type authDiscriminator struct {
	Type string `json:"Type"`
}

// isAuthAbsent reports whether raw carries no explicit Auth block: this is
// true for a nil/empty field, the JSON literal null, and whitespace-only
// content.
func isAuthAbsent(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	return trimmed == "" || trimmed == "null"
}

// synthesizeDefaultChain builds the Auth block an absent Auth field implies:
// a DefaultChain carrying the flat Profile spelling.
func synthesizeDefaultChain(profile string) json.RawMessage {
	raw, _ := json.Marshal(struct {
		Type    string `json:"Type"`
		Profile string `json:"Profile"`
	}{Type: AuthTypeDefaultChain, Profile: profile})
	return raw
}

// effectiveAuth resolves the auth block that governs this Config: the
// explicit Auth block if the forma set one, or a synthesised DefaultChain
// carrying the flat Profile spelling otherwise. Setting both is rejected,
// mirroring the Pkl-level `this == null || profile == null` constraint so
// callers that bypass the schema (tests, an older formae binary) still get
// the rule enforced in Go.
func (c *Config) effectiveAuth() (string, json.RawMessage, error) {
	var authType string
	var rawAuth json.RawMessage

	if isAuthAbsent(c.Auth) {
		authType = AuthTypeDefaultChain
		rawAuth = synthesizeDefaultChain(c.Profile)
	} else {
		if c.Profile != "" {
			return "", nil, errors.New("config: Auth and Profile are mutually exclusive; set one")
		}

		var disc authDiscriminator
		if err := json.Unmarshal(c.Auth, &disc); err != nil {
			return "", nil, fmt.Errorf("config: malformed Auth block: %w", err)
		}
		if disc.Type == "" {
			return "", nil, errors.New("config: Auth block is missing its Type discriminator")
		}

		authType = disc.Type
		rawAuth = c.Auth
	}

	if err := c.authPolicy.Validate(authType); err != nil {
		return "", nil, err
	}

	return authType, rawAuth, nil
}

// awsConfigOptions resolves this Config's effective auth into the
// awsconfig.LoadOptions functions ToAwsConfig hands to LoadDefaultConfig.
// Split out from ToAwsConfig so tests can inspect the resolved options (e.g.
// the profile threaded through) without exercising real credential/config-
// file resolution.
func (c *Config) awsConfigOptions() ([]func(*awsconfig.LoadOptions) error, error) {
	synthesized := isAuthAbsent(c.Auth)

	authType, rawAuth, err := c.effectiveAuth()
	if err != nil {
		return nil, err
	}

	opts := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(c.Region)}

	switch authType {
	case AuthTypeDefaultChain:
		var chain struct {
			Profile string `json:"Profile"`
		}
		if err := json.Unmarshal(rawAuth, &chain); err != nil {
			return nil, fmt.Errorf("config: malformed DefaultChain auth block: %w", err)
		}
		if chain.Profile != "" {
			opts = append(opts, awsconfig.WithSharedConfigProfile(chain.Profile))
		}
		if synthesized && chain.Profile != "" {
			c.warnDeprecatedFlatProfile()
		}

	case AuthTypeOidc:
		var oidc struct {
			RoleArn string `json:"RoleArn"`
		}
		if err := json.Unmarshal(rawAuth, &oidc); err != nil {
			return nil, fmt.Errorf("config: malformed Oidc auth block: %w", err)
		}
		if oidc.RoleArn == "" {
			return nil, errors.New("config: Oidc auth requires RoleArn")
		}
		if c.deps == nil || c.deps.Source == nil {
			return nil, errors.New("config: Oidc auth requires an OIDC token source, but this plugin instance has none wired (failing closed rather than falling back to ambient credentials)")
		}

		opts = append(opts, awsconfig.WithCredentialsProvider(
			c.deps.oidcCredentials(c.Region, oidc.RoleArn, rawAuth),
		))

	default:
		return nil, fmt.Errorf("config: unknown Auth type %q", authType)
	}

	return opts, nil
}

// warnDeprecatedFlatProfile logs deprecatedFlatProfileWarning once per
// plugin instance (via OidcDeps.warnFlat), or once per process when this
// Config carries no OidcDeps at all.
func (c *Config) warnDeprecatedFlatProfile() {
	if c.deps != nil {
		c.deps.warnFlat.Do(func() { slog.Warn(deprecatedFlatProfileWarning) })
		return
	}
	warnFlatFallback.Do(func() { slog.Warn(deprecatedFlatProfileWarning) })
}

func (c *Config) ToAwsConfig(ctx context.Context) (aws.Config, error) {
	opts, err := c.awsConfigOptions()
	if err != nil {
		return aws.Config{}, err
	}

	return awsconfig.LoadDefaultConfig(ctx, opts...)
}

// FromTargetConfig parses the target configuration JSON into a Config struct
func FromTargetConfig(targetConfig json.RawMessage) *Config {
	if targetConfig == nil {
		return &Config{}
	}
	config := &Config{}
	_ = json.Unmarshal(targetConfig, config)

	return config
}
