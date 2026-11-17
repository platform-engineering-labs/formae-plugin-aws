// © 2026 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package helper

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	cctypes "github.com/aws/aws-sdk-go-v2/service/cloudcontrol/types"
	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
)

func ccOpError(underlying error) error {
	return &smithy.OperationError{
		ServiceID:     "CloudControl",
		OperationName: "GetResource",
		Err:           underlying,
	}
}

// TestHandleCloudControlError_OnlyResourceNotFoundIsNotFound guards the one
// mapping sync relies on to tombstone a resource: a sync read that returns
// NotFound deletes the resource from formae's inventory, so only CloudControl's
// own ResourceNotFoundException may produce it.
func TestHandleCloudControlError_OnlyResourceNotFoundIsNotFound(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want cctypes.HandlerErrorCode
	}{
		{"resource not found", &cctypes.ResourceNotFoundException{Message: aws.String("not found")}, cctypes.HandlerErrorCodeNotFound},
		{"type not found", &cctypes.TypeNotFoundException{Message: aws.String("type not found")}, cctypes.HandlerErrorCodeInvalidRequest},
		{"request token not found", &cctypes.RequestTokenNotFoundException{Message: aws.String("token not found")}, cctypes.HandlerErrorCodeInvalidRequest},
		{"throttling", &cctypes.ThrottlingException{Message: aws.String("rate exceeded")}, cctypes.HandlerErrorCodeThrottling},
		{"network failure", &cctypes.NetworkFailureException{Message: aws.String("network")}, cctypes.HandlerErrorCodeNetworkFailure},
		{"service internal error", &cctypes.ServiceInternalErrorException{Message: aws.String("internal")}, cctypes.HandlerErrorCodeServiceInternalError},
		{"handler internal failure", &cctypes.HandlerInternalFailureException{Message: aws.String("handler")}, cctypes.HandlerErrorCodeInternalFailure},
		{"general service", &cctypes.GeneralServiceException{Message: aws.String("general")}, cctypes.HandlerErrorCodeGeneralServiceException},
		{"invalid credentials", &cctypes.InvalidCredentialsException{Message: aws.String("creds")}, cctypes.HandlerErrorCodeInvalidCredentials},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, ok := HandleCloudControlError(ccOpError(tt.err))
			assert.True(t, ok)
			assert.Equal(t, tt.want, code)
		})
	}
}

// TestHandleCloudControlError_TransportErrorsAreNotClassified covers errors
// that never reached CloudControl's handler: they must surface as Go errors,
// never as an error code (and so never as NotFound).
func TestHandleCloudControlError_TransportErrorsAreNotClassified(t *testing.T) {
	for _, err := range []error{
		ccOpError(context.DeadlineExceeded),
		ccOpError(errors.New("dial tcp: i/o timeout")),
		context.Canceled,
	} {
		code, ok := HandleCloudControlError(err)
		assert.False(t, ok, "%v", err)
		assert.Empty(t, code)
	}
}
