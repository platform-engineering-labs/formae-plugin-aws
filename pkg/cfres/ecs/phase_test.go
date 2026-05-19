// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package ecs

import (
	"context"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsecs "github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	awselbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbv2types "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
)

func TestCheckPhaseA_InProgress(t *testing.T) {
	ccx := &mockCCXClient{}
	ccx.On("StatusResource", mock.Anything, mock.Anything, mock.Anything).Return(
		&resource.StatusResult{
			ProgressResult: &resource.ProgressResult{
				Operation:       resource.OperationCreate,
				OperationStatus: resource.OperationStatusInProgress,
				StatusMessage:   "CCAPI still working",
			},
		}, nil)

	s := newServiceWithMocks(ccx, nil, nil)
	req := &resource.StatusRequest{
		RequestID: "formae-ecs/create/1747526400/tA",
		NativeID:  "pending|c|s",
	}
	res, ok := s.checkPhaseA(context.Background(), req, resource.OperationCreate, 1747526400, "tA")
	assert.False(t, ok, "still in Phase A — Phase B not entered")
	assert.Equal(t, resource.OperationStatusInProgress, res.ProgressResult.OperationStatus)
	assert.Equal(t, "formae-ecs/create/1747526400/tA", res.ProgressResult.RequestID)
}

func TestCheckPhaseA_Success_TransitionsToPhaseB(t *testing.T) {
	ccx := &mockCCXClient{}
	ccx.On("StatusResource", mock.Anything, mock.Anything, mock.Anything).Return(
		&resource.StatusResult{
			ProgressResult: &resource.ProgressResult{
				Operation:       resource.OperationCreate,
				OperationStatus: resource.OperationStatusSuccess,
			},
		}, nil)

	s := newServiceWithMocks(ccx, nil, nil)
	req := &resource.StatusRequest{RequestID: "formae-ecs/create/1747526400/tA", NativeID: "pending|c|s"}
	_, ok := s.checkPhaseA(context.Background(), req, resource.OperationCreate, 1747526400, "tA")
	assert.True(t, ok, "Phase B should be entered after CCAPI Success")
}

func TestCheckPhaseA_Failure_Propagates(t *testing.T) {
	ccx := &mockCCXClient{}
	ccx.On("StatusResource", mock.Anything, mock.Anything, mock.Anything).Return(
		&resource.StatusResult{
			ProgressResult: &resource.ProgressResult{
				Operation:       resource.OperationCreate,
				OperationStatus: resource.OperationStatusFailure,
				ErrorCode:       resource.OperationErrorCodeInvalidRequest,
				StatusMessage:   "bad CFN payload",
			},
		}, nil)

	s := newServiceWithMocks(ccx, nil, nil)
	req := &resource.StatusRequest{RequestID: "formae-ecs/create/1747526400/tA", NativeID: "pending|c|s"}
	res, ok := s.checkPhaseA(context.Background(), req, resource.OperationCreate, 1747526400, "tA")
	assert.False(t, ok)
	assert.Equal(t, resource.OperationStatusFailure, res.ProgressResult.OperationStatus)
	assert.Equal(t, resource.OperationErrorCodeInvalidRequest, res.ProgressResult.ErrorCode)
}

func TestCheckPhaseA_UpdateNotFound_RewritesToGeneralServiceException(t *testing.T) {
	// Async Phase A intercept: ccx.StatusResource only remaps NotFound→InProgress
	// for Create. For Update, NotFound (OOB delete pre-CCAPI-Success) propagates,
	// which is recoverable per SDK. We must rewrite to GeneralServiceException.
	ccx := &mockCCXClient{}
	ccx.On("StatusResource", mock.Anything, mock.Anything, mock.Anything).Return(
		&resource.StatusResult{
			ProgressResult: &resource.ProgressResult{
				Operation:       resource.OperationUpdate,
				OperationStatus: resource.OperationStatusFailure,
				ErrorCode:       resource.OperationErrorCodeNotFound,
				StatusMessage:   "NotFound from CCAPI",
			},
		}, nil)

	s := newServiceWithMocks(ccx, nil, nil)
	req := &resource.StatusRequest{
		RequestID: "formae-ecs/update/1747526400/tU",
		NativeID:  "arn:aws:ecs:us-east-1:123:service/c/s|c",
	}
	res, ok := s.checkPhaseA(context.Background(), req, resource.OperationUpdate, 1747526400, "tU")
	assert.False(t, ok)
	assert.Equal(t, resource.OperationErrorCodeGeneralServiceException, res.ProgressResult.ErrorCode)
	assert.Contains(t, res.ProgressResult.StatusMessage, "deleted out-of-band")
}

