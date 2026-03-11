// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package s3

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/service/s3control"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/stretchr/testify/mock"
)

type mockS3ControlClient struct {
	mock.Mock
}

func (m *mockS3ControlClient) UpdateStorageLensGroup(ctx context.Context, input *s3control.UpdateStorageLensGroupInput, optFns ...func(*s3control.Options)) (*s3control.UpdateStorageLensGroupOutput, error) {
	args := m.Called(ctx, input)
	return args.Get(0).(*s3control.UpdateStorageLensGroupOutput), args.Error(1)
}

type mockSTSClient struct {
	mock.Mock
}

func (m *mockSTSClient) GetCallerIdentity(ctx context.Context, input *sts.GetCallerIdentityInput, optFns ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error) {
	args := m.Called(ctx, input)
	return args.Get(0).(*sts.GetCallerIdentityOutput), args.Error(1)
}
