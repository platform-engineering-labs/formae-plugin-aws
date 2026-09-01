// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package main

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/platform-engineering-labs/formae/pkg/plugin"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
)

// Compile-time check: Plugin must satisfy OidcAware, so the SDK can hand it
// an OidcTokenSource at startup.
var _ plugin.OidcAware = (*Plugin)(nil)

// Compile-time check: Plugin must accept the plugin-specific configuration
// rendered from schema/Config.pkl.
var _ plugin.Configurable = (*Plugin)(nil)

// oidcTokenSourceFunc adapts a function to plugin.OidcTokenSource so a test
// can script what the source answers, and observe the ctx it was called
// with.
type oidcTokenSourceFunc func(ctx context.Context, audience string) (string, error)

func (f oidcTokenSourceFunc) IdentityToken(ctx context.Context, audience string) (string, error) {
	return f(ctx, audience)
}

func TestSetOidcTokenSource_PopulatesDeps(t *testing.T) {
	p := &Plugin{}
	assert.Nil(t, p.oidc)

	src := oidcTokenSourceFunc(func(context.Context, string) (string, error) {
		return "stub-token", nil
	})
	p.SetOidcTokenSource(src)

	require.NotNil(t, p.oidc)
	assert.NotNil(t, p.oidc.Source)
}

func TestPluginConfigure(t *testing.T) {
	t.Run("missing config leaves authentication unrestricted", func(t *testing.T) {
		p := &Plugin{}
		require.NoError(t, p.Configure(nil))
	})

	t.Run("empty allowlist leaves authentication unrestricted", func(t *testing.T) {
		p := &Plugin{}
		require.NoError(t, p.Configure(json.RawMessage(`{"allowedAuthMethods":[]}`)))
	})

	t.Run("malformed config is rejected", func(t *testing.T) {
		p := &Plugin{}
		err := p.Configure(json.RawMessage(`{not-json`))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "decode AWS plugin configuration")
	})

	t.Run("unknown auth method is rejected", func(t *testing.T) {
		p := &Plugin{}
		err := p.Configure(json.RawMessage(`{"allowedAuthMethods":["StaticCredentials"]}`))
		require.Error(t, err)
		assert.Contains(t, err.Error(), `unknown auth method "StaticCredentials"`)
	})
}

func TestPluginConfigure_OidcOnlyRejectsAmbientCredentialsBeforeLoading(t *testing.T) {
	p := &Plugin{}
	require.NoError(t, p.Configure(json.RawMessage(`{"allowedAuthMethods":["Oidc"]}`)))

	request := &resource.ReadRequest{
		ResourceType: "AWS::Formae::NoSuchProvisioneredType",
		NativeID:     "irrelevant-for-this-test",
		TargetConfig: json.RawMessage(`{"Region":"us-east-1"}`),
	}

	_, err := p.Read(context.Background(), request)

	require.Error(t, err)
	assert.Contains(t, err.Error(), `config: auth method "DefaultChain" is not allowed; allowed methods: "Oidc"`)
}

// operationCtxMarkerKey is the context.WithValue marker used to prove a ctx
// travelled, unmodified in its values, from the operation call down into the
// OidcTokenSource.
type operationCtxMarkerKey struct{}

// TestRead_OidcRoutesTheOperationCtxToTheTokenSource drives Plugin.Read (with
// a resource type carrying no registered provisioner, so it falls through to
// the CloudControl client) against a target config with an Oidc auth block.
// The stub token source returns an error, which fails the AWS SDK's
// credential resolution before it signs or sends any request, so the
// assertion never depends on network access or a real STS exchange; it only
// exercises Config->ToAwsConfig->CredentialsCache->Retrieve->IdentityToken
// far enough to observe the ctx that reached the token source.
func TestRead_OidcRoutesTheOperationCtxToTheTokenSource(t *testing.T) {
	var gotCtx context.Context
	src := oidcTokenSourceFunc(func(ctx context.Context, audience string) (string, error) {
		gotCtx = ctx
		assert.Equal(t, "sts.amazonaws.com", audience)
		return "", errors.New("stub source: deliberately unminted, for ctx-routing assertions only")
	})

	p := &Plugin{}
	p.SetOidcTokenSource(src)

	ctx := context.WithValue(context.Background(), operationCtxMarkerKey{}, "operation-ctx")
	request := &resource.ReadRequest{
		ResourceType: "AWS::Formae::NoSuchProvisioneredType",
		NativeID:     "irrelevant-for-this-test",
		TargetConfig: json.RawMessage(`{"Region":"us-east-1","Auth":{"Type":"Oidc","RoleArn":"arn:aws:iam::123456789012:role/formae-agent"}}`),
	}

	_, err := p.Read(ctx, request)

	require.Error(t, err)
	require.NotNil(t, gotCtx)
	assert.Equal(t, "operation-ctx", gotCtx.Value(operationCtxMarkerKey{}))
}

