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
	"time"

	awselbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbv2types "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/config"
)

func newServiceWithMocks(ccx ccxClient, ecsCli ecsClient, elb elbv2Client) *Service {
	return &Service{
		cfg:                &config.Config{},
		ccxClientFactory:   func(*config.Config) (ccxClient, error) { return ccx, nil },
		ecsClientFactory:   func(*config.Config) (ecsClient, error) { return ecsCli, nil },
		elbv2ClientFactory: func(*config.Config) (elbv2Client, error) { return elb, nil },
		now:                func() time.Time { return time.Unix(1747526400, 0) },
		operationTimeout:   20 * time.Minute,
		finalReadGrace:     2 * time.Minute,
	}
}

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

func TestService_Create_REPLICA_ECS_WrapsComposite(t *testing.T) {
	ccx := &mockCCXClient{}
	inner := &resource.CreateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:       resource.OperationCreate,
			OperationStatus: resource.OperationStatusInProgress,
			RequestID:       "ccapi-tA",
			NativeID:        "",
		},
	}
	ccx.On("CreateResource", mock.Anything, mock.Anything).Return(inner, nil)

	s := newServiceWithMocks(ccx, nil, nil)
	req := &resource.CreateRequest{
		ResourceType: "AWS::ECS::Service",
		Properties:   []byte(`{"Cluster":"my-cluster","ServiceName":"my-svc"}`),
	}
	res, err := s.Create(context.Background(), req)
	assert.NoError(t, err)
	assert.Equal(t, "formae-ecs/create/1747526400/ccapi-tA", res.ProgressResult.RequestID)
	assert.Equal(t, "pending|my-cluster|my-svc", res.ProgressResult.NativeID)
}

func TestService_Create_CODE_DEPLOY_Passthrough(t *testing.T) {
	ccx := &mockCCXClient{}
	inner := &resource.CreateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:       resource.OperationCreate,
			OperationStatus: resource.OperationStatusInProgress,
			RequestID:       "ccapi-tA",
			NativeID:        "",
		},
	}
	ccx.On("CreateResource", mock.Anything, mock.Anything).Return(inner, nil)

	s := newServiceWithMocks(ccx, nil, nil)
	req := &resource.CreateRequest{
		ResourceType: "AWS::ECS::Service",
		Properties:   []byte(`{"DeploymentController":{"Type":"CODE_DEPLOY"}}`),
	}
	res, err := s.Create(context.Background(), req)
	assert.NoError(t, err)
	// No composite wrap — bare CCAPI token.
	assert.Equal(t, "ccapi-tA", res.ProgressResult.RequestID)
	assert.Equal(t, "", res.ProgressResult.NativeID)
}

func TestService_Create_DAEMON_Passthrough(t *testing.T) {
	ccx := &mockCCXClient{}
	inner := &resource.CreateResult{
		ProgressResult: &resource.ProgressResult{
			OperationStatus: resource.OperationStatusInProgress,
			RequestID:       "ccapi-tA",
		},
	}
	ccx.On("CreateResource", mock.Anything, mock.Anything).Return(inner, nil)

	s := newServiceWithMocks(ccx, nil, nil)
	req := &resource.CreateRequest{
		ResourceType: "AWS::ECS::Service",
		Properties:   []byte(`{"SchedulingStrategy":"DAEMON"}`),
	}
	res, err := s.Create(context.Background(), req)
	assert.NoError(t, err)
	assert.Equal(t, "ccapi-tA", res.ProgressResult.RequestID, "DAEMON shapes pass through without composite wrap")
}

func TestService_Create_REPLICA_MissingServiceName_InvalidRequest(t *testing.T) {
	// shapeSupportsPhaseB(true) AND ServiceName missing → terminal Failure.
	s := newServiceWithMocks(&mockCCXClient{}, nil, nil)
	req := &resource.CreateRequest{
		ResourceType: "AWS::ECS::Service",
		Properties:   []byte(`{"Cluster":"my-cluster"}`),
	}
	res, err := s.Create(context.Background(), req)
	assert.NoError(t, err)
	assert.Equal(t, resource.OperationStatusFailure, res.ProgressResult.OperationStatus)
	assert.Equal(t, resource.OperationErrorCodeInvalidRequest, res.ProgressResult.ErrorCode)
	assert.Contains(t, res.ProgressResult.StatusMessage, "ServiceName")
}

