// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package s3

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/mock"
)

type mockS3Client struct {
	mock.Mock
}

func (m *mockS3Client) PutBucketPolicy(ctx context.Context, input *s3.PutBucketPolicyInput, optFns ...func(*s3.Options)) (*s3.PutBucketPolicyOutput, error) {
	args := m.Called(ctx, input)
	return args.Get(0).(*s3.PutBucketPolicyOutput), args.Error(1)
}
