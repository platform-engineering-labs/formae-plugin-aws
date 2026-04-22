// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package ecs

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/config"
)

func TestTaskSet_List_ReturnsCompositeNativeIDsFromService(t *testing.T) {
	ctx := context.Background()
	client := &mockECSTaskSetClient{}

	clusterArg := "my-cluster"
	serviceArg := "my-service"
	clusterArn := "arn:aws:ecs:us-east-1:123:cluster/my-cluster"
	serviceArn := "arn:aws:ecs:us-east-1:123:service/my-cluster/my-service"

	client.On("DescribeServices", ctx, mock.MatchedBy(func(input *ecs.DescribeServicesInput) bool {
		return input.Cluster != nil && *input.Cluster == clusterArg &&
			len(input.Services) == 1 && input.Services[0] == serviceArg
	})).Return(&ecs.DescribeServicesOutput{
		Services: []ecstypes.Service{
			{
				ClusterArn: aws.String(clusterArn),
				ServiceArn: aws.String(serviceArn),
				TaskSets: []ecstypes.TaskSet{
					{
						Id:         aws.String("ecs-svc/1111111111111111111"),
						ClusterArn: aws.String(clusterArn),
						ServiceArn: aws.String(serviceArn),
					},
					{
						Id:         aws.String("ecs-svc/2222222222222222222"),
						ClusterArn: aws.String(clusterArn),
						ServiceArn: aws.String(serviceArn),
					},
				},
			},
		},
	}, nil)

	ts := &TaskSet{cfg: &config.Config{}}
	result, err := ts.listWithClient(ctx, client, &resource.ListRequest{
		ResourceType: "AWS::ECS::TaskSet",
		AdditionalProperties: map[string]string{
			"Cluster": clusterArg,
			"Service": serviceArg,
		},
	})

	assert.NoError(t, err)
	// Native ID shape mirrors the CloudControl CRUD path:
	// <ClusterArn>|<ServiceName>|<Id>.
	assert.Equal(t, []string{
		"arn:aws:ecs:us-east-1:123:cluster/my-cluster|my-service|ecs-svc/1111111111111111111",
		"arn:aws:ecs:us-east-1:123:cluster/my-cluster|my-service|ecs-svc/2222222222222222222",
	}, result.NativeIDs)
	client.AssertExpectations(t)
}

func TestTaskSet_List_ReturnsEmptyWhenServiceHasNoTaskSets(t *testing.T) {
	ctx := context.Background()
	client := &mockECSTaskSetClient{}
	client.On("DescribeServices", ctx, mock.Anything).Return(&ecs.DescribeServicesOutput{
		Services: []ecstypes.Service{
			{
				ClusterArn: aws.String("arn:aws:ecs:us-east-1:123:cluster/my-cluster"),
				ServiceArn: aws.String("arn:aws:ecs:us-east-1:123:service/my-cluster/my-service"),
				TaskSets:   nil,
			},
		},
	}, nil)

	ts := &TaskSet{cfg: &config.Config{}}
	result, err := ts.listWithClient(ctx, client, &resource.ListRequest{
		ResourceType: "AWS::ECS::TaskSet",
		AdditionalProperties: map[string]string{
			"Cluster": "my-cluster",
			"Service": "my-service",
		},
	})

	assert.NoError(t, err)
	assert.Empty(t, result.NativeIDs)
}

func TestTaskSet_List_ReturnsEmptyWhenServiceNotFoundInResponse(t *testing.T) {
	ctx := context.Background()
	client := &mockECSTaskSetClient{}
	client.On("DescribeServices", ctx, mock.Anything).Return(&ecs.DescribeServicesOutput{
		Services: []ecstypes.Service{},
		Failures: []ecstypes.Failure{{Reason: aws.String("MISSING")}},
	}, nil)

	ts := &TaskSet{cfg: &config.Config{}}
	result, err := ts.listWithClient(ctx, client, &resource.ListRequest{
		ResourceType: "AWS::ECS::TaskSet",
		AdditionalProperties: map[string]string{
			"Cluster": "my-cluster",
			"Service": "my-service",
		},
	})

	assert.NoError(t, err, "missing service should not fail discovery")
	assert.Empty(t, result.NativeIDs)
}

func TestTaskSet_List_ErrorsWhenClusterFilterMissing(t *testing.T) {
	ctx := context.Background()
	client := &mockECSTaskSetClient{}

	ts := &TaskSet{cfg: &config.Config{}}
	_, err := ts.listWithClient(ctx, client, &resource.ListRequest{
		ResourceType: "AWS::ECS::TaskSet",
		AdditionalProperties: map[string]string{
			"Service": "my-service",
		},
	})

	assert.Error(t, err)
	client.AssertNotCalled(t, "DescribeServices")
}

func TestTaskSet_List_ErrorsWhenServiceFilterMissing(t *testing.T) {
	ctx := context.Background()
	client := &mockECSTaskSetClient{}

	ts := &TaskSet{cfg: &config.Config{}}
	_, err := ts.listWithClient(ctx, client, &resource.ListRequest{
		ResourceType: "AWS::ECS::TaskSet",
		AdditionalProperties: map[string]string{
			"Cluster": "my-cluster",
		},
	})

	assert.Error(t, err)
	client.AssertNotCalled(t, "DescribeServices")
}

func TestTaskSet_List_ReturnsEmptyOnClusterNotFound(t *testing.T) {
	ctx := context.Background()
	client := &mockECSTaskSetClient{}
	client.On("DescribeServices", ctx, mock.Anything).Return(
		(*ecs.DescribeServicesOutput)(nil),
		&ecstypes.ClusterNotFoundException{},
	)

	ts := &TaskSet{cfg: &config.Config{}}
	result, err := ts.listWithClient(ctx, client, &resource.ListRequest{
		ResourceType: "AWS::ECS::TaskSet",
		AdditionalProperties: map[string]string{
			"Cluster": "my-cluster",
			"Service": "my-service",
		},
	})

	assert.NoError(t, err, "missing parent cluster should not fail discovery")
	assert.Empty(t, result.NativeIDs)
}

func TestTaskSet_List_PropagatesOtherErrors(t *testing.T) {
	ctx := context.Background()
	client := &mockECSTaskSetClient{}
	client.On("DescribeServices", ctx, mock.Anything).Return(
		(*ecs.DescribeServicesOutput)(nil),
		errors.New("throttled"),
	)

	ts := &TaskSet{cfg: &config.Config{}}
	_, err := ts.listWithClient(ctx, client, &resource.ListRequest{
		ResourceType: "AWS::ECS::TaskSet",
		AdditionalProperties: map[string]string{
			"Cluster": "my-cluster",
			"Service": "my-service",
		},
	})

	assert.Error(t, err)
}
