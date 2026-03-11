// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package iam

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/stretchr/testify/mock"
)

type mockPolicyClient struct {
	mock.Mock
}

func (m *mockPolicyClient) PutRolePolicy(ctx context.Context, input *iam.PutRolePolicyInput, optFns ...func(*iam.Options)) (*iam.PutRolePolicyOutput, error) {
	args := m.Called(ctx, input)
	return args.Get(0).(*iam.PutRolePolicyOutput), args.Error(1)
}

func (m *mockPolicyClient) GetRolePolicy(ctx context.Context, input *iam.GetRolePolicyInput, optFns ...func(*iam.Options)) (*iam.GetRolePolicyOutput, error) {
	args := m.Called(ctx, input)
	return args.Get(0).(*iam.GetRolePolicyOutput), args.Error(1)
}

func (m *mockPolicyClient) DeleteRolePolicy(ctx context.Context, input *iam.DeleteRolePolicyInput, optFns ...func(*iam.Options)) (*iam.DeleteRolePolicyOutput, error) {
	args := m.Called(ctx, input)
	return args.Get(0).(*iam.DeleteRolePolicyOutput), args.Error(1)
}

func (m *mockPolicyClient) PutUserPolicy(ctx context.Context, input *iam.PutUserPolicyInput, optFns ...func(*iam.Options)) (*iam.PutUserPolicyOutput, error) {
	args := m.Called(ctx, input)
	return args.Get(0).(*iam.PutUserPolicyOutput), args.Error(1)
}

func (m *mockPolicyClient) GetUserPolicy(ctx context.Context, input *iam.GetUserPolicyInput, optFns ...func(*iam.Options)) (*iam.GetUserPolicyOutput, error) {
	args := m.Called(ctx, input)
	return args.Get(0).(*iam.GetUserPolicyOutput), args.Error(1)
}

func (m *mockPolicyClient) DeleteUserPolicy(ctx context.Context, input *iam.DeleteUserPolicyInput, optFns ...func(*iam.Options)) (*iam.DeleteUserPolicyOutput, error) {
	args := m.Called(ctx, input)
	return args.Get(0).(*iam.DeleteUserPolicyOutput), args.Error(1)
}

func (m *mockPolicyClient) PutGroupPolicy(ctx context.Context, input *iam.PutGroupPolicyInput, optFns ...func(*iam.Options)) (*iam.PutGroupPolicyOutput, error) {
	args := m.Called(ctx, input)
	return args.Get(0).(*iam.PutGroupPolicyOutput), args.Error(1)
}

func (m *mockPolicyClient) GetGroupPolicy(ctx context.Context, input *iam.GetGroupPolicyInput, optFns ...func(*iam.Options)) (*iam.GetGroupPolicyOutput, error) {
	args := m.Called(ctx, input)
	return args.Get(0).(*iam.GetGroupPolicyOutput), args.Error(1)
}

func (m *mockPolicyClient) DeleteGroupPolicy(ctx context.Context, input *iam.DeleteGroupPolicyInput, optFns ...func(*iam.Options)) (*iam.DeleteGroupPolicyOutput, error) {
	args := m.Called(ctx, input)
	return args.Get(0).(*iam.DeleteGroupPolicyOutput), args.Error(1)
}
