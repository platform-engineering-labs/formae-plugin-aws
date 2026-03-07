// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package sqs

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/stretchr/testify/mock"
)

type mockSQSClient struct {
	mock.Mock
}

func (m *mockSQSClient) SetQueueAttributes(ctx context.Context, input *sqs.SetQueueAttributesInput, optFns ...func(*sqs.Options)) (*sqs.SetQueueAttributesOutput, error) {
	args := m.Called(ctx, input)
	return args.Get(0).(*sqs.SetQueueAttributesOutput), args.Error(1)
}

func (m *mockSQSClient) GetQueueAttributes(ctx context.Context, input *sqs.GetQueueAttributesInput, optFns ...func(*sqs.Options)) (*sqs.GetQueueAttributesOutput, error) {
	args := m.Called(ctx, input)
	return args.Get(0).(*sqs.GetQueueAttributesOutput), args.Error(1)
}
