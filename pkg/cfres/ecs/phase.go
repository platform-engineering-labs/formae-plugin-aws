// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package ecs

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsecs "github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

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

	ecsCli, err := s.ecsClientFactory(s.cfg)
	if err != nil {
		return s.classifyForStatus(err, op, req, unixStart, "build ECS client"), nil
	}
	descOut, err := ecsCli.DescribeServices(ctx, &awsecs.DescribeServicesInput{
		Cluster:  &cluster,
		Services: []string{service},
	})
	if err != nil {
		return s.classifyForStatus(err, op, req, unixStart, "DescribeServices"), nil
	}

	// INACTIVE failure → fast OOB-delete terminal.
	if hasInactiveFailure(descOut) {
		return &resource.StatusResult{
			ProgressResult: terminalFailurePR(op, req.NativeID, "",
				resource.OperationErrorCodeGeneralServiceException,
				fmt.Sprintf("ECS service %s in cluster %s reports INACTIVE — deleted out-of-band",
					service, cluster)),
		}, nil
	}
	if len(descOut.Services) == 0 {
		return s.inProgressOrTimeout(op, req, unixStart,
			"service not yet visible in DescribeServices (may be lag or out-of-band deletion)"), nil
	}
	svc := descOut.Services[0]
	if aws.ToString(svc.Status) == "INACTIVE" {
		return &resource.StatusResult{
			ProgressResult: terminalFailurePR(op, req.NativeID, "",
				resource.OperationErrorCodeGeneralServiceException,
				fmt.Sprintf("ECS service %s is INACTIVE — deleted out-of-band", service)),
		}, nil
	}

	primary := findPrimaryDeployment(svc.Deployments)
	if primary == nil {
		return s.inProgressOrTimeout(op, req, unixStart, "no PRIMARY deployment yet"), nil
	}

	// Update no-new-deployment grace window.
	if op == resource.OperationUpdate {
		opStart := time.Unix(unixStart, 0)
		if primary.CreatedAt == nil || primary.CreatedAt.Before(opStart.Add(-primaryCreatedSlack)) {
			if s.now().Sub(opStart) < updateGraceWindow {
				return s.inProgressOrTimeout(op, req, unixStart,
					"waiting for new deployment to start"), nil
			}
			// Past grace — treat as no-op Update; check existing primary's stability.
		}
	}

	// Explicit FAILED before stability check.
	if primary.RolloutState == ecstypes.DeploymentRolloutStateFailed {
		return &resource.StatusResult{
			ProgressResult: terminalFailurePR(op, req.NativeID, "",
				resource.OperationErrorCodeGeneralServiceException,
				"deployment failed: "+aws.ToString(primary.RolloutStateReason)),
		}, nil
	}

	if !isPhaseBStable(primary, svc) {
		return s.inProgressOrTimeout(op, req, unixStart,
			fmt.Sprintf("rollout %s: %d/%d tasks running",
				primary.RolloutState, svc.RunningCount, svc.DesiredCount)), nil
	}

	// TG health + finalSuccess — Task 15/16.
	return s.finalSuccess(ctx, req, op, unixStart, svc)
}

func findPrimaryDeployment(deployments []ecstypes.Deployment) *ecstypes.Deployment {
	for i := range deployments {
		if aws.ToString(deployments[i].Status) == "PRIMARY" {
			return &deployments[i]
		}
	}
	return nil
}

func isPhaseBStable(primary *ecstypes.Deployment, svc ecstypes.Service) bool {
	return primary.RolloutState == ecstypes.DeploymentRolloutStateCompleted &&
		svc.RunningCount == svc.DesiredCount
}

// Placeholder for Task 15/16. finalSuccess takes the unixStart so it can pass
// it to inProgressOrFinalReadTimeout in Task 16.
func (s *Service) finalSuccess(ctx context.Context, req *resource.StatusRequest,
	op resource.Operation, unixStart int64, svc ecstypes.Service) (*resource.StatusResult, error) {
	panic("finalSuccess: TG health gate + Read in Task 15/16")
}
