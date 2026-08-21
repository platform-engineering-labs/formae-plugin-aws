// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package config

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"

	"github.com/platform-engineering-labs/formae/pkg/plugin"
)

// oidcAudience is the audience every identity token destined for AWS is
// minted for; the role's trust policy matches on it.
const oidcAudience = "sts.amazonaws.com"

// oidcRefreshTimeout bounds one credential refresh end to end: minting an
// identity token plus exchanging it at STS. The stock aws.CredentialsCache
// hands the refresh a context whose cancellation is already suppressed, so
// without a bound of our own a wedged broker would hold the refresh open
// indefinitely.
const oidcRefreshTimeout = 30 * time.Second

// oidcRoleSessionName names every web-identity session this plugin opens.
// Without it the SDK generates a random name, so CloudTrail shows an
// unattributable assumed-role principal; a fixed name makes the caller
// legible in the customer's audit trail.
const oidcRoleSessionName = "formae-aws-plugin"

// oneShotRetriever hands the stock, context-free IdentityTokenRetriever a
// token that was already minted for exactly one Retrieve call. Tokens are
// short-lived and single-use by design, so a retriever is never reused.
type oneShotRetriever struct{ token []byte }

func (r oneShotRetriever) GetIdentityToken() ([]byte, error) { return r.token, nil }

// oidcCredentialsProvider mints a fresh identity token per refresh and
// exchanges it for AWS credentials by assuming roleArn. It holds no
// credential state: aws.CredentialsCache owns caching and refresh timing.
type oidcCredentialsProvider struct {
	source    plugin.OidcTokenSource
	roleArn   string
	stsClient stscreds.AssumeRoleWithWebIdentityAPIClient
}

// Retrieve reads nothing mutable from ctx. aws.CredentialsCache already
// suppresses caller cancellation on the refresh path (values survive,
// cancellation does not), so honouring the incoming deadline here would be an
// illusion; the refresh instead runs under its own bounded context derived
// with context.WithoutCancel, which keeps the request-scoped values the token
// source needs to reach the right broker.
func (p *oidcCredentialsProvider) Retrieve(ctx context.Context) (aws.Credentials, error) {
	refreshCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), oidcRefreshTimeout)
	defer cancel()

	token, err := p.source.IdentityToken(refreshCtx, oidcAudience)
	if err != nil {
		return aws.Credentials{}, fmt.Errorf("config: minting an identity token for role %q: %w", p.roleArn, err)
	}

	exchange := stscreds.NewWebIdentityRoleProvider(
		p.stsClient, p.roleArn, oneShotRetriever{token: []byte(token)},
		func(o *stscreds.WebIdentityRoleOptions) { o.RoleSessionName = oidcRoleSessionName },
	)

	creds, err := exchange.Retrieve(refreshCtx)
	if err != nil {
		return aws.Credentials{}, fmt.Errorf(
			"config: exchanging the identity token for credentials on role %q: %w",
			p.roleArn, redactToken(err, token),
		)
	}

	return creds, nil
}

// redactToken strips any verbatim occurrence of the identity token from an
// error's message. STS echoes the submitted token in some rejection bodies,
// and the token is bearer credential material that must never reach a log or
// an error shown to a user. Redacting flattens the error chain, so it only
// happens when a leak is actually present.
func redactToken(err error, token string) error {
	if token == "" {
		return err
	}

	msg := err.Error()
	if !strings.Contains(msg, token) {
		return err
	}

	return errors.New(strings.ReplaceAll(msg, token, "[redacted identity token]"))
}

// oidcCacheKey identifies one credentials cache. Two targets share a cache
// only when they assume the same role in the same region under a
// byte-identical auth block, so any auth change (a new session name, a new
// policy) yields a distinct cache rather than silently reusing credentials
// minted under the old settings. The NUL separators keep a boundary shift
// between the role and the region from colliding.
//
// The key deliberately omits the token source's identity: the SDK installs
// exactly one OidcTokenSource per plugin process (via OidcAware at startup),
// so within one OidcDeps every cache entry is already scoped to that single
// broker pairing and there is nothing for the key to distinguish.
func oidcCacheKey(roleArn, region string, rawAuth json.RawMessage) string {
	sum := sha256.Sum256(rawAuth)

	return roleArn + "\x00" + region + "\x00" + hex.EncodeToString(sum[:])
}

// credentialsCacheHolder carries a lazily built cache plus the once that
// guarantees a concurrent miss constructs it exactly one time.
type credentialsCacheHolder struct {
	once  sync.Once
	cache *aws.CredentialsCache
}

// credentialsCacheFor returns the cache registered under key, building it on
// first use. There is deliberately no eviction: the map grows with the number
// of distinct auth configurations a plugin instance sees, which is bounded by
// the targets in play, and evicting an entry could not invalidate the
// references already handed out, so it would reintroduce exactly the
// duplicate token exchanges the cache exists to prevent.
func (d *OidcDeps) credentialsCacheFor(key string, build func() *aws.CredentialsCache) *aws.CredentialsCache {
	entry, _ := d.caches.LoadOrStore(key, &credentialsCacheHolder{})
	holder := entry.(*credentialsCacheHolder)
	holder.once.Do(func() { holder.cache = build() })

	return holder.cache
}

// oidcCredentials returns the plugin-lifetime credentials cache that mints
// AWS credentials for roleArn by exchanging brokered identity tokens. The STS
// client is built from a region-only base config: it needs no credentials of
// its own, because AssumeRoleWithWebIdentity is an unsigned call.
func (d *OidcDeps) oidcCredentials(region, roleArn string, rawAuth json.RawMessage) *aws.CredentialsCache {
	// Read the factory into a local rather than filling the field in: an
	// OidcDeps built as a bare literal leaves it nil, and defaulting here
	// keeps that recoverable without a write that would race a concurrent
	// operation reading the same struct.
	factory := d.stsFactory
	if factory == nil {
		factory = defaultSTSFactory
	}

	return d.credentialsCacheFor(oidcCacheKey(roleArn, region, rawAuth), func() *aws.CredentialsCache {
		return aws.NewCredentialsCache(&oidcCredentialsProvider{
			source:    d.Source,
			roleArn:   roleArn,
			stsClient: factory(aws.Config{Region: region}),
		})
	})
}
