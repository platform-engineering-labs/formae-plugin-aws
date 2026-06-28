// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package iam

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/stretchr/testify/mock"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
)

// mockRoleClient mocks the IAM calls the custom Role Read uses to enrich inline
// policies. ListRolePolicies is shared in shape with mockIAMClient but this mock
// also carries GetRolePolicy.
type mockRoleClient struct {
	mock.Mock
}

func (m *mockRoleClient) ListRolePolicies(ctx context.Context, input *iam.ListRolePoliciesInput, optFns ...func(*iam.Options)) (*iam.ListRolePoliciesOutput, error) {
	args := m.Called(ctx, input)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*iam.ListRolePoliciesOutput), args.Error(1)
}

func (m *mockRoleClient) GetRolePolicy(ctx context.Context, input *iam.GetRolePolicyInput, optFns ...func(*iam.Options)) (*iam.GetRolePolicyOutput, error) {
	args := m.Called(ctx, input)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*iam.GetRolePolicyOutput), args.Error(1)
}

// mockRoleCCXReader mocks the generic CloudControl read the custom Role Read
// delegates to before enrichment.
type mockRoleCCXReader struct {
	mock.Mock
}

func (m *mockRoleCCXReader) ReadResource(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	args := m.Called(ctx, request)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*resource.ReadResult), args.Error(1)
}

func matchGetRolePolicy(roleName, policyName string) any {
	return mock.MatchedBy(func(input *iam.GetRolePolicyInput) bool {
		return input.RoleName != nil && *input.RoleName == roleName &&
			input.PolicyName != nil && *input.PolicyName == policyName
	})
}
