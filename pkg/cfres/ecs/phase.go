// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package ecs

import (
	"context"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
)

// checkPhaseA polls CCAPI and returns either (resultToReturn, false) when
// Phase A is still in flight or failed, or (nil, true) when CCAPI has accepted
// the operation and Phase B should run.
func (s *Service) checkPhaseA(ctx context.Context, req *resource.StatusRequest,
	op resource.Operation, unixStart int64, ccapiToken string) (*resource.StatusResult, bool) {
	cli, err := s.ccxClientFactory(s.cfg)
	if err != nil {
		return s.classifyForStatus(err, op, req, unixStart, "build CCAPI client"), false
	}
	phaseAReq := *req
	phaseAReq.RequestID = ccapiToken
	res, err := cli.StatusResource(ctx, &phaseAReq, s.Read)
	if err != nil {
		return s.classifyForStatus(err, op, req, unixStart, "ccx.StatusResource"), false
	}
	if res.ProgressResult == nil {
		return s.inProgressOrTimeout(op, req, unixStart, "CCAPI status returned no progress"), false
	}
	switch res.ProgressResult.OperationStatus {
	case resource.OperationStatusSuccess:
		return nil, true
	case resource.OperationStatusInProgress:
		return s.inProgressOrTimeout(op, req, unixStart, res.ProgressResult.StatusMessage), false
	case resource.OperationStatusFailure:
		// Update + NotFound = OOB-delete pre-CCAPI-Success. NotFound is recoverable
		// per the SDK; surface as terminal GeneralServiceException to avoid retry loops.
		if op == resource.OperationUpdate && res.ProgressResult.ErrorCode == resource.OperationErrorCodeNotFound {
			return &resource.StatusResult{
				ProgressResult: terminalFailurePR(op, req.NativeID, req.RequestID,
					resource.OperationErrorCodeGeneralServiceException,
					"ECS service deleted out-of-band during Update (CCAPI reported NotFound): "+
						res.ProgressResult.StatusMessage),
			}, false
		}
		// Other failures: propagate verbatim with composite identity restored.
		res.ProgressResult.RequestID = req.RequestID
		res.ProgressResult.Operation = op
		return res, false
	default:
		return s.inProgressOrTimeout(op, req, unixStart,
			"unexpected CCAPI status: "+string(res.ProgressResult.OperationStatus)), false
	}
}

func (s *Service) statusPhaseB(ctx context.Context, req *resource.StatusRequest,
	op resource.Operation, unixStart int64, cluster, service string) (*resource.StatusResult, error) {
	panic("statusPhaseB: implemented in Task 14")
}