func TestService_Update_REPLICA_ECS_WrapsComposite(t *testing.T) {
	ccx := &mockCCXClient{}
	inner := &resource.UpdateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:       resource.OperationUpdate,
			OperationStatus: resource.OperationStatusInProgress,
			RequestID:       "ccapi-tU",
			NativeID:        "",
		},
	}
	ccx.On("UpdateResource", mock.Anything, mock.Anything).Return(inner, nil)

	s := newServiceWithMocks(ccx, nil, nil)
	canonical := "arn:aws:ecs:us-east-1:123:service/my-cluster/my-svc|my-cluster"
	req := &resource.UpdateRequest{
		ResourceType:    "AWS::ECS::Service",
		NativeID:        canonical,
		PriorProperties: []byte(`{"Cluster":"my-cluster","ServiceName":"my-svc"}`),
	}
	res, err := s.Update(context.Background(), req)
	assert.NoError(t, err)
	assert.Equal(t, "formae-ecs/update/1747526400/ccapi-tU", res.ProgressResult.RequestID)
	assert.Equal(t, canonical, res.ProgressResult.NativeID)
}

func TestService_Update_DAEMON_Passthrough(t *testing.T) {
	ccx := &mockCCXClient{}
	inner := &resource.UpdateResult{
		ProgressResult: &resource.ProgressResult{
			OperationStatus: resource.OperationStatusInProgress,
			RequestID:       "ccapi-tU",
		},
	}
	ccx.On("UpdateResource", mock.Anything, mock.Anything).Return(inner, nil)

	s := newServiceWithMocks(ccx, nil, nil)
	req := &resource.UpdateRequest{
		ResourceType:    "AWS::ECS::Service",
		NativeID:        "arn:aws:ecs:us-east-1:123:service/c/s|c",
		PriorProperties: []byte(`{"SchedulingStrategy":"DAEMON"}`),
	}
	res, err := s.Update(context.Background(), req)
	assert.NoError(t, err)
	assert.Equal(t, "ccapi-tU", res.ProgressResult.RequestID, "DAEMON passes through")
}

func TestService_Update_SyncNotFound_RewritesToGeneralServiceException(t *testing.T) {
	// Simulates ccx.UpdateResource's preflight GetResource returning NotFound
	// for an OOB-deleted service. NotFound is recoverable in the SDK — we must
	// rewrite to GeneralServiceException to avoid operator retry loops.
	ccx := &mockCCXClient{}
	inner := &resource.UpdateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:       resource.OperationUpdate,
			OperationStatus: resource.OperationStatusFailure,
			ErrorCode:       resource.OperationErrorCodeNotFound,
			StatusMessage:   "ResourceNotFoundException",
		},
	}
	ccx.On("UpdateResource", mock.Anything, mock.Anything).Return(inner, nil)

	s := newServiceWithMocks(ccx, nil, nil)
	req := &resource.UpdateRequest{
		ResourceType:    "AWS::ECS::Service",
		NativeID:        "arn:aws:ecs:us-east-1:123:service/c/s|c",
		PriorProperties: []byte(`{"Cluster":"c","ServiceName":"s"}`),
	}
	res, err := s.Update(context.Background(), req)
	assert.NoError(t, err)
	assert.Equal(t, resource.OperationStatusFailure, res.ProgressResult.OperationStatus)
	assert.Equal(t, resource.OperationErrorCodeGeneralServiceException, res.ProgressResult.ErrorCode)
	assert.Contains(t, res.ProgressResult.StatusMessage, "deleted out-of-band")
}

func TestService_Update_SyncThrottling_Propagates(t *testing.T) {
	ccx := &mockCCXClient{}
	inner := &resource.UpdateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:       resource.OperationUpdate,
			OperationStatus: resource.OperationStatusFailure,
			ErrorCode:       resource.OperationErrorCodeThrottling,
			StatusMessage:   "Rate exceeded",
		},
	}
	ccx.On("UpdateResource", mock.Anything, mock.Anything).Return(inner, nil)

	s := newServiceWithMocks(ccx, nil, nil)
	req := &resource.UpdateRequest{
		ResourceType:    "AWS::ECS::Service",
		NativeID:        "arn:aws:ecs:us-east-1:123:service/c/s|c",
		PriorProperties: []byte(`{"Cluster":"c","ServiceName":"s"}`),
	}
	res, err := s.Update(context.Background(), req)
	assert.NoError(t, err)
	// Don't over-intercept. Throttling passes through; operator's CRUD retry path handles it.
	assert.Equal(t, resource.OperationErrorCodeThrottling, res.ProgressResult.ErrorCode)
}