func TestCheckPhaseA_CreateNotFound_NotIntercepted(t *testing.T) {
	// In normal operation, ccx already turned Create+NotFound into InProgress. If
	// we ever see Create+NotFound here, propagate (don't apply the Update intercept).
	ccx := &mockCCXClient{}
	ccx.On("StatusResource", mock.Anything, mock.Anything, mock.Anything).Return(
		&resource.StatusResult{
			ProgressResult: &resource.ProgressResult{
				Operation:       resource.OperationCreate,
				OperationStatus: resource.OperationStatusFailure,
				ErrorCode:       resource.OperationErrorCodeNotFound,
			},
		}, nil)

	s := newServiceWithMocks(ccx, nil, nil)
	req := &resource.StatusRequest{RequestID: "formae-ecs/create/1747526400/tA", NativeID: "pending|c|s"}
	res, ok := s.checkPhaseA(context.Background(), req, resource.OperationCreate, 1747526400, "tA")
	assert.False(t, ok)
	assert.Equal(t, resource.OperationErrorCodeNotFound, res.ProgressResult.ErrorCode,
		"Create NotFound passes through unchanged; ccx-level remap is the intended handler")
}

func TestCheckPhaseA_CCAPIError_RetryableInProgress(t *testing.T) {
	ccx := &mockCCXClient{}
	ccx.On("StatusResource", mock.Anything, mock.Anything, mock.Anything).Return(
		(*resource.StatusResult)(nil), &fakeAPIError{code: "Throttling"})

	s := newServiceWithMocks(ccx, nil, nil)
	req := &resource.StatusRequest{RequestID: "formae-ecs/create/1747526400/tA", NativeID: "pending|c|s"}
	res, ok := s.checkPhaseA(context.Background(), req, resource.OperationCreate, 1747526400, "tA")
	assert.False(t, ok)
	assert.Equal(t, resource.OperationStatusInProgress, res.ProgressResult.OperationStatus)
}

func TestStatusPhaseB_INACTIVEFailure_TerminalGeneralServiceException(t *testing.T) {
	ecsCli := &mockECSClient{}
	ecsCli.On("DescribeServices", mock.Anything, mock.Anything).Return(
		&awsecs.DescribeServicesOutput{
			Failures: []ecstypes.Failure{{Reason: aws.String("INACTIVE")}},
		}, nil)

	s := newServiceWithMocks(&mockCCXClient{}, ecsCli, nil)
	req := &resource.StatusRequest{RequestID: "formae-ecs/create/1747526400/tA", NativeID: "pending|c|s"}
	res, err := s.statusPhaseB(context.Background(), req, resource.OperationCreate, 1747526400, "c", "s")
	assert.NoError(t, err)
	assert.Equal(t, resource.OperationStatusFailure, res.ProgressResult.OperationStatus)
	assert.Equal(t, resource.OperationErrorCodeGeneralServiceException, res.ProgressResult.ErrorCode)
	assert.Contains(t, res.ProgressResult.StatusMessage, "deleted out-of-band")
}

func TestStatusPhaseB_INACTIVEServiceStatus_TerminalGeneralServiceException(t *testing.T) {
	ecsCli := &mockECSClient{}
	ecsCli.On("DescribeServices", mock.Anything, mock.Anything).Return(
		&awsecs.DescribeServicesOutput{
			Services: []ecstypes.Service{{Status: aws.String("INACTIVE")}},
		}, nil)

	s := newServiceWithMocks(&mockCCXClient{}, ecsCli, nil)
	req := &resource.StatusRequest{RequestID: "formae-ecs/create/1747526400/tA", NativeID: "pending|c|s"}
	res, err := s.statusPhaseB(context.Background(), req, resource.OperationCreate, 1747526400, "c", "s")
	assert.NoError(t, err)
	assert.Equal(t, resource.OperationErrorCodeGeneralServiceException, res.ProgressResult.ErrorCode)
}

