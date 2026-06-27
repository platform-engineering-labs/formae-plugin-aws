// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package ec2

import (
	"context"

	ec2sdk "github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/stretchr/testify/mock"
)

type mockVgwRoutePropagationClient struct {
	mock.Mock
}

func (m *mockVgwRoutePropagationClient) EnableVgwRoutePropagation(ctx context.Context, input *ec2sdk.EnableVgwRoutePropagationInput, optFns ...func(*ec2sdk.Options)) (*ec2sdk.EnableVgwRoutePropagationOutput, error) {
	args := m.Called(ctx, input)
	return args.Get(0).(*ec2sdk.EnableVgwRoutePropagationOutput), args.Error(1)
}

func (m *mockVgwRoutePropagationClient) DisableVgwRoutePropagation(ctx context.Context, input *ec2sdk.DisableVgwRoutePropagationInput, optFns ...func(*ec2sdk.Options)) (*ec2sdk.DisableVgwRoutePropagationOutput, error) {
	args := m.Called(ctx, input)
	return args.Get(0).(*ec2sdk.DisableVgwRoutePropagationOutput), args.Error(1)
}

func (m *mockVgwRoutePropagationClient) DescribeRouteTables(ctx context.Context, input *ec2sdk.DescribeRouteTablesInput, optFns ...func(*ec2sdk.Options)) (*ec2sdk.DescribeRouteTablesOutput, error) {
	args := m.Called(ctx, input)
	return args.Get(0).(*ec2sdk.DescribeRouteTablesOutput), args.Error(1)
}
