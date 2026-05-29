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
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/config"
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

func TestStatusPhaseB_UpdateWithNewDeployment_NormalFlow(t *testing.T) {
	// Update where primary.CreatedAt >= unixStart - slack (the new deployment IS ours).
	// Phase B proceeds normally, returns InProgress for a still-rolling deployment.
	unixStart := int64(1747526400)
	primaryStart := time.Unix(unixStart, 0).Add(5 * time.Second) // new deployment just appeared
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
	// Now is shortly after the deployment started
	s.now = func() time.Time { return primaryStart.Add(10 * time.Second) }
	req := &resource.StatusRequest{RequestID: "formae-ecs/update/1747526400/tU", NativeID: "pending|c|s"}
	res, err := s.statusPhaseB(context.Background(), req, resource.OperationUpdate, unixStart, "c", "s")
	assert.NoError(t, err)
	assert.Equal(t, resource.OperationStatusInProgress, res.ProgressResult.OperationStatus)
	assert.Contains(t, res.ProgressResult.StatusMessage, "0/1")
}

func TestStatusPhaseB_UpdateNoNewDeployment_WithinGrace_InProgress(t *testing.T) {
	// Update where primary.CreatedAt is well before unixStart-slack AND we are still
	// inside the updateGraceWindow (60s) since unixStart. Should return InProgress
	// with "waiting for new deployment to start".
	unixStart := int64(1747526400)
	primaryStart := time.Unix(unixStart, 0).Add(-1 * time.Hour) // pre-existing deployment
	ecsCli := &mockECSClient{}
	ecsCli.On("DescribeServices", mock.Anything, mock.Anything).Return(
		&awsecs.DescribeServicesOutput{
			Services: []ecstypes.Service{{
				Status:       aws.String("ACTIVE"),
				RunningCount: 1,
				DesiredCount: 1,
				Deployments: []ecstypes.Deployment{{
					Status:       aws.String("PRIMARY"),
					RolloutState: ecstypes.DeploymentRolloutStateCompleted,
					CreatedAt:    &primaryStart,
				}},
			}},
		}, nil)

	s := newServiceWithMocks(&mockCCXClient{}, ecsCli, nil)
	// 30s after unixStart — inside the 60s grace window
	s.now = func() time.Time { return time.Unix(unixStart, 0).Add(30 * time.Second) }
	req := &resource.StatusRequest{RequestID: "formae-ecs/update/1747526400/tU", NativeID: "pending|c|s"}
	res, err := s.statusPhaseB(context.Background(), req, resource.OperationUpdate, unixStart, "c", "s")
	assert.NoError(t, err)
	assert.Equal(t, resource.OperationStatusInProgress, res.ProgressResult.OperationStatus)
	assert.Contains(t, res.ProgressResult.StatusMessage, "waiting for new deployment to start")
}

func TestStatusPhaseB_UpdateNoNewDeployment_PastGrace_NoopUpdate_Success(t *testing.T) {
	// Update where primary.CreatedAt is well before unixStart-slack AND we are PAST
	// the updateGraceWindow. The implementation falls through to stability check
	// on the existing primary. Since it's stable + no TGs, it should report Success.
	unixStart := int64(1747526400)
	primaryStart := time.Unix(unixStart, 0).Add(-1 * time.Hour) // pre-existing, stable
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
	// 5 min after unixStart — past 60s grace
	s.now = func() time.Time { return time.Unix(unixStart, 0).Add(5 * time.Minute) }
	req := &resource.StatusRequest{RequestID: "formae-ecs/update/1747526400/tU", NativeID: "pending|c|s"}
	res, err := s.statusPhaseB(context.Background(), req, resource.OperationUpdate, unixStart, "c", "s")
	assert.NoError(t, err)
	assert.Equal(t, resource.OperationStatusSuccess, res.ProgressResult.OperationStatus,
		"no-op Update past grace with stable existing primary should report Success")
	assert.Equal(t, resource.OperationUpdate, res.ProgressResult.Operation)
}

