// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package ecs

import (
	"fmt"
	"time"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
)

// inProgressOrTimeout returns an InProgress ProgressResult within the operation
// budget, or escalates to terminal Failure (GeneralServiceException) past it.
// EVERY InProgress site in Status routes through this helper EXCEPT post-stability
// Read retries (see inProgressOrFinalReadTimeout).
func (s *Service) inProgressOrTimeout(op resource.Operation, req *resource.StatusRequest,
	unixStart int64, msg string) *resource.StatusResult {
	return s.boundedInProgress(op, req, unixStart, s.operationTimeout, msg)
}

// inProgressOrFinalReadTimeout uses operationTimeout+finalReadGrace as the cutoff.
// Only finalSuccess calls this — once stability has been observed, the Read gets
// extra wall-clock time so a brief post-stability NotFound/empty at the 20m
// boundary doesn't false-fail an actually-healthy service.
func (s *Service) inProgressOrFinalReadTimeout(op resource.Operation, req *resource.StatusRequest,
	unixStart int64, msg string) *resource.StatusResult {
	return s.boundedInProgress(op, req, unixStart, s.operationTimeout+s.finalReadGrace, msg)
}

func (s *Service) boundedInProgress(op resource.Operation, req *resource.StatusRequest,
	unixStart int64, budget time.Duration, msg string) *resource.StatusResult {
	if s.now().Sub(time.Unix(unixStart, 0)) > budget {
		return &resource.StatusResult{
			ProgressResult: terminalFailurePR(op, req.NativeID, "",
				resource.OperationErrorCodeGeneralServiceException,
				fmt.Sprintf("ECS operation exceeded %s budget: %s", budget, msg)),
		}
	}
	return &resource.StatusResult{
		ProgressResult: &resource.ProgressResult{
			Operation:       op,
			OperationStatus: resource.OperationStatusInProgress,
			RequestID:       req.RequestID,
			NativeID:        req.NativeID,
			StatusMessage:   msg,
		},
	}
}

// classifyForStatus maps a Go/AWS error to either an InProgress (within timeout)
// or terminal Failure ProgressResult. Used at every Status-mode call site.
func (s *Service) classifyForStatus(err error, op resource.Operation, req *resource.StatusRequest,
	unixStart int64, contextMsg string) *resource.StatusResult {
	code, retryable := classifyAWSError(err)
	if retryable {
		return s.inProgressOrTimeout(op, req, unixStart, contextMsg+": "+err.Error())
	}
	return &resource.StatusResult{
		ProgressResult: terminalFailurePR(op, req.NativeID, req.RequestID, code,
			contextMsg+": "+err.Error()),
	}
}
