// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package ecs

import (
	"context"
	"fmt"

	awsecs "github.com/aws/aws-sdk-go-v2/service/ecs"
	awselbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"

	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/ccx"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/config"
)

// ccxClient is the surface of pkg/ccx the ECS provisioner depends on. Defined
// here so unit tests can mock just the methods we actually call.
type ccxClient interface {
	CreateResource(ctx context.Context, req *resource.CreateRequest) (*resource.CreateResult, error)
	UpdateResource(ctx context.Context, req *resource.UpdateRequest) (*resource.UpdateResult, error)
	StatusResource(ctx context.Context, req *resource.StatusRequest, readFunc func(context.Context, *resource.ReadRequest) (*resource.ReadResult, error)) (*resource.StatusResult, error)
	ReadResource(ctx context.Context, req *resource.ReadRequest) (*resource.ReadResult, error)
}

// ecsClient is the surface of the AWS ECS SDK used by the Service provisioner for stability polling.
type ecsClient interface {
	DescribeServices(ctx context.Context, params *awsecs.DescribeServicesInput, optFns ...func(*awsecs.Options)) (*awsecs.DescribeServicesOutput, error)
}

// elbv2Client is the surface of the AWS ELBv2 SDK used by the Service provisioner
// for target-health checks and endpoint composition.
type elbv2Client interface {
	DescribeTargetHealth(ctx context.Context, params *awselbv2.DescribeTargetHealthInput, optFns ...func(*awselbv2.Options)) (*awselbv2.DescribeTargetHealthOutput, error)
	DescribeTargetGroups(ctx context.Context, params *awselbv2.DescribeTargetGroupsInput, optFns ...func(*awselbv2.Options)) (*awselbv2.DescribeTargetGroupsOutput, error)
	DescribeLoadBalancers(ctx context.Context, params *awselbv2.DescribeLoadBalancersInput, optFns ...func(*awselbv2.Options)) (*awselbv2.DescribeLoadBalancersOutput, error)
	DescribeListeners(ctx context.Context, params *awselbv2.DescribeListenersInput, optFns ...func(*awselbv2.Options)) (*awselbv2.DescribeListenersOutput, error)
}

func defaultCCXClientFactory(cfg *config.Config) (ccxClient, error) {
	return ccx.NewClient(cfg)
}

func defaultECSClientFactory(cfg *config.Config) (ecsClient, error) {
	awsCfg, err := cfg.ToAwsConfig(context.Background())
	if err != nil {
		return nil, fmt.Errorf("ecs: build AWS config: %w", err)
	}
	return awsecs.NewFromConfig(awsCfg), nil
}

func defaultELBv2ClientFactory(cfg *config.Config) (elbv2Client, error) {
	awsCfg, err := cfg.ToAwsConfig(context.Background())
	if err != nil {
		return nil, fmt.Errorf("elbv2: build AWS config: %w", err)
	}
	return awselbv2.NewFromConfig(awsCfg), nil
}
