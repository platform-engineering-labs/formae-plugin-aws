// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package ecs

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
)

func newServiceWithClock(unixStart, nowUnix int64, opTimeout, grace time.Duration) *Service {
	return &Service{
		now:              func() time.Time { return time.Unix(nowUnix, 0) },
		operationTimeout: opTimeout,
		finalReadGrace:   grace,
	}
}

func TestInProgressOrTimeout_WithinBudget(t *testing.T) {
	s := newServiceWithClock(0, 60, 20*time.Minute, 2*time.Minute)
	req := &resource.StatusRequest{RequestID: "rid", NativeID: "nid"}
	res := s.inProgressOrTimeout(resource.OperationCreate, req, 0, "rolling out")
	assert.Equal(t, resource.OperationStatusInProgress, res.ProgressResult.OperationStatus)
	assert.Equal(t, "rid", res.ProgressResult.RequestID)
	assert.Equal(t, "nid", res.ProgressResult.NativeID)
	assert.Equal(t, "rolling out", res.ProgressResult.StatusMessage)
}

func TestInProgressOrTimeout_PastBudget_Escalates(t *testing.T) {
	// unixStart=0, now=20m + 1s → past 20m budget
	s := newServiceWithClock(0, 20*60+1, 20*time.Minute, 2*time.Minute)
	req := &resource.StatusRequest{RequestID: "rid", NativeID: "nid"}
	res := s.inProgressOrTimeout(resource.OperationCreate, req, 0, "still IN_PROGRESS")
	assert.Equal(t, resource.OperationStatusFailure, res.ProgressResult.OperationStatus)
	assert.Equal(t, resource.OperationErrorCodeGeneralServiceException, res.ProgressResult.ErrorCode)
	assert.Contains(t, res.ProgressResult.StatusMessage, "exceeded")
}

func TestInProgressOrFinalReadTimeout_GraceExtendsBudget(t *testing.T) {
	// unixStart=0, now=20m+30s. Inside operationTimeout+grace=22m.
	s := newServiceWithClock(0, 20*60+30, 20*time.Minute, 2*time.Minute)
	req := &resource.StatusRequest{RequestID: "rid", NativeID: "nid"}
	res := s.inProgressOrFinalReadTimeout(resource.OperationCreate, req, 0, "post-stability Read NotFound")
	assert.Equal(t, resource.OperationStatusInProgress, res.ProgressResult.OperationStatus,
		"stable services within the 22m total budget should still get InProgress")
}

func TestInProgressOrFinalReadTimeout_PastExtendedBudget_Escalates(t *testing.T) {
	// 22m+1s — past operationTimeout+grace
	s := newServiceWithClock(0, 22*60+1, 20*time.Minute, 2*time.Minute)
	req := &resource.StatusRequest{RequestID: "rid", NativeID: "nid"}
	res := s.inProgressOrFinalReadTimeout(resource.OperationCreate, req, 0, "still failing")
	assert.Equal(t, resource.OperationStatusFailure, res.ProgressResult.OperationStatus)
	assert.Equal(t, resource.OperationErrorCodeGeneralServiceException, res.ProgressResult.ErrorCode)
}

func TestClassifyForStatus_Retryable_InProgress(t *testing.T) {
	s := newServiceWithClock(0, 60, 20*time.Minute, 2*time.Minute)
	req := &resource.StatusRequest{RequestID: "rid", NativeID: "nid"}
	res := s.classifyForStatus(&fakeAPIError{code: "Throttling"}, resource.OperationCreate, req, 0, "DescribeServices")
	assert.Equal(t, resource.OperationStatusInProgress, res.ProgressResult.OperationStatus)
	assert.Contains(t, res.ProgressResult.StatusMessage, "DescribeServices")
}

func TestClassifyForStatus_Terminal_Failure(t *testing.T) {
	s := newServiceWithClock(0, 60, 20*time.Minute, 2*time.Minute)
	req := &resource.StatusRequest{RequestID: "rid", NativeID: "nid"}
	res := s.classifyForStatus(&fakeAPIError{code: "AccessDenied"}, resource.OperationCreate, req, 0, "DescribeServices")
	assert.Equal(t, resource.OperationStatusFailure, res.ProgressResult.OperationStatus)
	assert.Equal(t, resource.OperationErrorCodeAccessDenied, res.ProgressResult.ErrorCode)
}

func TestClassifyForStatus_RetryablePastTimeout_Escalates(t *testing.T) {
	// Throttling past the 20m budget → terminal escalation.
	s := newServiceWithClock(0, 20*60+1, 20*time.Minute, 2*time.Minute)
	req := &resource.StatusRequest{RequestID: "rid", NativeID: "nid"}
	res := s.classifyForStatus(errors.New("net: i/o timeout"), resource.OperationCreate, req, 0, "DescribeServices")
	// errors.New isn't a net.Error, so this lands as GeneralServiceException terminal.
	// For the timeout-escalation behavior, fakeAPIError{Throttling} hits the same path:
	res2 := s.classifyForStatus(&fakeAPIError{code: "Throttling"}, resource.OperationCreate, req, 0, "DescribeServices")
	assert.Equal(t, resource.OperationStatusFailure, res2.ProgressResult.OperationStatus)
	assert.Equal(t, resource.OperationErrorCodeGeneralServiceException, res2.ProgressResult.ErrorCode)
	_ = res
}