func TestStatusPhaseB_TransientComposeError_ReturnsInProgress(t *testing.T) {
	ctx := context.Background()
	mockECS := &mockECSClient{}
	mockELB := &mockELBv2Client{}

	tgArn := "arn:aws:elasticloadbalancing:us-east-1:123:targetgroup/tg/abc"

	// Service is stable + TG healthy — passes existing Phase B gates.
	now := time.Now()
	mockECS.On("DescribeServices", ctx, mock.Anything).Return(&awsecs.DescribeServicesOutput{
		Services: []ecstypes.Service{{
			ServiceArn:   ptr("arn:aws:ecs:us-east-1:123:service/cluster1/svc1"),
			ClusterArn:   ptr("arn:aws:ecs:us-east-1:123:cluster/cluster1"),
			Status:       ptr("ACTIVE"),
			DesiredCount: 1,
			RunningCount: 1,
			Deployments: []ecstypes.Deployment{
				{
					Status:       ptr("PRIMARY"),
					RolloutState: ecstypes.DeploymentRolloutStateCompleted,
					CreatedAt:    &now,
				},
			},
			LoadBalancers: []ecstypes.LoadBalancer{
				{ContainerName: ptr("app"), ContainerPort: ptr(int32(443)), TargetGroupArn: ptr(tgArn)},
			},
		}},
	}, nil)

	// TG health passes.
	mockELB.On("DescribeTargetHealth", ctx, mock.Anything).Return(&awselbv2.DescribeTargetHealthOutput{
		TargetHealthDescriptions: []elbv2types.TargetHealthDescription{
			{TargetHealth: &elbv2types.TargetHealth{State: elbv2types.TargetHealthStateEnumHealthy}},
		},
	}, nil)

	// composeEndpoints' DescribeTargetGroups all 3 retries throttle.
	mockELB.On("DescribeTargetGroups", ctx, mock.Anything).
		Return((*awselbv2.DescribeTargetGroupsOutput)(nil), awsAPIErr("Throttling", "rate exceeded")).Times(3)

	svc := &Service{
		cfg:                nil,
		ecsClientFactory:   func(_ *config.Config) (ecsClient, error) { return mockECS, nil },
		elbv2ClientFactory: func(_ *config.Config) (elbv2Client, error) { return mockELB, nil },
		now:                time.Now,
		operationTimeout:   20 * time.Minute,
		finalReadGrace:     2 * time.Minute,
	}

	result, _ := svc.statusPhaseB(ctx, &resource.StatusRequest{
		NativeID:     "cluster1|svc1",
		ResourceType: "AWS::ECS::Service",
		RequestID:    "formae-ecs/create/1234567890/ccapi-tok",
	}, resource.OperationCreate, time.Now().Unix(), "cluster1", "svc1")

	assert.NotNil(t, result.ProgressResult)
	assert.Equal(t, resource.OperationStatusInProgress, result.ProgressResult.OperationStatus,
		"expected InProgress when composeEndpoints reports TransientError")
}

