// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package secretsmanager

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/config"
)

// mockCCXReader mocks the Cloud Control read path.
type mockCCXReader struct {
	mock.Mock
}

func (m *mockCCXReader) ReadResource(ctx context.Context, req *resource.ReadRequest) (*resource.ReadResult, error) {
	args := m.Called(ctx, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*resource.ReadResult), args.Error(1)
}

// mockSMClient mocks the SecretsManager GetSecretValue call.
type mockSMClient struct {
	mock.Mock
}

func (m *mockSMClient) GetSecretValue(ctx context.Context, input *secretsmanager.GetSecretValueInput, optFns ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error) {
	args := m.Called(ctx, input)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*secretsmanager.GetSecretValueOutput), args.Error(1)
}

func TestSecret_Read_AlwaysEnrichesSecretValue(t *testing.T) {
	// Read unconditionally enriches: the secret value is always fetched from
	// SecretsManager and merged into the result properties. The agent decides
	// how to store it (opaque hashing), so the plugin never withholds it.
	ctx := context.Background()
	ccxMock := &mockCCXReader{}
	smMock := &mockSMClient{}

	ccxMock.On("ReadResource", ctx, mock.Anything).Return(&resource.ReadResult{
		ResourceType: "AWS::SecretsManager::Secret",
		Properties:   `{"Name":"my-secret","ARN":"arn:aws:secretsmanager:us-east-1:123456789012:secret:my-secret"}`,
	}, nil)

	secretVal := "super-secret-value"
	smMock.On("GetSecretValue", ctx, mock.MatchedBy(func(in *secretsmanager.GetSecretValueInput) bool {
		return in.SecretId != nil && *in.SecretId == "my-secret-id"
	})).Return(&secretsmanager.GetSecretValueOutput{
		SecretString: aws.String(secretVal),
	}, nil)

	s := &Secret{cfg: &config.Config{}}
	result, err := s.readWithClients(ctx, ccxMock, smMock, &resource.ReadRequest{
		NativeID:     "my-secret-id",
		ResourceType: "AWS::SecretsManager::Secret",
	})

	require.NoError(t, err)
	require.NotNil(t, result)

	var props map[string]any
	require.NoError(t, json.Unmarshal([]byte(result.Properties), &props))
	assert.Equal(t, secretVal, props["SecretString"], "SecretString must be present in result")

	ccxMock.AssertExpectations(t)
	smMock.AssertExpectations(t)
}

func TestSecret_Read_SecretBinaryNotEnriched(t *testing.T) {
	// SecretBinary is intentionally excluded from enrichment: the agent's
	// opaque-hashing table only covers SecretString for this resource type.
	// Returning raw binary data would persist it as base64 plaintext at rest.
	// This test asserts the absence of SecretBinary in the result properties
	// when the AWS API returns a binary secret.
	ctx := context.Background()
	ccxMock := &mockCCXReader{}
	smMock := &mockSMClient{}

	ccxMock.On("ReadResource", ctx, mock.Anything).Return(&resource.ReadResult{
		ResourceType: "AWS::SecretsManager::Secret",
		Properties:   `{"Name":"binary-secret"}`,
	}, nil)

	smMock.On("GetSecretValue", ctx, mock.Anything).Return(&secretsmanager.GetSecretValueOutput{
		SecretBinary: []byte("binary-payload"),
	}, nil)

	s := &Secret{cfg: &config.Config{}}
	result, err := s.readWithClients(ctx, ccxMock, smMock, &resource.ReadRequest{
		NativeID:     "binary-secret-id",
		ResourceType: "AWS::SecretsManager::Secret",
	})

	require.NoError(t, err)
	require.NotNil(t, result)

	var props map[string]any
	require.NoError(t, json.Unmarshal([]byte(result.Properties), &props))
	_, hasSecretBinary := props["SecretBinary"]
	assert.False(t, hasSecretBinary, "SecretBinary must not be present in result (no opaque coverage in agent)")
	_, hasSecretString := props["SecretString"]
	assert.False(t, hasSecretString, "SecretString must not be present when the secret has no string value")

	ccxMock.AssertExpectations(t)
	smMock.AssertExpectations(t)
}

func TestSecret_Read_EnrichesEvenWhenCCXReturnsEmptyProperties(t *testing.T) {
	// Enrichment works even when Cloud Control returns an empty properties blob.
	ctx := context.Background()
	ccxMock := &mockCCXReader{}
	smMock := &mockSMClient{}

	ccxMock.On("ReadResource", ctx, mock.Anything).Return(&resource.ReadResult{
		ResourceType: "AWS::SecretsManager::Secret",
		Properties:   "",
	}, nil)

	secretVal := "another-secret"
	smMock.On("GetSecretValue", ctx, mock.Anything).Return(&secretsmanager.GetSecretValueOutput{
		SecretString: aws.String(secretVal),
	}, nil)

	s := &Secret{cfg: &config.Config{}}
	result, err := s.readWithClients(ctx, ccxMock, smMock, &resource.ReadRequest{
		NativeID:     "my-secret-id",
		ResourceType: "AWS::SecretsManager::Secret",
	})

	require.NoError(t, err)
	var props map[string]any
	require.NoError(t, json.Unmarshal([]byte(result.Properties), &props))
	assert.Equal(t, secretVal, props["SecretString"])
}

func TestSecret_Read_GetSecretValueFailureReturnsPartialResult(t *testing.T) {
	// If GetSecretValue fails, Read still returns the Cloud Control result
	// (no secret enrichment) rather than propagating an error.
	ctx := context.Background()
	ccxMock := &mockCCXReader{}
	smMock := &mockSMClient{}

	ccxMock.On("ReadResource", ctx, mock.Anything).Return(&resource.ReadResult{
		ResourceType: "AWS::SecretsManager::Secret",
		Properties:   `{"Name":"my-secret"}`,
	}, nil)

	smMock.On("GetSecretValue", ctx, mock.Anything).Return(
		(*secretsmanager.GetSecretValueOutput)(nil),
		errors.New("access denied"),
	)

	s := &Secret{cfg: &config.Config{}}
	result, err := s.readWithClients(ctx, ccxMock, smMock, &resource.ReadRequest{
		NativeID:     "my-secret-id",
		ResourceType: "AWS::SecretsManager::Secret",
	})

	require.NoError(t, err, "GetSecretValue failure must not propagate as an error")
	require.NotNil(t, result)

	var props map[string]any
	require.NoError(t, json.Unmarshal([]byte(result.Properties), &props))
	_, hasSecret := props["SecretString"]
	assert.False(t, hasSecret, "SecretString must be absent when GetSecretValue fails")
}
