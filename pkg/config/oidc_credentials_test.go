// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	ststypes "github.com/aws/aws-sdk-go-v2/service/sts/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testRoleArn = "arn:aws:iam::123456789012:role/formae-agent"

// tokenSourceFunc adapts a function to plugin.OidcTokenSource so a test can
// script what the broker answers per call.
type tokenSourceFunc func(ctx context.Context, audience string) (string, error)

func (f tokenSourceFunc) IdentityToken(ctx context.Context, audience string) (string, error) {
	return f(ctx, audience)
}

// fakeSTS stands in for the AssumeRoleWithWebIdentity API client, recording
// what each exchange was called with and answering with canned credentials.
type fakeSTS struct {
	mu sync.Mutex

	calls       int
	token       string
	roleArn     string
	ctxErr      error
	hadDeadline bool
	remaining   time.Duration

	// fail, when set, turns the exchange into the error it returns. It
	// receives the submitted token so a test can make the failure echo it.
	fail func(token string) error
}

func (f *fakeSTS) AssumeRoleWithWebIdentity(
	ctx context.Context,
	in *sts.AssumeRoleWithWebIdentityInput,
	_ ...func(*sts.Options),
) (*sts.AssumeRoleWithWebIdentityOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.calls++
	f.token = aws.ToString(in.WebIdentityToken)
	f.roleArn = aws.ToString(in.RoleArn)
	f.ctxErr = ctx.Err()
	if deadline, ok := ctx.Deadline(); ok {
		f.hadDeadline = true
		f.remaining = time.Until(deadline)
	}

	if f.fail != nil {
		if err := f.fail(f.token); err != nil {
			return nil, err
		}
	}

	expires := time.Now().Add(time.Hour)

	return &sts.AssumeRoleWithWebIdentityOutput{
		Credentials: &ststypes.Credentials{
			AccessKeyId:     aws.String("AKIAEXAMPLE"),
			SecretAccessKey: aws.String("example-secret"),
			SessionToken:    aws.String("example-session"),
			Expiration:      &expires,
		},
		AssumedRoleUser: &ststypes.AssumedRoleUser{
			Arn: aws.String("arn:aws:sts::123456789012:assumed-role/formae-agent/session"),
		},
	}, nil
}

func (f *fakeSTS) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.calls
}

// isolateAwsEnv points the SDK's shared-config discovery at empty files in a
// temp dir, so a test that runs LoadDefaultConfig does not pick up whatever
// AWS configuration the developer's machine happens to carry.
func isolateAwsEnv(t *testing.T) {
	t.Helper()

	dir := t.TempDir()
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(dir, "config"))
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(dir, "credentials"))
	t.Setenv("AWS_PROFILE", "")
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
}

func TestRetrieve_ExchangesTheSourceTokenForCredentials(t *testing.T) {
	var gotAudience string
	source := tokenSourceFunc(func(_ context.Context, audience string) (string, error) {
		gotAudience = audience
		return "stub-token", nil
	})
	fake := &fakeSTS{}
	provider := &oidcCredentialsProvider{source: source, roleArn: testRoleArn, stsClient: fake}

	creds, err := provider.Retrieve(context.Background())
	require.NoError(t, err)

	assert.Equal(t, "sts.amazonaws.com", gotAudience)
	assert.Equal(t, 1, fake.callCount())
	assert.Equal(t, "stub-token", fake.token)
	assert.Equal(t, testRoleArn, fake.roleArn)
	assert.Equal(t, "AKIAEXAMPLE", creds.AccessKeyID)
	assert.Equal(t, "example-secret", creds.SecretAccessKey)
	assert.Equal(t, "example-session", creds.SessionToken)
	assert.True(t, creds.CanExpire)
	assert.WithinDuration(t, time.Now().Add(time.Hour), creds.Expires, time.Minute)
}

func TestRetrieve_SourceErrorFailsClosed(t *testing.T) {
	source := tokenSourceFunc(func(context.Context, string) (string, error) {
		return "", errors.New("broker unavailable")
	})
	fake := &fakeSTS{}
	provider := &oidcCredentialsProvider{source: source, roleArn: testRoleArn, stsClient: fake}

	creds, err := provider.Retrieve(context.Background())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "broker unavailable")
	assert.Equal(t, 0, fake.callCount())
	assert.Equal(t, aws.Credentials{}, creds)
}

type routingKey struct{}

func TestRetrieve_DerivesABoundedRefreshCtx(t *testing.T) {
	// The incoming ctx is cancelled before Retrieve is even called: the
	// refresh must still complete, and must still see the ctx's values.
	incoming, cancel := context.WithCancel(context.WithValue(context.Background(), routingKey{}, "routed"))
	cancel()

	var sourceErr error
	var sourceValue any
	source := tokenSourceFunc(func(ctx context.Context, _ string) (string, error) {
		sourceErr = ctx.Err()
		sourceValue = ctx.Value(routingKey{})
		return "stub-token", nil
	})
	fake := &fakeSTS{}
	provider := &oidcCredentialsProvider{source: source, roleArn: testRoleArn, stsClient: fake}

	creds, err := provider.Retrieve(incoming)
	require.NoError(t, err)
	assert.Equal(t, "AKIAEXAMPLE", creds.AccessKeyID)

	assert.NoError(t, sourceErr)
	assert.Equal(t, "routed", sourceValue)

	assert.NoError(t, fake.ctxErr)
	assert.True(t, fake.hadDeadline, "the refresh ctx must carry a deadline")
	assert.Greater(t, fake.remaining, 25*time.Second)
	assert.LessOrEqual(t, fake.remaining, oidcRefreshTimeout)
}