func TestStatusPhaseB_HappyPath_PopulatesEndpointsViaRead(t *testing.T) {
	ctx := context.Background()
	mockCCX := &mockCCXClient{}
	mockECS := &mockECSClient{}
	mockELB := &mockELBv2Client{}

	tgArn := "arn:aws:elasticloadbalancing:us-east-1:123:targetgroup/tg/abc"
	albArn := "arn:aws:elasticloadbalancing:us-east-1:123:loadbalancer/app/a/1"
	serviceArn := "arn:aws:ecs:us-east-1:123:service/cluster1/svc1"

	mockECS.On("DescribeServices", ctx, mock.Anything).Return(&awsecs.DescribeServicesOutput{
		Services: []ecstypes.Service{{
			ServiceArn:   ptr(serviceArn),
			ClusterArn:   ptr("arn:aws:ecs:us-east-1:123:cluster/cluster1"),
			Status:       ptr("ACTIVE"),
			DesiredCount: 1,
			RunningCount: 1,
			Deployments: []ecstypes.Deployment{{
				Status:       ptr("PRIMARY"),
				RolloutState: ecstypes.DeploymentRolloutStateCompleted,
			}},
			LoadBalancers: []ecstypes.LoadBalancer{
				{ContainerName: ptr("app"), ContainerPort: ptr(int32(443)), TargetGroupArn: ptr(tgArn)},
			},
		}},
	}, nil)

	mockELB.On("DescribeTargetHealth", ctx, mock.Anything).Return(&awselbv2.DescribeTargetHealthOutput{
		TargetHealthDescriptions: []elbv2types.TargetHealthDescription{
			{TargetHealth: &elbv2types.TargetHealth{State: elbv2types.TargetHealthStateEnumHealthy}},
		},
	}, nil)
	mockELB.On("DescribeTargetGroups", ctx, mock.Anything).Return(&awselbv2.DescribeTargetGroupsOutput{
		TargetGroups: []elbv2types.TargetGroup{{TargetGroupArn: ptr(tgArn), LoadBalancerArns: []string{albArn}}},
	}, nil)
	mockELB.On("DescribeLoadBalancers", ctx, mock.Anything).Return(&awselbv2.DescribeLoadBalancersOutput{
		LoadBalancers: []elbv2types.LoadBalancer{
			{LoadBalancerArn: ptr(albArn), DNSName: ptr("dns-1"), Type: elbv2types.LoadBalancerTypeEnumApplication},
		},
	}, nil)
	mockELB.On("DescribeListeners", ctx, mock.Anything).Return(&awselbv2.DescribeListenersOutput{
		Listeners: []elbv2types.Listener{{
			ListenerArn:    ptr("l1"),
			Port:           ptr(int32(443)),
			Protocol:       elbv2types.ProtocolEnumHttps,
			DefaultActions: []elbv2types.Action{{Type: elbv2types.ActionTypeEnumForward, TargetGroupArn: ptr(tgArn)}},
		}},
	}, nil)

	// CCAPI Read returns the persisted service properties; the readWithClient
	// post-processor (which uses elbv2ClientFactory) injects the Endpoints map.
	mockCCX.On("ReadResource", ctx, mock.Anything).Return(&resource.ReadResult{
		ResourceType: "AWS::ECS::Service",
		Properties: `{
			"ServiceArn": "` + serviceArn + `",
			"Cluster": "arn:aws:ecs:us-east-1:123:cluster/cluster1",
			"LoadBalancers": [{"ContainerName":"app","ContainerPort":443,"TargetGroupArn":"` + tgArn + `"}]
		}`,
	}, nil)

	svc := &Service{
		cfg:                nil,
		ccxClientFactory:   func(_ *config.Config) (ccxClient, error) { return mockCCX, nil },
		ecsClientFactory:   func(_ *config.Config) (ecsClient, error) { return mockECS, nil },
		elbv2ClientFactory: func(_ *config.Config) (elbv2Client, error) { return mockELB, nil },
		now:                time.Now,
		operationTimeout:   20 * time.Minute,
		finalReadGrace:     2 * time.Minute,
	}

	result, _ := svc.statusPhaseB(ctx, &resource.StatusRequest{
		NativeID:     "cluster1|svc1",
		ResourceType: "AWS::ECS::Service",
		RequestID:    "formae-ecs/create/1234567890/ccapi-tok",
	}, resource.OperationCreate, time.Now().Unix(), "cluster1", "svc1")

	assert.NotNil(t, result.ProgressResult)
	assert.Equal(t, resource.OperationStatusSuccess, result.ProgressResult.OperationStatus)
	assert.Contains(t, string(result.ProgressResult.ResourceProperties), `"Endpoints":{"app:443":"https://dns-1:443"}`)
}