func TestStatusPhaseB_ServiceMissing_InProgressBounded(t *testing.T) {
	ecsCli := &mockECSClient{}
	ecsCli.On("DescribeServices", mock.Anything, mock.Anything).Return(
		&awsecs.DescribeServicesOutput{}, nil)

	s := newServiceWithMocks(&mockCCXClient{}, ecsCli, nil)
	req := &resource.StatusRequest{RequestID: "formae-ecs/create/1747526400/tA", NativeID: "pending|c|s"}
	res, err := s.statusPhaseB(context.Background(), req, resource.OperationCreate, 1747526400, "c", "s")
	assert.NoError(t, err)
	assert.Equal(t, resource.OperationStatusInProgress, res.ProgressResult.OperationStatus)
	assert.Contains(t, res.ProgressResult.StatusMessage, "not yet visible")
}

func TestStatusPhaseB_RolloutInProgress_InProgress(t *testing.T) {
	now := time.Unix(1747526400, 0)
	ecsCli := &mockECSClient{}
	ecsCli.On("DescribeServices", mock.Anything, mock.Anything).Return(
		&awsecs.DescribeServicesOutput{
			Services: []ecstypes.Service{{
				Status:       aws.String("ACTIVE"),
				RunningCount: 0,
				DesiredCount: 1,
				Deployments: []ecstypes.Deployment{{
					Status:       aws.String("PRIMARY"),
					RolloutState: ecstypes.DeploymentRolloutStateInProgress,
					CreatedAt:    &now,
				}},
			}},
		}, nil)

	s := newServiceWithMocks(&mockCCXClient{}, ecsCli, nil)
	req := &resource.StatusRequest{RequestID: "formae-ecs/create/1747526400/tA", NativeID: "pending|c|s"}
	res, err := s.statusPhaseB(context.Background(), req, resource.OperationCreate, 1747526400, "c", "s")
	assert.NoError(t, err)
	assert.Equal(t, resource.OperationStatusInProgress, res.ProgressResult.OperationStatus)
	assert.Contains(t, res.ProgressResult.StatusMessage, "0/1")
}

func TestStatusPhaseB_RolloutFailed_TerminalGeneralServiceException(t *testing.T) {
	now := time.Unix(1747526400, 0)
	ecsCli := &mockECSClient{}
	ecsCli.On("DescribeServices", mock.Anything, mock.Anything).Return(
		&awsecs.DescribeServicesOutput{
			Services: []ecstypes.Service{{
				Status:       aws.String("ACTIVE"),
				RunningCount: 0,
				DesiredCount: 1,
				Deployments: []ecstypes.Deployment{{
					Status:             aws.String("PRIMARY"),
					RolloutState:       ecstypes.DeploymentRolloutStateFailed,
					RolloutStateReason: aws.String("Circuit breaker tripped"),
					CreatedAt:          &now,
				}},
			}},
		}, nil)

	s := newServiceWithMocks(&mockCCXClient{}, ecsCli, nil)
	req := &resource.StatusRequest{RequestID: "formae-ecs/create/1747526400/tA", NativeID: "pending|c|s"}
	res, err := s.statusPhaseB(context.Background(), req, resource.OperationCreate, 1747526400, "c", "s")
	assert.NoError(t, err)
	assert.Equal(t, resource.OperationErrorCodeGeneralServiceException, res.ProgressResult.ErrorCode)
	assert.Contains(t, res.ProgressResult.StatusMessage, "Circuit breaker")
}