func TestService_Status_BareToken_DelegatesToCCXStatus(t *testing.T) {
	// Non-composite RequestID — defensive fallback for CODE_DEPLOY/EXTERNAL/DAEMON
	// shapes or legacy replays.
	ccx := &mockCCXClient{}
	inner := &resource.StatusResult{
		ProgressResult: &resource.ProgressResult{
			Operation:       resource.OperationCreate,
			OperationStatus: resource.OperationStatusInProgress,
			RequestID:       "f470d40b-d23c-4d3a-9c11-uuid",
		},
	}
	ccx.On("StatusResource", mock.Anything, mock.Anything, mock.Anything).Return(inner, nil)

	s := newServiceWithMocks(ccx, nil, nil)
	res, err := s.Status(context.Background(), &resource.StatusRequest{
		RequestID:    "f470d40b-d23c-4d3a-9c11-uuid",
		NativeID:     "",
		ResourceType: "AWS::ECS::Service",
	})
	assert.NoError(t, err)
	assert.Equal(t, resource.OperationStatusInProgress, res.ProgressResult.OperationStatus)
}

func TestService_Status_MalformedNativeID_TerminalInvalidRequest(t *testing.T) {
	s := newServiceWithMocks(&mockCCXClient{}, nil, nil)
	res, err := s.Status(context.Background(), &resource.StatusRequest{
		RequestID:    "formae-ecs/create/1747526400/tA",
		NativeID:     "garbage",
		ResourceType: "AWS::ECS::Service",
	})
	assert.NoError(t, err)
	assert.Equal(t, resource.OperationStatusFailure, res.ProgressResult.OperationStatus)
	assert.Equal(t, resource.OperationErrorCodeInvalidRequest, res.ProgressResult.ErrorCode)
}

func TestService_Create_SyncSuccess_RewritesToInProgress(t *testing.T) {
	ccx := &mockCCXClient{}
	inner := &resource.CreateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationCreate,
			OperationStatus:    resource.OperationStatusSuccess,
			RequestID:          "ccapi-tA",
			NativeID:           "arn:aws:ecs:us-east-1:123:service/my-cluster/my-svc|my-cluster",
			ResourceProperties: []byte(`{"k":"v"}`),
		},
	}
	ccx.On("CreateResource", mock.Anything, mock.Anything).Return(inner, nil)

	s := newServiceWithMocks(ccx, nil, nil)
	req := &resource.CreateRequest{
		ResourceType: "AWS::ECS::Service",
		Properties:   []byte(`{"Cluster":"my-cluster","ServiceName":"my-svc"}`),
	}
	res, err := s.Create(context.Background(), req)
	assert.NoError(t, err)
	assert.Equal(t, resource.OperationStatusInProgress, res.ProgressResult.OperationStatus)
	assert.Nil(t, res.ProgressResult.ResourceProperties)
	assert.Equal(t, "formae-ecs/create/1747526400/ccapi-tA", res.ProgressResult.RequestID)
}

func TestRead_PopulatesEndpoints_HappyPath(t *testing.T) {
	ctx := context.Background()
	mockCCX := &mockCCXReadClient{}
	mockELB := &mockELBv2Client{}

	tgArn := "arn:aws:elasticloadbalancing:us-east-1:123:targetgroup/tg/abc"
	albArn := "arn:aws:elasticloadbalancing:us-east-1:123:loadbalancer/app/a/1"

	mockCCX.On("ReadResource", ctx, mock.Anything).Return(&resource.ReadResult{
		ResourceType: "AWS::ECS::Service",
		Properties: `{
			"ServiceArn": "arn:aws:ecs:us-east-1:123:service/clusterA/svc1",
			"Cluster": "clusterA",
			"LoadBalancers": [
				{"ContainerName":"app","ContainerPort":443,"TargetGroupArn":"` + tgArn + `"}
			]
		}`,
	}, nil)

	mockELB.On("DescribeTargetGroups", ctx, &awselbv2.DescribeTargetGroupsInput{TargetGroupArns: []string{tgArn}}).
		Return(&awselbv2.DescribeTargetGroupsOutput{
			TargetGroups: []elbv2types.TargetGroup{{TargetGroupArn: ptr(tgArn), LoadBalancerArns: []string{albArn}}},
		}, nil)
	mockELB.On("DescribeLoadBalancers", ctx, &awselbv2.DescribeLoadBalancersInput{LoadBalancerArns: []string{albArn}}).
		Return(&awselbv2.DescribeLoadBalancersOutput{
			LoadBalancers: []elbv2types.LoadBalancer{
				{LoadBalancerArn: ptr(albArn), DNSName: ptr("dns-1"), Type: elbv2types.LoadBalancerTypeEnumApplication},
			},
		}, nil)
	mockELB.On("DescribeListeners", ctx, &awselbv2.DescribeListenersInput{LoadBalancerArn: ptr(albArn)}).
		Return(&awselbv2.DescribeListenersOutput{
			Listeners: []elbv2types.Listener{{
				ListenerArn:    ptr("l1"),
				Port:           ptr(int32(443)),
				Protocol:       elbv2types.ProtocolEnumHttps,
				DefaultActions: []elbv2types.Action{{Type: elbv2types.ActionTypeEnumForward, TargetGroupArn: ptr(tgArn)}},
			}},
		}, nil)

	svc := &Service{
		cfg:                nil,
		elbv2ClientFactory: func(_ *config.Config) (elbv2Client, error) { return mockELB, nil },
		now:                time.Now,
	}

	out, err := svc.readWithClient(ctx, mockCCX, &resource.ReadRequest{
		NativeID:     "clusterA|svc1",
		ResourceType: "AWS::ECS::Service",
	})
	assert.NoError(t, err)
	assert.NotNil(t, out)
	assert.Contains(t, out.Properties, `"Endpoints":{"app:443":"https://dns-1:443"}`)
}

