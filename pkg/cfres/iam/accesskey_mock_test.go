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

type mockAccessKeyClient struct {
	mock.Mock
}

func (m *mockAccessKeyClient) CreateAccessKey(ctx context.Context, input *iam.CreateAccessKeyInput, optFns ...func(*iam.Options)) (*iam.CreateAccessKeyOutput, error) {
	args := m.Called(ctx, input)
	return args.Get(0).(*iam.CreateAccessKeyOutput), args.Error(1)
}

func (m *mockAccessKeyClient) UpdateAccessKey(ctx context.Context, input *iam.UpdateAccessKeyInput, optFns ...func(*iam.Options)) (*iam.UpdateAccessKeyOutput, error) {
	args := m.Called(ctx, input)
	return args.Get(0).(*iam.UpdateAccessKeyOutput), args.Error(1)
}

func (m *mockAccessKeyClient) DeleteAccessKey(ctx context.Context, input *iam.DeleteAccessKeyInput, optFns ...func(*iam.Options)) (*iam.DeleteAccessKeyOutput, error) {
	args := m.Called(ctx, input)
	return args.Get(0).(*iam.DeleteAccessKeyOutput), args.Error(1)
}

func (m *mockAccessKeyClient) ListAccessKeys(ctx context.Context, input *iam.ListAccessKeysInput, optFns ...func(*iam.Options)) (*iam.ListAccessKeysOutput, error) {
	args := m.Called(ctx, input)
	return args.Get(0).(*iam.ListAccessKeysOutput), args.Error(1)
}