func TestStatusPhaseB_PastTimeout_NotStable_Terminal(t *testing.T) {
	primaryStart := time.Unix(1747526400, 0)
	ecsCli := &mockECSClient{}
	ecsCli.On("DescribeServices", mock.Anything, mock.Anything).Return(
		&awsecs.DescribeServicesOutput{
			Services: []ecstypes.Service{{
				Status:       aws.String("ACTIVE"),
				RunningCount: 0,
				DesiredCount: 1,
				Deployments: []ecstypes.Deployment{{
					Status:       aws.String("PRIMARY"),
					RolloutState: ecstypes.DeploymentRolloutStateInProgress,
					CreatedAt:    &primaryStart,
				}},
			}},
		}, nil)

	s := newServiceWithMocks(&mockCCXClient{}, ecsCli, nil)
	s.now = func() time.Time { return primaryStart.Add(25 * time.Minute) }
	req := &resource.StatusRequest{RequestID: "formae-ecs/create/1747526400/tA", NativeID: "pending|c|s"}
	res, err := s.statusPhaseB(context.Background(), req, resource.OperationCreate, 1747526400, "c", "s")
	assert.NoError(t, err)
	assert.Equal(t, resource.OperationStatusFailure, res.ProgressResult.OperationStatus)
	assert.Equal(t, resource.OperationErrorCodeGeneralServiceException, res.ProgressResult.ErrorCode)
}

func TestStatusPhaseB_DesiredCountZero_NoTGCheck(t *testing.T) {
	primaryStart := time.Unix(1747526400, 0)
	ecsCli := &mockECSClient{}
	ecsCli.On("DescribeServices", mock.Anything, mock.Anything).Return(
		&awsecs.DescribeServicesOutput{
			Services: []ecstypes.Service{{
				Status:       aws.String("ACTIVE"),
				ServiceArn:   aws.String("arn:aws:ecs:us-east-1:123:service/c/s"),
				RunningCount: 0,
				DesiredCount: 0,
				LoadBalancers: []ecstypes.LoadBalancer{
					{TargetGroupArn: aws.String("arn:tg:1")}, // attached but desired=0 → skip
				},
				Deployments: []ecstypes.Deployment{{
					Status:       aws.String("PRIMARY"),
					RolloutState: ecstypes.DeploymentRolloutStateCompleted,
					CreatedAt:    &primaryStart,
				}},
			}},
		}, nil)

	ccx := &mockCCXClient{}
	ccx.On("ReadResource", mock.Anything, mock.Anything).Return(
		&resource.ReadResult{ResourceType: "AWS::ECS::Service", Properties: `{"DesiredCount":0}`}, nil)

	elb := &mockELBv2Client{} // never called — assert via mock expectations

	s := newServiceWithMocks(ccx, ecsCli, elb)
	req := &resource.StatusRequest{RequestID: "formae-ecs/create/1747526400/tA", NativeID: "pending|c|s"}
	res, err := s.statusPhaseB(context.Background(), req, resource.OperationCreate, 1747526400, "c", "s")
	assert.NoError(t, err)
	assert.Equal(t, resource.OperationStatusSuccess, res.ProgressResult.OperationStatus)
	elb.AssertNotCalled(t, "DescribeTargetHealth")
}

func TestStatusPhaseB_ClassicELB_SkipsTGCheck(t *testing.T) {
	primaryStart := time.Unix(1747526400, 0)
	ecsCli := &mockECSClient{}
	ecsCli.On("DescribeServices", mock.Anything, mock.Anything).Return(
		&awsecs.DescribeServicesOutput{
			Services: []ecstypes.Service{{
				Status:       aws.String("ACTIVE"),
				ServiceArn:   aws.String("arn:aws:ecs:us-east-1:123:service/c/s"),
				RunningCount: 1,
				DesiredCount: 1,
				LoadBalancers: []ecstypes.LoadBalancer{
					// Classic ELB attachment — no TargetGroupArn, only LoadBalancerName.
					{LoadBalancerName: aws.String("classic-elb-1")},
				},
				Deployments: []ecstypes.Deployment{{
					Status:       aws.String("PRIMARY"),
					RolloutState: ecstypes.DeploymentRolloutStateCompleted,
					CreatedAt:    &primaryStart,
				}},
			}},
		}, nil)

	ccx := &mockCCXClient{}
	ccx.On("ReadResource", mock.Anything, mock.Anything).Return(
		&resource.ReadResult{ResourceType: "AWS::ECS::Service", Properties: `{"DesiredCount":1}`}, nil)

	elb := &mockELBv2Client{}
	s := newServiceWithMocks(ccx, ecsCli, elb)
	req := &resource.StatusRequest{RequestID: "formae-ecs/create/1747526400/tA", NativeID: "pending|c|s"}
	res, err := s.statusPhaseB(context.Background(), req, resource.OperationCreate, 1747526400, "c", "s")
	assert.NoError(t, err)
	assert.Equal(t, resource.OperationStatusSuccess, res.ProgressResult.OperationStatus)
	elb.AssertNotCalled(t, "DescribeTargetHealth")
}

