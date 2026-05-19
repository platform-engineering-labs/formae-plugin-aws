// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package ecs

import (
	"errors"
	"net"

	"github.com/aws/smithy-go"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
)

// classifyAWSError maps an error from an AWS SDK call (or our own ccx layer) to
// a (errorCode, retryable) verdict. See design Q5 for the full mapping.
//
// Discipline: unknown errors default to TERMINAL (GeneralServiceException) — fail
// loudly rather than poll forever on something we don't recognize. The 20-minute
// operation timeout (via inProgressOrTimeout) means even retryable verdicts can
// escalate to terminal Failure if they persist.
func classifyAWSError(err error) (resource.OperationErrorCode, bool) {
	if err == nil {
		return "", false
	}
	var ae smithy.APIError
	if errors.As(err, &ae) {
		switch ae.ErrorCode() {
		// Retryable
		case "Throttling", "ThrottlingException", "RequestLimitExceeded",
			"TooManyRequestsException":
			return resource.OperationErrorCodeThrottling, true
		case "ServiceUnavailable", "ServiceUnavailableException":
			return resource.OperationErrorCodeServiceInternalError, true
		case "InternalFailure", "InternalServerError":
			return resource.OperationErrorCodeServiceInternalError, true
		// Terminal — auth/permissions
		case "AccessDenied", "AccessDeniedException", "UnauthorizedOperation":
			return resource.OperationErrorCodeAccessDenied, false
		case "ExpiredToken", "InvalidClientTokenId",
			"InvalidSignatureException", "SignatureDoesNotMatch":
			return resource.OperationErrorCodeInvalidCredentials, false
		// Terminal — validation
		case "ValidationException", "InvalidParameterException",
			"InvalidParameterValueException", "InvalidInputException":
			return resource.OperationErrorCodeInvalidRequest, false
		}
	}
	// Network errors with .Timeout() == true → retryable
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return resource.OperationErrorCodeNetworkFailure, true
	}
	return resource.OperationErrorCodeGeneralServiceException, false
}

// classifyForEntry is used by Create/Update entry. Retryable AWS errors return
// a recoverable ErrorCode so the operator's handlePluginResult schedules a CRUD
// retry of the entire operation. Terminal errors get a non-recoverable code so
// the operator surfaces the failure immediately.
func classifyForEntry(err error, op resource.Operation, nativeID, contextMsg string) *resource.ProgressResult {
	code, retryable := classifyAWSError(err)
	if retryable {
		// Map our verdict to a recoverable code the SDK's recoverableErrorCodes
		// table actually recognises. (Throttling, NetworkFailure, ServiceInternalError
		// are all in the table — see pkg/plugin/resource/resource.go:172-181.)
		switch code {
		case resource.OperationErrorCodeThrottling,
			resource.OperationErrorCodeNetworkFailure,
			resource.OperationErrorCodeServiceInternalError:
			// already recoverable
		default:
			code = resource.OperationErrorCodeThrottling
		}
	}
	return terminalFailurePR(op, nativeID, "", code, contextMsg+": "+err.Error())
}

// terminalFailurePR builds a populated Failure ProgressResult.
func terminalFailurePR(op resource.Operation, nativeID, requestID string,
	code resource.OperationErrorCode, msg string) *resource.ProgressResult {
	return &resource.ProgressResult{
		Operation:       op,
		OperationStatus: resource.OperationStatusFailure,
		NativeID:        nativeID,
		RequestID:       requestID,
		ErrorCode:       code,
		StatusMessage:   msg,
	}
}

// classifyReadResultForFinal classifies a post-stability Read outcome. Handles
// both Go errors and ReadResult.ErrorCode (ccx.ReadResource maps CCAPI errors
// into ErrorCode without returning a Go error — see pkg/ccx/client.go:294-303).
//
// Returns:
//   - ok=true:                 Read returned non-empty Properties → caller emits Success
//   - ok=false, retryable=true: route through inProgressOrFinalReadTimeout (grace-bounded)
//   - ok=false, retryable=false: terminal Failure with `code`
func classifyReadResultForFinal(rr *resource.ReadResult, readErr error) (resource.OperationErrorCode, bool, bool) {
	if readErr != nil {
		code, retryable := classifyAWSError(readErr)
		return code, retryable, false
	}
	if rr == nil {
		return resource.OperationErrorCodeGeneralServiceException, false, false
	}
	switch rr.ErrorCode {
	case "":
		if rr.Properties == "" {
			return "", true, false // retryable: empty body without error
		}
		return "", false, true // success
	case resource.OperationErrorCodeNotFound,
		resource.OperationErrorCodeThrottling,
		resource.OperationErrorCodeServiceInternalError,
		resource.OperationErrorCodeServiceTimeout,
		resource.OperationErrorCodeNetworkFailure,
		resource.OperationErrorCodeInternalFailure:
		return rr.ErrorCode, true, false
	case resource.OperationErrorCodeAccessDenied,
		resource.OperationErrorCodeInvalidCredentials,
		resource.OperationErrorCodeInvalidRequest:
		return rr.ErrorCode, false, false
	default:
		return resource.OperationErrorCodeGeneralServiceException, false, false
	}
}
