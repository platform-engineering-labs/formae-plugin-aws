// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package ecs

import (
	"errors"
	"net"
	"testing"

	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
)

type fakeAPIError struct{ code, msg string }

func (e *fakeAPIError) Error() string                 { return e.code + ": " + e.msg }
func (e *fakeAPIError) ErrorCode() string             { return e.code }
func (e *fakeAPIError) ErrorMessage() string          { return e.msg }
func (e *fakeAPIError) ErrorFault() smithy.ErrorFault { return smithy.FaultServer }

func TestClassifyAWSError_Throttling(t *testing.T) {
	code, retryable := classifyAWSError(&fakeAPIError{code: "Throttling", msg: "Rate exceeded"})
	assert.True(t, retryable)
	assert.Equal(t, resource.OperationErrorCodeThrottling, code)
}

func TestClassifyAWSError_AccessDenied(t *testing.T) {
	code, retryable := classifyAWSError(&fakeAPIError{code: "AccessDenied", msg: "denied"})
	assert.False(t, retryable)
	assert.Equal(t, resource.OperationErrorCodeAccessDenied, code)
}

func TestClassifyAWSError_ExpiredToken(t *testing.T) {
	code, retryable := classifyAWSError(&fakeAPIError{code: "ExpiredToken", msg: "expired"})
	assert.False(t, retryable)
	assert.Equal(t, resource.OperationErrorCodeInvalidCredentials, code)
}

func TestClassifyAWSError_ValidationException(t *testing.T) {
	code, retryable := classifyAWSError(&fakeAPIError{code: "ValidationException", msg: "bad input"})
	assert.False(t, retryable)
	assert.Equal(t, resource.OperationErrorCodeInvalidRequest, code)
}

func TestClassifyAWSError_UnknownCode_TerminalGeneralServiceException(t *testing.T) {
	code, retryable := classifyAWSError(&fakeAPIError{code: "SomethingBrandNew", msg: "?"})
	assert.False(t, retryable)
	assert.Equal(t, resource.OperationErrorCodeGeneralServiceException, code)
}

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

var _ net.Error = timeoutErr{}

func TestClassifyAWSError_NetworkTimeout_Retryable(t *testing.T) {
	code, retryable := classifyAWSError(timeoutErr{})
	assert.True(t, retryable)
	assert.Equal(t, resource.OperationErrorCodeNetworkFailure, code)
}

func TestClassifyAWSError_PlainError_TerminalGeneralServiceException(t *testing.T) {
	code, retryable := classifyAWSError(errors.New("some random non-AWS error"))
	assert.False(t, retryable)
	assert.Equal(t, resource.OperationErrorCodeGeneralServiceException, code)
}

func TestTerminalFailurePR(t *testing.T) {
	pr := terminalFailurePR(resource.OperationCreate, "nid", "rid",
		resource.OperationErrorCodeAccessDenied, "denied here")
	assert.Equal(t, resource.OperationCreate, pr.Operation)
	assert.Equal(t, resource.OperationStatusFailure, pr.OperationStatus)
	assert.Equal(t, "nid", pr.NativeID)
	assert.Equal(t, "rid", pr.RequestID)
	assert.Equal(t, resource.OperationErrorCodeAccessDenied, pr.ErrorCode)
	assert.Contains(t, pr.StatusMessage, "denied here")
}

func TestClassifyReadResultForFinal_Success(t *testing.T) {
	rr := &resource.ReadResult{Properties: `{"k":"v"}`}
	code, retryable, ok := classifyReadResultForFinal(rr, nil)
	assert.True(t, ok)
	assert.False(t, retryable)
	assert.Equal(t, resource.OperationErrorCode(""), code)
}

func TestClassifyReadResultForFinal_NotFound_Retryable(t *testing.T) {
	rr := &resource.ReadResult{ErrorCode: resource.OperationErrorCodeNotFound}
	_, retryable, ok := classifyReadResultForFinal(rr, nil)
	assert.False(t, ok)
	assert.True(t, retryable)
}

func TestClassifyReadResultForFinal_AccessDenied_Terminal(t *testing.T) {
	rr := &resource.ReadResult{ErrorCode: resource.OperationErrorCodeAccessDenied}
	code, retryable, ok := classifyReadResultForFinal(rr, nil)
	assert.False(t, ok)
	assert.False(t, retryable)
	assert.Equal(t, resource.OperationErrorCodeAccessDenied, code)
}

func TestClassifyReadResultForFinal_EmptyProperties_Retryable(t *testing.T) {
	rr := &resource.ReadResult{ErrorCode: "", Properties: ""}
	_, retryable, ok := classifyReadResultForFinal(rr, nil)
	assert.False(t, ok)
	assert.True(t, retryable)
}

func TestClassifyReadResultForFinal_GoError_GoesThroughAWSClassifier(t *testing.T) {
	_, retryable, ok := classifyReadResultForFinal(nil, &fakeAPIError{code: "Throttling"})
	assert.False(t, ok)
	assert.True(t, retryable)
}
