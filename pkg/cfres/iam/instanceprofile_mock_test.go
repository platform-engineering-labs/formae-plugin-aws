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

type mockInstanceProfileClient struct {
	mock.Mock
}

func (m *mockInstanceProfileClient) AddRoleToInstanceProfile(ctx context.Context, input *iam.AddRoleToInstanceProfileInput, optFns ...func(*iam.Options)) (*iam.AddRoleToInstanceProfileOutput, error) {
	args := m.Called(ctx, input)
	return args.Get(0).(*iam.AddRoleToInstanceProfileOutput), args.Error(1)
}

func (m *mockInstanceProfileClient) RemoveRoleFromInstanceProfile(ctx context.Context, input *iam.RemoveRoleFromInstanceProfileInput, optFns ...func(*iam.Options)) (*iam.RemoveRoleFromInstanceProfileOutput, error) {
	args := m.Called(ctx, input)
	return args.Get(0).(*iam.RemoveRoleFromInstanceProfileOutput), args.Error(1)
}