func TestRetrieve_NeverLeaksTheTokenInErrors(t *testing.T) {
	const secret = "header.super-secret-jwt-payload.signature"

	t.Run("an STS failure that echoes the submitted token", func(t *testing.T) {
		source := tokenSourceFunc(func(context.Context, string) (string, error) {
			return secret, nil
		})
		fake := &fakeSTS{fail: func(token string) error {
			return fmt.Errorf("InvalidIdentityToken: the token %s was rejected", token)
		}}
		provider := &oidcCredentialsProvider{source: source, roleArn: testRoleArn, stsClient: fake}

		_, err := provider.Retrieve(context.Background())

		require.Error(t, err)
		assert.NotContains(t, err.Error(), secret)
		assert.Contains(t, err.Error(), "redacted")
	})

	t.Run("a source failure on a later refresh", func(t *testing.T) {
		var calls int
		source := tokenSourceFunc(func(context.Context, string) (string, error) {
			calls++
			if calls == 1 {
				return secret, nil
			}
			return "", errors.New("broker rotated its signing key")
		})
		provider := &oidcCredentialsProvider{source: source, roleArn: testRoleArn, stsClient: &fakeSTS{}}

		_, err := provider.Retrieve(context.Background())
		require.NoError(t, err)

		_, err = provider.Retrieve(context.Background())

		require.Error(t, err)
		assert.NotContains(t, err.Error(), secret)
	})
}

func TestCredentialsCacheFor_GetOrCreateIsSynchronized(t *testing.T) {
	deps := NewOidcDeps(stubTokenSource{})

	var builds int64
	build := func() *aws.CredentialsCache {
		atomic.AddInt64(&builds, 1)
		return aws.NewCredentialsCache(&oidcCredentialsProvider{
			source:    stubTokenSource{},
			roleArn:   testRoleArn,
			stsClient: &fakeSTS{},
		})
	}

	const goroutines = 8
	got := make([]*aws.CredentialsCache, goroutines)
	start := make(chan struct{})

	var wg sync.WaitGroup
	for i := range got {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			got[i] = deps.credentialsCacheFor("shared-key", build)
		}(i)
	}
	close(start)
	wg.Wait()

	assert.EqualValues(t, 1, atomic.LoadInt64(&builds), "a concurrent miss must construct exactly once")
	for _, cache := range got {
		assert.Same(t, got[0], cache)
	}
}

func TestCredentialsCacheFor_KeyIncludesAuthHash(t *testing.T) {
	deps := NewOidcDeps(stubTokenSource{})
	build := func() *aws.CredentialsCache {
		return aws.NewCredentialsCache(&oidcCredentialsProvider{
			source:    stubTokenSource{},
			roleArn:   testRoleArn,
			stsClient: &fakeSTS{},
		})
	}

	first := oidcCacheKey(testRoleArn, "us-east-1", json.RawMessage(`{"Type":"Oidc","RoleArn":"`+testRoleArn+`","SessionName":"a"}`))
	second := oidcCacheKey(testRoleArn, "us-east-1", json.RawMessage(`{"Type":"Oidc","RoleArn":"`+testRoleArn+`","SessionName":"b"}`))
	require.NotEqual(t, first, second)

	assert.Same(t, deps.credentialsCacheFor(first, build), deps.credentialsCacheFor(first, build))
	assert.NotSame(t, deps.credentialsCacheFor(first, build), deps.credentialsCacheFor(second, build))
}

func TestCredentialsCacheFor_KeySeparatesRoleAndRegion(t *testing.T) {
	raw := json.RawMessage(`{"Type":"Oidc","RoleArn":"` + testRoleArn + `"}`)

	assert.NotEqual(t,
		oidcCacheKey(testRoleArn, "us-east-1", raw),
		oidcCacheKey(testRoleArn, "eu-west-1", raw),
	)
	// The NUL separator keeps a role/region boundary shift from colliding.
	assert.NotEqual(t,
		oidcCacheKey("role", "a-b", raw),
		oidcCacheKey("role-a", "b", raw),
	)
}

func TestCacheReuse_OneExchangePerLifetime(t *testing.T) {
	isolateAwsEnv(t)

	fake := &fakeSTS{}
	deps := NewOidcDeps(stubTokenSource{})
	deps.stsFactory = func(aws.Config) stscreds.AssumeRoleWithWebIdentityAPIClient { return fake }

	auth := json.RawMessage(`{"Type":"Oidc","RoleArn":"` + testRoleArn + `"}`)

	for range 2 {
		// A fresh Config each time, as every plugin operation builds one;
		// only the plugin-lifetime deps are shared.
		c := (&Config{Region: "us-east-1", Auth: auth}).WithOidcDeps(deps)

		cfg, err := c.ToAwsConfig(context.Background())
		require.NoError(t, err)

		creds, err := cfg.Credentials.Retrieve(context.Background())
		require.NoError(t, err)
		assert.Equal(t, "AKIAEXAMPLE", creds.AccessKeyID)
	}

	assert.Equal(t, 1, fake.callCount(), "the cached credentials must survive across ToAwsConfig calls")
}
