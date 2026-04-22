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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/config"
)

func TestService_Read_ReinflatesBareClusterNameToArn(t *testing.T) {
	ctx := context.Background()
	client := &mockCCXReadClient{}

	// CC's Read normalizes Cluster to the bare name even when the caller
	// created with the full ARN. ServiceArn is always the full ARN.
	innerResult := &resource.ReadResult{
		ResourceType: "AWS::ECS::Service",
		Properties: `{
			"ServiceName": "my-svc",
			"Cluster": "my-cluster",
			"ServiceArn": "arn:aws:ecs:us-east-1:226695765433:service/my-cluster/my-svc"
		}`,
	}
	client.On("ReadResource", ctx, mock.Anything).Return(innerResult, nil)

	svc := &Service{cfg: &config.Config{}}
	out, err := svc.readWithClient(ctx, client, &resource.ReadRequest{
		ResourceType: "AWS::ECS::Service",
		NativeID:     "my-cluster|my-svc",
	})

	assert.NoError(t, err)
	var props map[string]any
	assert.NoError(t, json.Unmarshal([]byte(out.Properties), &props))
	assert.Equal(t, "arn:aws:ecs:us-east-1:226695765433:cluster/my-cluster", props["Cluster"],
		"Cluster must be re-inflated from bare name to full ARN to match what the caller sent on Create")
	assert.Equal(t, "my-svc", props["ServiceName"])
}

func TestService_Read_LeavesClusterAloneWhenAlreadyArn(t *testing.T) {
	ctx := context.Background()
	client := &mockCCXReadClient{}
	arn := "arn:aws:ecs:us-east-1:226695765433:cluster/my-cluster"
	innerResult := &resource.ReadResult{
		ResourceType: "AWS::ECS::Service",
		Properties: `{
			"Cluster": "` + arn + `",
			"ServiceArn": "arn:aws:ecs:us-east-1:226695765433:service/my-cluster/my-svc"
		}`,
	}
	client.On("ReadResource", ctx, mock.Anything).Return(innerResult, nil)

	svc := &Service{cfg: &config.Config{}}
	out, err := svc.readWithClient(ctx, client, &resource.ReadRequest{
		ResourceType: "AWS::ECS::Service",
		NativeID:     "my-cluster|my-svc",
	})

	assert.NoError(t, err)
	var props map[string]any
	assert.NoError(t, json.Unmarshal([]byte(out.Properties), &props))
	assert.Equal(t, arn, props["Cluster"])
}

func TestService_Read_PassesThroughWhenServiceArnMissing(t *testing.T) {
	// Without a ServiceArn we can't infer region/account, so we have no
	// choice but to leave the bare name in place. Better to let the
	// planner see the drift than guess wrong.
	ctx := context.Background()
	client := &mockCCXReadClient{}
	innerResult := &resource.ReadResult{
		ResourceType: "AWS::ECS::Service",
		Properties:   `{"Cluster": "my-cluster"}`,
	}
	client.On("ReadResource", ctx, mock.Anything).Return(innerResult, nil)

	svc := &Service{cfg: &config.Config{}}
	out, err := svc.readWithClient(ctx, client, &resource.ReadRequest{
		ResourceType: "AWS::ECS::Service",
		NativeID:     "my-cluster|my-svc",
	})

	assert.NoError(t, err)
	var props map[string]any
	assert.NoError(t, json.Unmarshal([]byte(out.Properties), &props))
	assert.Equal(t, "my-cluster", props["Cluster"])
}

func TestService_Read_PropagatesErrorResult(t *testing.T) {
	ctx := context.Background()
	client := &mockCCXReadClient{}
	innerResult := &resource.ReadResult{
		ResourceType: "AWS::ECS::Service",
		ErrorCode:    resource.OperationErrorCodeNotFound,
	}
	client.On("ReadResource", ctx, mock.Anything).Return(innerResult, nil)

	svc := &Service{cfg: &config.Config{}}
	out, err := svc.readWithClient(ctx, client, &resource.ReadRequest{
		ResourceType: "AWS::ECS::Service",
		NativeID:     "missing",
	})

	assert.NoError(t, err)
	assert.Equal(t, resource.OperationErrorCodeNotFound, out.ErrorCode)
}

func TestService_Read_PropagatesInnerError(t *testing.T) {
	ctx := context.Background()
	client := &mockCCXReadClient{}
	client.On("ReadResource", ctx, mock.Anything).Return((*resource.ReadResult)(nil), errors.New("throttled"))

	svc := &Service{cfg: &config.Config{}}
	_, err := svc.readWithClient(ctx, client, &resource.ReadRequest{
		ResourceType: "AWS::ECS::Service",
		NativeID:     "my-cluster|my-svc",
	})

	assert.Error(t, err)
}
