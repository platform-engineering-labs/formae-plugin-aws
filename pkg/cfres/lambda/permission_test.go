// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package lambda

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awslambda "github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const vrefFunctionArn = "arn:aws:lambda:us-east-1:111122223333:function:my-function"

type mockPermissionClient struct {
	// policies maps qualifier ("" for the unqualified function scope) to the
	// policy JSON GetPolicy returns; a missing key returns ResourceNotFound.
	policies     map[string]string
	versionPages [][]lambdatypes.FunctionConfiguration
	aliases      []lambdatypes.AliasConfiguration
	listErr      error
}

func (m *mockPermissionClient) GetPolicy(_ context.Context, in *awslambda.GetPolicyInput, _ ...func(*awslambda.Options)) (*awslambda.GetPolicyOutput, error) {
	q := ""
	if in.Qualifier != nil {
		q = *in.Qualifier
	}
	policy, ok := m.policies[q]
	if !ok {
		return nil, &lambdatypes.ResourceNotFoundException{}
	}
	return &awslambda.GetPolicyOutput{Policy: aws.String(policy)}, nil
}

func (m *mockPermissionClient) ListVersionsByFunction(_ context.Context, in *awslambda.ListVersionsByFunctionInput, _ ...func(*awslambda.Options)) (*awslambda.ListVersionsByFunctionOutput, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	page := 0
	if in.Marker != nil {
		page = 1
	}
	out := &awslambda.ListVersionsByFunctionOutput{}
	if page < len(m.versionPages) {
		out.Versions = m.versionPages[page]
	}
	if page == 0 && len(m.versionPages) > 1 {
		out.NextMarker = aws.String("next")
	}
	return out, nil
}

func (m *mockPermissionClient) ListAliases(_ context.Context, _ *awslambda.ListAliasesInput, _ ...func(*awslambda.Options)) (*awslambda.ListAliasesOutput, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	return &awslambda.ListAliasesOutput{Aliases: m.aliases}, nil
}

func policyDoc(sid, resourceArn string) string {
	return `{"Version":"2012-10-17","Id":"default","Statement":[{"Sid":"` + sid + `","Effect":"Allow","Principal":{"Service":"s3.amazonaws.com"},"Action":"lambda:InvokeFunction","Resource":"` + resourceArn + `"}]}`
}

func TestLambdaPermissionList(t *testing.T) {
	perm := &Permission{}
	request := &resource.ListRequest{AdditionalProperties: map[string]string{"FunctionName": "my-function"}}

	t.Run("emits ids across function, version, and alias policy scopes", func(t *testing.T) {
		client := &mockPermissionClient{
			policies: map[string]string{
				"":     policyDoc("fn-sid", vrefFunctionArn),
				"1":    policyDoc("v1-sid", vrefFunctionArn+":1"),
				"live": policyDoc("alias-sid", vrefFunctionArn+":live"),
			},
			versionPages: [][]lambdatypes.FunctionConfiguration{{
				{Version: aws.String("$LATEST")},
				{Version: aws.String("1")},
			}},
			aliases: []lambdatypes.AliasConfiguration{{Name: aws.String("live")}},
		}
		result, err := perm.listWithClient(context.Background(), client, request)
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{
			vrefFunctionArn + "|fn-sid",
			vrefFunctionArn + ":1|v1-sid",
			vrefFunctionArn + ":live|alias-sid",
		}, result.NativeIDs)
	})

	t.Run("walks every version page", func(t *testing.T) {
		client := &mockPermissionClient{
			policies: map[string]string{
				"2": policyDoc("v2-sid", vrefFunctionArn+":2"),
			},
			versionPages: [][]lambdatypes.FunctionConfiguration{
				{{Version: aws.String("1")}},
				{{Version: aws.String("2")}},
			},
		}
		result, err := perm.listWithClient(context.Background(), client, request)
		require.NoError(t, err)
		assert.Equal(t, []string{vrefFunctionArn + ":2|v2-sid"}, result.NativeIDs)
	})

	t.Run("skips scopes without a policy", func(t *testing.T) {
		client := &mockPermissionClient{
			policies:     map[string]string{"1": policyDoc("v1-sid", vrefFunctionArn+":1")},
			versionPages: [][]lambdatypes.FunctionConfiguration{{{Version: aws.String("1")}}},
		}
		result, err := perm.listWithClient(context.Background(), client, request)
		require.NoError(t, err)
		assert.Equal(t, []string{vrefFunctionArn + ":1|v1-sid"}, result.NativeIDs)
	})

	t.Run("requires the FunctionName filter", func(t *testing.T) {
		_, err := perm.listWithClient(context.Background(), &mockPermissionClient{}, &resource.ListRequest{
			AdditionalProperties: map[string]string{},
		})
		assert.Error(t, err)
	})

	t.Run("treats a deleted function as an empty list", func(t *testing.T) {
		client := &mockPermissionClient{listErr: &lambdatypes.ResourceNotFoundException{}}
		result, err := perm.listWithClient(context.Background(), client, request)
		require.NoError(t, err)
		assert.Empty(t, result.NativeIDs)
	})

	t.Run("propagates other errors", func(t *testing.T) {
		client := &mockPermissionClient{listErr: &lambdatypes.TooManyRequestsException{}}
		_, err := perm.listWithClient(context.Background(), client, request)
		assert.Error(t, err)
	})
}