func TestRead_TransientError_ReturnsRecoverablePluginError(t *testing.T) {
	ctx := context.Background()
	mockCCX := &mockCCXReadClient{}
	mockELB := &mockELBv2Client{}

	tgArn := "arn:aws:elasticloadbalancing:us-east-1:123:targetgroup/tg/abc"

	mockCCX.On("ReadResource", ctx, mock.Anything).Return(&resource.ReadResult{
		ResourceType: "AWS::ECS::Service",
		Properties: `{
			"ServiceArn": "arn:aws:ecs:us-east-1:123:service/clusterA/svc1",
			"Cluster": "arn:aws:ecs:us-east-1:123:cluster/clusterA",
			"LoadBalancers": [
				{"ContainerName":"app","ContainerPort":443,"TargetGroupArn":"` + tgArn + `"}
			]
		}`,
	}, nil)

	// All 3 retries throttle.
	mockELB.On("DescribeTargetGroups", ctx, mock.Anything).
		Return((*awselbv2.DescribeTargetGroupsOutput)(nil), awsAPIErr("Throttling", "rate exceeded")).Times(3)

	svc := &Service{
		cfg:                nil,
		elbv2ClientFactory: func(_ *config.Config) (elbv2Client, error) { return mockELB, nil },
		now:                time.Now,
	}

	out, err := svc.readWithClient(ctx, mockCCX, &resource.ReadRequest{
		NativeID:     "clusterA|svc1",
		ResourceType: "AWS::ECS::Service",
	})
	assert.NoError(t, err)
	assert.NotNil(t, out)
	// Recoverable plugin error code surfaced; no Endpoints in the response.
	assert.Equal(t, resource.OperationErrorCodeThrottling, out.ErrorCode)
	assert.Empty(t, out.Properties)
}

func TestRead_EmptyLoadBalancers_EmitsEmptyEndpoints(t *testing.T) {
	ctx := context.Background()
	mockCCX := &mockCCXReadClient{}
	mockELB := &mockELBv2Client{}

	mockCCX.On("ReadResource", ctx, mock.Anything).Return(&resource.ReadResult{
		ResourceType: "AWS::ECS::Service",
		Properties: `{
			"ServiceArn": "arn:aws:ecs:us-east-1:123:service/clusterA/svc1",
			"Cluster": "arn:aws:ecs:us-east-1:123:cluster/clusterA",
			"LoadBalancers": []
		}`,
	}, nil)

	// composeEndpoints should make ZERO API calls for empty LBs.

	svc := &Service{
		cfg:                nil,
		elbv2ClientFactory: func(_ *config.Config) (elbv2Client, error) { return mockELB, nil },
		now:                time.Now,
	}

	out, err := svc.readWithClient(ctx, mockCCX, &resource.ReadRequest{
		NativeID:     "clusterA|svc1",
		ResourceType: "AWS::ECS::Service",
	})
	assert.NoError(t, err)
	assert.NotNil(t, out)
	assert.Contains(t, out.Properties, `"Endpoints":{}`)
	mockELB.AssertExpectations(t)
}