func TestMatchesFilter(t *testing.T) {
	t.Run("matches when all filter properties are present and equal", func(t *testing.T) {
		properties := `{"VpcId":"vpc-123","SubnetId":"subnet-456","CidrBlock":"10.0.0.0/24"}`
		filter := map[string]string{"VpcId": "vpc-123"}
		assert.True(t, matchesFilter(properties, filter))
	})

	t.Run("does not match when filter property value differs", func(t *testing.T) {
		properties := `{"VpcId":"vpc-999","SubnetId":"subnet-456"}`
		filter := map[string]string{"VpcId": "vpc-123"}
		assert.False(t, matchesFilter(properties, filter))
	})

	t.Run("matches with multiple filter properties", func(t *testing.T) {
		properties := `{"ClusterName":"my-cluster","ServiceName":"my-service","TaskSetId":"ts-1"}`
		filter := map[string]string{"ClusterName": "my-cluster", "ServiceName": "my-service"}
		assert.True(t, matchesFilter(properties, filter))
	})

	t.Run("does not match when one of multiple filter properties differs", func(t *testing.T) {
		properties := `{"ClusterName":"my-cluster","ServiceName":"other-service","TaskSetId":"ts-1"}`
		filter := map[string]string{"ClusterName": "my-cluster", "ServiceName": "my-service"}
		assert.False(t, matchesFilter(properties, filter))
	})

	t.Run("includes resource when filter property is missing from response", func(t *testing.T) {
		properties := `{"SubnetId":"subnet-456"}`
		filter := map[string]string{"VpcId": "vpc-123"}
		assert.True(t, matchesFilter(properties, filter))
	})

	t.Run("includes resource when properties JSON is malformed", func(t *testing.T) {
		properties := `not-json`
		filter := map[string]string{"VpcId": "vpc-123"}
		assert.True(t, matchesFilter(properties, filter))
	})

	t.Run("includes resource when property value is not a string", func(t *testing.T) {
		properties := `{"VpcId":{"nested":"object"},"SubnetId":"subnet-456"}`
		filter := map[string]string{"VpcId": "vpc-123"}
		assert.True(t, matchesFilter(properties, filter))
	})

	t.Run("matches with empty filter", func(t *testing.T) {
		properties := `{"VpcId":"vpc-123"}`
		filter := map[string]string{}
		assert.True(t, matchesFilter(properties, filter))
	})

	t.Run("matches when the property echoes the filtered name in ARN form", func(t *testing.T) {
		properties := `{"FunctionName":"arn:aws:lambda:us-east-1:111122223333:function:my-function","Id":"sid-1"}`
		filter := map[string]string{"FunctionName": "my-function"}
		assert.True(t, matchesFilter(properties, filter))
	})

	t.Run("matches when the filter value is an ARN and the property echoes the short name", func(t *testing.T) {
		properties := `{"FunctionName":"my-function","Id":"sid-1"}`
		filter := map[string]string{"FunctionName": "arn:aws:lambda:us-east-1:111122223333:function:my-function"}
		assert.True(t, matchesFilter(properties, filter))
	})

	t.Run("does not match when the ARN-form property names a different resource", func(t *testing.T) {
		properties := `{"FunctionName":"arn:aws:lambda:us-east-1:111122223333:function:other-function","Id":"sid-1"}`
		filter := map[string]string{"FunctionName": "my-function"}
		assert.False(t, matchesFilter(properties, filter))
	})
}

func TestDiscoveryListExclusions(t *testing.T) {
	t.Run("excludes AWS-managed IAM policies and keeps customer policies", func(t *testing.T) {
		excluded := discoveryListExclusions["AWS::IAM::ManagedPolicy"]
		assert.True(t, excluded("arn:aws:iam::aws:policy/AdministratorAccess"))
		assert.True(t, excluded("arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole"))
		assert.False(t, excluded("arn:aws:iam::111122223333:policy/my-policy"))
	})

	t.Run("excludes AWS-managed KMS aliases and keeps customer aliases", func(t *testing.T) {
		excluded := discoveryListExclusions["AWS::KMS::Alias"]
		assert.True(t, excluded("alias/aws/s3"))
		assert.False(t, excluded("alias/my-key"))
		assert.False(t, excluded("alias/awsome-key"))
	})

	t.Run("excludes the $LATEST pseudo-version and keeps published versions", func(t *testing.T) {
		excluded := discoveryListExclusions["AWS::Lambda::Version"]
		assert.True(t, excluded("arn:aws:lambda:us-east-1:111122223333:function:my-function:$LATEST"))
		assert.False(t, excluded("arn:aws:lambda:us-east-1:111122223333:function:my-function:3"))
	})

	t.Run("has no exclusion for other types", func(t *testing.T) {
		assert.Nil(t, discoveryListExclusions["AWS::S3::Bucket"])
	})
}
