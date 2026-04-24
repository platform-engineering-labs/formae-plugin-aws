// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package lambda

import (
	"context"

	awslambda "github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/stretchr/testify/mock"
)

type mockEventInvokeConfigClient struct {
	mock.Mock
}

func (m *mockEventInvokeConfigClient) UpdateFunctionEventInvokeConfig(ctx context.Context, input *awslambda.UpdateFunctionEventInvokeConfigInput, optFns ...func(*awslambda.Options)) (*awslambda.UpdateFunctionEventInvokeConfigOutput, error) {
	args := m.Called(ctx, input)
	out, _ := args.Get(0).(*awslambda.UpdateFunctionEventInvokeConfigOutput)
	return out, args.Error(1)
}