func TestStatusPhaseB_TransientClearsAcrossPolls(t *testing.T) {
	ctx := context.Background()
	mockCCX := &mockCCXClient{}
	mockECS := &mockECSClient{}
	mockELB := &mockELBv2Client{}

	tgArn := "arn:aws:elasticloadbalancing:us-east-1:123:targetgroup/tg/abc"
	albArn := "arn:aws:elasticloadbalancing:us-east-1:123:loadbalancer/app/a/1"

	mockECS.On("DescribeServices", ctx, mock.Anything).Return(&awsecs.DescribeServicesOutput{
		Services: []ecstypes.Service{{
			ServiceArn:   ptr("arn:aws:ecs:us-east-1:123:service/cluster1/svc1"),
			ClusterArn:   ptr("arn:aws:ecs:us-east-1:123:cluster/cluster1"),
			Status:       ptr("ACTIVE"),
			DesiredCount: 1,
			RunningCount: 1,
			Deployments: []ecstypes.Deployment{{
				Status:       ptr("PRIMARY"),
				RolloutState: ecstypes.DeploymentRolloutStateCompleted,
			}},
			LoadBalancers: []ecstypes.LoadBalancer{
				{ContainerName: ptr("app"), ContainerPort: ptr(int32(443)), TargetGroupArn: ptr(tgArn)},
			},
		}},
	}, nil)
	mockELB.On("DescribeTargetHealth", ctx, mock.Anything).Return(&awselbv2.DescribeTargetHealthOutput{
		TargetHealthDescriptions: []elbv2types.TargetHealthDescription{
			{TargetHealth: &elbv2types.TargetHealth{State: elbv2types.TargetHealthStateEnumHealthy}},
		},
	}, nil)

	// First poll: DescribeTargetGroups throttles all 3 retries.
	mockELB.On("DescribeTargetGroups", ctx, mock.Anything).
		Return((*awselbv2.DescribeTargetGroupsOutput)(nil), awsAPIErr("Throttling", "rate exceeded")).Times(3)

	// Second poll: success for both the endpoint-composition gate and the subsequent
	// Read (readWithClient also calls composeEndpoints internally), so register twice.
	mockELB.On("DescribeTargetGroups", ctx, mock.Anything).Return(&awselbv2.DescribeTargetGroupsOutput{
		TargetGroups: []elbv2types.TargetGroup{{TargetGroupArn: ptr(tgArn), LoadBalancerArns: []string{albArn}}},
	}, nil).Times(2)
	mockELB.On("DescribeLoadBalancers", ctx, mock.Anything).Return(&awselbv2.DescribeLoadBalancersOutput{
		LoadBalancers: []elbv2types.LoadBalancer{
			{LoadBalancerArn: ptr(albArn), DNSName: ptr("dns-1"), Type: elbv2types.LoadBalancerTypeEnumApplication},
		},
	}, nil)
	mockELB.On("DescribeListeners", ctx, mock.Anything).Return(&awselbv2.DescribeListenersOutput{
		Listeners: []elbv2types.Listener{{
			ListenerArn:    ptr("l1"),
			Port:           ptr(int32(443)),
			Protocol:       elbv2types.ProtocolEnumHttps,
			DefaultActions: []elbv2types.Action{{Type: elbv2types.ActionTypeEnumForward, TargetGroupArn: ptr(tgArn)}},
		}},
	}, nil)
	mockCCX.On("ReadResource", ctx, mock.Anything).Return(&resource.ReadResult{
		ResourceType: "AWS::ECS::Service",
		Properties: `{
			"ServiceArn": "arn:aws:ecs:us-east-1:123:service/cluster1/svc1",
			"Cluster": "arn:aws:ecs:us-east-1:123:cluster/cluster1",
			"LoadBalancers": [{"ContainerName":"app","ContainerPort":443,"TargetGroupArn":"` + tgArn + `"}]
		}`,
	}, nil)

	svc := &Service{
		cfg:                nil,
		ccxClientFactory:   func(_ *config.Config) (ccxClient, error) { return mockCCX, nil },
		ecsClientFactory:   func(_ *config.Config) (ecsClient, error) { return mockECS, nil },
		elbv2ClientFactory: func(_ *config.Config) (elbv2Client, error) { return mockELB, nil },
		now:                time.Now,
		operationTimeout:   20 * time.Minute,
		finalReadGrace:     2 * time.Minute,
	}

	// Poll 1: transient → InProgress.
	r1, _ := svc.statusPhaseB(ctx, &resource.StatusRequest{
		NativeID: "cluster1|svc1", ResourceType: "AWS::ECS::Service",
		RequestID: "formae-ecs/create/1234567890/ccapi-tok",
	}, resource.OperationCreate, time.Now().Unix(), "cluster1", "svc1")
	assert.Equal(t, resource.OperationStatusInProgress, r1.ProgressResult.OperationStatus)

	// Poll 2: composeEndpoints succeeds → Success.
	r2, _ := svc.statusPhaseB(ctx, &resource.StatusRequest{
		NativeID: "cluster1|svc1", ResourceType: "AWS::ECS::Service",
		RequestID: "formae-ecs/create/1234567890/ccapi-tok",
	}, resource.OperationCreate, time.Now().Unix(), "cluster1", "svc1")
	assert.Equal(t, resource.OperationStatusSuccess, r2.ProgressResult.OperationStatus)
	assert.Contains(t, string(r2.ProgressResult.ResourceProperties), `"Endpoints":{"app:443":"https://dns-1:443"}`)
}