func TestStatusPhaseB_FinalReadNotFound_WithinGrace_InProgress(t *testing.T) {
	primaryStart := time.Unix(1747526400, 0)
	ecsCli := &mockECSClient{}
	ecsCli.On("DescribeServices", mock.Anything, mock.Anything).Return(
		&awsecs.DescribeServicesOutput{
			Services: []ecstypes.Service{{
				Status:       aws.String("ACTIVE"),
				ServiceArn:   aws.String("arn:aws:ecs:us-east-1:123:service/c/s"),
				RunningCount: 1,
				DesiredCount: 1,
				Deployments: []ecstypes.Deployment{{
					Status:       aws.String("PRIMARY"),
					RolloutState: ecstypes.DeploymentRolloutStateCompleted,
					CreatedAt:    &primaryStart,
				}},
			}},
		}, nil)
	ccx := &mockCCXClient{}
	// Final Read returns NotFound (eventual consistency on a just-stabilized service).
	ccx.On("ReadResource", mock.Anything, mock.Anything).Return(
		&resource.ReadResult{
			ResourceType: "AWS::ECS::Service",
			ErrorCode:    resource.OperationErrorCodeNotFound,
		}, nil)

	s := newServiceWithMocks(ccx, ecsCli, nil)
	// 20m 30s after unixStart — inside operationTimeout + finalReadGrace (22m).
	s.now = func() time.Time { return time.Unix(1747526400, 0).Add(20*time.Minute + 30*time.Second) }

	req := &resource.StatusRequest{RequestID: "formae-ecs/create/1747526400/tA", NativeID: "pending|c|s"}
	res, err := s.statusPhaseB(context.Background(), req, resource.OperationCreate, 1747526400, "c", "s")
	assert.NoError(t, err)
	assert.Equal(t, resource.OperationStatusInProgress, res.ProgressResult.OperationStatus,
		"stable service within finalReadGrace should still be InProgress, not terminal")
}

func TestStatusPhaseB_FinalReadAccessDenied_Terminal(t *testing.T) {
	primaryStart := time.Unix(1747526400, 0)
	ecsCli := &mockECSClient{}
	ecsCli.On("DescribeServices", mock.Anything, mock.Anything).Return(
		&awsecs.DescribeServicesOutput{
			Services: []ecstypes.Service{{
				Status:       aws.String("ACTIVE"),
				ServiceArn:   aws.String("arn:aws:ecs:us-east-1:123:service/c/s"),
				RunningCount: 1,
				DesiredCount: 1,
				Deployments: []ecstypes.Deployment{{
					Status:       aws.String("PRIMARY"),
					RolloutState: ecstypes.DeploymentRolloutStateCompleted,
					CreatedAt:    &primaryStart,
				}},
			}},
		}, nil)
	ccx := &mockCCXClient{}
	ccx.On("ReadResource", mock.Anything, mock.Anything).Return(
		&resource.ReadResult{ErrorCode: resource.OperationErrorCodeAccessDenied}, nil)

	s := newServiceWithMocks(ccx, ecsCli, nil)
	req := &resource.StatusRequest{RequestID: "formae-ecs/create/1747526400/tA", NativeID: "pending|c|s"}
	res, err := s.statusPhaseB(context.Background(), req, resource.OperationCreate, 1747526400, "c", "s")
	assert.NoError(t, err)
	assert.Equal(t, resource.OperationStatusFailure, res.ProgressResult.OperationStatus)
	assert.Equal(t, resource.OperationErrorCodeAccessDenied, res.ProgressResult.ErrorCode)
}

