// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package ecs

import (
	"context"
	"encoding/json"
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

func TestTaskSet_Update_CallsUpdateTaskSetWithParsedNativeIDAndScale(t *testing.T) {
	ctx := context.Background()
	client := &mockECSTaskSetClient{}

	clusterArn := "arn:aws:ecs:us-east-1:123:cluster/my-cluster"
	serviceName := "my-service"
	taskSetID := "ecs-svc/1111111111111111111"
	nativeID := clusterArn + "|" + serviceName + "|" + taskSetID

	desired := json.RawMessage(`{
		"Cluster": "` + clusterArn + `",
		"Service": "arn:aws:ecs:us-east-1:123:service/my-cluster/my-service",
		"TaskDefinition": "arn:aws:ecs:us-east-1:123:task-definition/foo:1",
		"LaunchType": "FARGATE",
		"Scale": {"Unit": "PERCENT", "Value": 50}
	}`)

	client.On("UpdateTaskSet", ctx, mock.MatchedBy(func(input *ecs.UpdateTaskSetInput) bool {
		return input.Cluster != nil && *input.Cluster == clusterArn &&
			input.Service != nil && *input.Service == serviceName &&
			input.TaskSet != nil && *input.TaskSet == taskSetID &&
			input.Scale != nil && input.Scale.Unit == ecstypes.ScaleUnitPercent && input.Scale.Value == 50
	})).Return(&ecs.UpdateTaskSetOutput{
		TaskSet: &ecstypes.TaskSet{
			Id:         aws.String(taskSetID),
			ClusterArn: aws.String(clusterArn),
			ServiceArn: aws.String("arn:aws:ecs:us-east-1:123:service/my-cluster/my-service"),
			Scale:      &ecstypes.Scale{Unit: ecstypes.ScaleUnitPercent, Value: 50},
		},
	}, nil)

	ts := &TaskSet{cfg: &config.Config{}}
	result, err := ts.updateWithClient(ctx, client, &resource.UpdateRequest{
		ResourceType:      "AWS::ECS::TaskSet",
		NativeID:          nativeID,
		DesiredProperties: desired,
	})

	assert.NoError(t, err)
	assert.Equal(t, resource.OperationStatusSuccess, result.ProgressResult.OperationStatus)
	assert.Equal(t, nativeID, result.ProgressResult.NativeID)
	// The returned properties must reflect the updated Scale value so
	// downstream idempotency checks see the intended state.
	var props map[string]any
	assert.NoError(t, json.Unmarshal(result.ProgressResult.ResourceProperties, &props))
	scale, ok := props["Scale"].(map[string]any)
	assert.True(t, ok, "Scale missing from result properties")
	assert.Equal(t, "PERCENT", scale["Unit"])
	assert.InDelta(t, float64(50), scale["Value"].(float64), 0.001)
	client.AssertExpectations(t)
}

func TestTaskSet_Update_ErrorsWhenNativeIDIsNotComposite(t *testing.T) {
	ctx := context.Background()
	client := &mockECSTaskSetClient{}

	ts := &TaskSet{cfg: &config.Config{}}
	_, err := ts.updateWithClient(ctx, client, &resource.UpdateRequest{
		ResourceType:      "AWS::ECS::TaskSet",
		NativeID:          "not-a-composite",
		DesiredProperties: json.RawMessage(`{"Scale":{"Unit":"PERCENT","Value":0}}`),
	})

	assert.Error(t, err)
	client.AssertNotCalled(t, "UpdateTaskSet")
}

func TestTaskSet_Update_ErrorsWhenScaleIsMissingFromProperties(t *testing.T) {
	ctx := context.Background()
	client := &mockECSTaskSetClient{}

	ts := &TaskSet{cfg: &config.Config{}}
	_, err := ts.updateWithClient(ctx, client, &resource.UpdateRequest{
		ResourceType:      "AWS::ECS::TaskSet",
		NativeID:          "arn:aws:ecs:us-east-1:123:cluster/c|s|ecs-svc/1",
		DesiredProperties: json.RawMessage(`{"Cluster":"c","Service":"s"}`),
	})

	assert.Error(t, err)
	client.AssertNotCalled(t, "UpdateTaskSet")
}

func TestTaskSet_Update_PropagatesAWSError(t *testing.T) {
	ctx := context.Background()
	client := &mockECSTaskSetClient{}

	client.On("UpdateTaskSet", ctx, mock.Anything).Return(
		(*ecs.UpdateTaskSetOutput)(nil),
		errors.New("throttled"),
	)

	ts := &TaskSet{cfg: &config.Config{}}
	_, err := ts.updateWithClient(ctx, client, &resource.UpdateRequest{
		ResourceType:      "AWS::ECS::TaskSet",
		NativeID:          "arn:aws:ecs:us-east-1:123:cluster/c|s|ecs-svc/1",
		DesiredProperties: json.RawMessage(`{"Scale":{"Unit":"PERCENT","Value":100}}`),
	})

	assert.Error(t, err)
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

func TestTaskSet_Read_ReinflatesBareClusterAndServiceToArns(t *testing.T) {
	// CC Read on TaskSet normalizes Cluster and Service to their bare
	// names even when the caller created with ARNs. Both are createOnly,
	// so the drift surfaces as phantom Replace on every reapply unless we
	// normalize Read to match the caller's ARN shape. Composite NativeID
	// supplies the cluster ARN (parts[0]) which we use to derive region
	// and account.
	ctx := context.Background()
	client := &mockCCXReadClient{}

	clusterArn := "arn:aws:ecs:us-east-1:226695765433:cluster/my-cluster"
	serviceArn := "arn:aws:ecs:us-east-1:226695765433:service/my-cluster/my-svc"
	nativeID := clusterArn + "|my-svc|ecs-svc/111"

	innerResult := &resource.ReadResult{
		ResourceType: "AWS::ECS::TaskSet",
		Properties: `{
			"Cluster": "my-cluster",
			"Service": "my-svc",
			"Id": "ecs-svc/111"
		}`,
	}
	client.On("ReadResource", ctx, mock.Anything).Return(innerResult, nil)

	ts := &TaskSet{cfg: &config.Config{}}
	out, err := ts.readWithClient(ctx, client, &resource.ReadRequest{
		ResourceType: "AWS::ECS::TaskSet",
		NativeID:     nativeID,
	})

	assert.NoError(t, err)
	var props map[string]any
	assert.NoError(t, json.Unmarshal([]byte(out.Properties), &props))
	assert.Equal(t, clusterArn, props["Cluster"], "Cluster must be re-inflated to full ARN")
	assert.Equal(t, serviceArn, props["Service"], "Service must be re-inflated to full ARN")
	assert.Equal(t, "ecs-svc/111", props["Id"])
}

func TestTaskSet_Read_LeavesValuesAloneWhenAlreadyArns(t *testing.T) {
	ctx := context.Background()
	client := &mockCCXReadClient{}

	clusterArn := "arn:aws:ecs:us-east-1:226695765433:cluster/my-cluster"
	serviceArn := "arn:aws:ecs:us-east-1:226695765433:service/my-cluster/my-svc"
	innerResult := &resource.ReadResult{
		ResourceType: "AWS::ECS::TaskSet",
		Properties: `{
			"Cluster": "` + clusterArn + `",
			"Service": "` + serviceArn + `"
		}`,
	}
	client.On("ReadResource", ctx, mock.Anything).Return(innerResult, nil)

	ts := &TaskSet{cfg: &config.Config{}}
	out, err := ts.readWithClient(ctx, client, &resource.ReadRequest{
		ResourceType: "AWS::ECS::TaskSet",
		NativeID:     clusterArn + "|my-svc|ecs-svc/111",
	})

	assert.NoError(t, err)
	var props map[string]any
	assert.NoError(t, json.Unmarshal([]byte(out.Properties), &props))
	assert.Equal(t, clusterArn, props["Cluster"])
	assert.Equal(t, serviceArn, props["Service"])
}

func TestTaskSet_Read_PassesThroughWhenNativeIDPart0NotArn(t *testing.T) {
	// If we can't infer region/account from NativeID we have nothing safe
	// to work with — leave the short names in place.
	ctx := context.Background()
	client := &mockCCXReadClient{}

	innerResult := &resource.ReadResult{
		ResourceType: "AWS::ECS::TaskSet",
		Properties:   `{"Cluster": "my-cluster", "Service": "my-svc"}`,
	}
	client.On("ReadResource", ctx, mock.Anything).Return(innerResult, nil)

	ts := &TaskSet{cfg: &config.Config{}}
	out, err := ts.readWithClient(ctx, client, &resource.ReadRequest{
		ResourceType: "AWS::ECS::TaskSet",
		NativeID:     "my-cluster|my-svc|ecs-svc/111",
	})

	assert.NoError(t, err)
	var props map[string]any
	assert.NoError(t, json.Unmarshal([]byte(out.Properties), &props))
	assert.Equal(t, "my-cluster", props["Cluster"])
	assert.Equal(t, "my-svc", props["Service"])
}

func TestTaskSet_Read_PropagatesErrorResult(t *testing.T) {
	ctx := context.Background()
	client := &mockCCXReadClient{}
	innerResult := &resource.ReadResult{
		ResourceType: "AWS::ECS::TaskSet",
		ErrorCode:    resource.OperationErrorCodeNotFound,
	}
	client.On("ReadResource", ctx, mock.Anything).Return(innerResult, nil)

	ts := &TaskSet{cfg: &config.Config{}}
	out, err := ts.readWithClient(ctx, client, &resource.ReadRequest{
		ResourceType: "AWS::ECS::TaskSet",
		NativeID:     "missing|missing|missing",
	})

	assert.NoError(t, err)
	assert.Equal(t, resource.OperationErrorCodeNotFound, out.ErrorCode)
}

func TestTaskSet_Read_PropagatesInnerError(t *testing.T) {
	ctx := context.Background()
	client := &mockCCXReadClient{}
	client.On("ReadResource", ctx, mock.Anything).Return((*resource.ReadResult)(nil), errors.New("throttled"))

	ts := &TaskSet{cfg: &config.Config{}}
	_, err := ts.readWithClient(ctx, client, &resource.ReadRequest{
		ResourceType: "AWS::ECS::TaskSet",
		NativeID:     "arn:aws:ecs:us-east-1:123:cluster/c|s|ecs-svc/1",
	})

	assert.Error(t, err)
}
