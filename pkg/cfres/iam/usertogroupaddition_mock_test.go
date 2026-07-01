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

type mockUserToGroupAdditionClient struct {
	mock.Mock
}

func (m *mockUserToGroupAdditionClient) GetGroup(ctx context.Context, input *iam.GetGroupInput, optFns ...func(*iam.Options)) (*iam.GetGroupOutput, error) {
	args := m.Called(ctx, input)
	out, _ := args.Get(0).(*iam.GetGroupOutput)
	return out, args.Error(1)
}

func (m *mockUserToGroupAdditionClient) AddUserToGroup(ctx context.Context, input *iam.AddUserToGroupInput, optFns ...func(*iam.Options)) (*iam.AddUserToGroupOutput, error) {
	args := m.Called(ctx, input)
	out, _ := args.Get(0).(*iam.AddUserToGroupOutput)
	return out, args.Error(1)
}

func (m *mockUserToGroupAdditionClient) RemoveUserFromGroup(ctx context.Context, input *iam.RemoveUserFromGroupInput, optFns ...func(*iam.Options)) (*iam.RemoveUserFromGroupOutput, error) {
	args := m.Called(ctx, input)
	out, _ := args.Get(0).(*iam.RemoveUserFromGroupOutput)
	return out, args.Error(1)
}