func TestStatusPhaseB_StableAtBoundary_SucceedsNotTimeoutsOut(t *testing.T) {
	// Service stabilized just before the 20m boundary. Final Read succeeds.
	// Verify Success even though `now - unixStart` > operationTimeout would
	// have escalated via inProgressOrTimeout — but the success path doesn't
	// route through that helper.
	primaryStart := time.Unix(1747526400, 0)
	ecsCli := &mockECSClient{}
	ecsCli.On("DescribeServices", mock.Anything, mock.Anything).Return(
		&awsecs.DescribeServicesOutput{
			Services: []ecstypes.Service{{
				Status:       aws.String("ACTIVE"),
				ServiceArn:   aws.String("arn:aws:ecs:us-east-1:123:service/c/s"),
				RunningCount: 1,
				DesiredCount: 1,
				Deployments: []ecstypes.Deployment{{
					Status:       aws.String("PRIMARY"),
					RolloutState: ecstypes.DeploymentRolloutStateCompleted,
					CreatedAt:    &primaryStart,
				}},
			}},
		}, nil)
	ccx := &mockCCXClient{}
	ccx.On("ReadResource", mock.Anything, mock.Anything).Return(
		&resource.ReadResult{ResourceType: "AWS::ECS::Service", Properties: `{"k":"v"}`}, nil)

	s := newServiceWithMocks(ccx, ecsCli, nil)
	s.now = func() time.Time { return time.Unix(1747526400, 0).Add(20*time.Minute + 1*time.Second) }

	req := &resource.StatusRequest{RequestID: "formae-ecs/create/1747526400/tA", NativeID: "pending|c|s"}
	res, err := s.statusPhaseB(context.Background(), req, resource.OperationCreate, 1747526400, "c", "s")
	assert.NoError(t, err)
	assert.Equal(t, resource.OperationStatusSuccess, res.ProgressResult.OperationStatus)
}

func TestStatusPhaseB_TGUnhealthy_InProgress(t *testing.T) {
	primaryStart := time.Unix(1747526400, 0)
	ecsCli := &mockECSClient{}
	ecsCli.On("DescribeServices", mock.Anything, mock.Anything).Return(
		&awsecs.DescribeServicesOutput{
			Services: []ecstypes.Service{{
				Status:       aws.String("ACTIVE"),
				RunningCount: 1,
				DesiredCount: 1,
				LoadBalancers: []ecstypes.LoadBalancer{
					{TargetGroupArn: aws.String("arn:tg:1")},
				},
				Deployments: []ecstypes.Deployment{{
					Status:       aws.String("PRIMARY"),
					RolloutState: ecstypes.DeploymentRolloutStateCompleted,
					CreatedAt:    &primaryStart,
				}},
			}},
		}, nil)

	elb := &mockELBv2Client{}
	elb.On("DescribeTargetHealth", mock.Anything, mock.Anything).Return(
		&awselbv2.DescribeTargetHealthOutput{
			TargetHealthDescriptions: []elbv2types.TargetHealthDescription{{
				TargetHealth: &elbv2types.TargetHealth{State: elbv2types.TargetHealthStateEnumUnhealthy},
			}},
		}, nil)

	s := newServiceWithMocks(&mockCCXClient{}, ecsCli, elb)
	req := &resource.StatusRequest{RequestID: "formae-ecs/create/1747526400/tA", NativeID: "pending|c|s"}
	res, err := s.statusPhaseB(context.Background(), req, resource.OperationCreate, 1747526400, "c", "s")
	assert.NoError(t, err)
	assert.Equal(t, resource.OperationStatusInProgress, res.ProgressResult.OperationStatus)
	assert.Contains(t, res.ProgressResult.StatusMessage, "healthy")
}
