// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package ecs

import (
	"context"
	"testing"

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

// Silence unused imports — these types will be used by Tasks 14/15 tests.
var _ = aws.String
var _ = awsecs.DescribeServicesInput{}
var _ ecstypes.LaunchType
var _ = awselbv2.DescribeTargetHealthInput{}
var _ elbv2types.TargetHealthStateEnum
