// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package ecs

import (
	"context"

	awsecs "github.com/aws/aws-sdk-go-v2/service/ecs"
	awselbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	"github.com/stretchr/testify/mock"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
)

// mockCCXClient implements the full ccxClient interface (Read+Create+Update+Status).
// We keep the name from the prior mockCCXReadClient since the existing Read tests
// reference it; we just add the new methods.
type mockCCXClient struct {
	mock.Mock
}

func (m *mockCCXClient) ReadResource(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	args := m.Called(ctx, request)
	out, _ := args.Get(0).(*resource.ReadResult)
	return out, args.Error(1)
}

func (m *mockCCXClient) CreateResource(ctx context.Context, request *resource.CreateRequest) (*resource.CreateResult, error) {
	args := m.Called(ctx, request)
	out, _ := args.Get(0).(*resource.CreateResult)
	return out, args.Error(1)
}

func (m *mockCCXClient) UpdateResource(ctx context.Context, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	args := m.Called(ctx, request)
	out, _ := args.Get(0).(*resource.UpdateResult)
	return out, args.Error(1)
}

func (m *mockCCXClient) StatusResource(ctx context.Context, request *resource.StatusRequest,
	readFunc func(context.Context, *resource.ReadRequest) (*resource.ReadResult, error)) (*resource.StatusResult, error) {
	args := m.Called(ctx, request, readFunc)
	out, _ := args.Get(0).(*resource.StatusResult)
	return out, args.Error(1)
}

// Alias retained for the existing Read tests which reference mockCCXReadClient.
type mockCCXReadClient = mockCCXClient

type mockECSClient struct {
	mock.Mock
}

func (m *mockECSClient) DescribeServices(ctx context.Context, params *awsecs.DescribeServicesInput,
	optFns ...func(*awsecs.Options)) (*awsecs.DescribeServicesOutput, error) {
	args := m.Called(ctx, params)
	out, _ := args.Get(0).(*awsecs.DescribeServicesOutput)
	return out, args.Error(1)
}

type mockELBv2Client struct {
	mock.Mock
}

func (m *mockELBv2Client) DescribeTargetHealth(ctx context.Context, params *awselbv2.DescribeTargetHealthInput,
	optFns ...func(*awselbv2.Options)) (*awselbv2.DescribeTargetHealthOutput, error) {
	args := m.Called(ctx, params)
	out, _ := args.Get(0).(*awselbv2.DescribeTargetHealthOutput)
	return out, args.Error(1)
}

func (m *mockELBv2Client) DescribeTargetGroups(ctx context.Context, params *awselbv2.DescribeTargetGroupsInput,
	optFns ...func(*awselbv2.Options)) (*awselbv2.DescribeTargetGroupsOutput, error) {
	args := m.Called(ctx, params)
	out, _ := args.Get(0).(*awselbv2.DescribeTargetGroupsOutput)
	return out, args.Error(1)
}

func (m *mockELBv2Client) DescribeLoadBalancers(ctx context.Context, params *awselbv2.DescribeLoadBalancersInput,
	optFns ...func(*awselbv2.Options)) (*awselbv2.DescribeLoadBalancersOutput, error) {
	args := m.Called(ctx, params)
	out, _ := args.Get(0).(*awselbv2.DescribeLoadBalancersOutput)
	return out, args.Error(1)
}

func (m *mockELBv2Client) DescribeListeners(ctx context.Context, params *awselbv2.DescribeListenersInput,
	optFns ...func(*awselbv2.Options)) (*awselbv2.DescribeListenersOutput, error) {
	args := m.Called(ctx, params)
	out, _ := args.Get(0).(*awselbv2.DescribeListenersOutput)
	return out, args.Error(1)
}
