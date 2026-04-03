// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package ec2

import (
	"context"

	ccsdk "github.com/aws/aws-sdk-go-v2/service/cloudcontrol"
	ec2sdk "github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/stretchr/testify/mock"
)

type mockCCClient struct {
	mock.Mock
}

func (m *mockCCClient) ListResources(ctx context.Context, input *ccsdk.ListResourcesInput, optFns ...func(*ccsdk.Options)) (*ccsdk.ListResourcesOutput, error) {
	args := m.Called(ctx, input)
	return args.Get(0).(*ccsdk.ListResourcesOutput), args.Error(1)
}

type mockSRTAClient struct {
	mock.Mock
}

func (m *mockSRTAClient) DescribeRouteTables(ctx context.Context, input *ec2sdk.DescribeRouteTablesInput, optFns ...func(*ec2sdk.Options)) (*ec2sdk.DescribeRouteTablesOutput, error) {
	args := m.Called(ctx, input)
	return args.Get(0).(*ec2sdk.DescribeRouteTablesOutput), args.Error(1)
}
