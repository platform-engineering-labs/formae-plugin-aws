// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package ecs

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsecs "github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	awselbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbv2types "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"

	"github.com/platform-engineering-labs/formae/pkg/plugin"
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

// needsTargetHealth returns true iff desiredCount > 0 AND at least one LB entry
// has a non-empty TargetGroupArn. Classic-ELB attachments (loadBalancerName only)
// are skipped — we have no equivalent health signal and ECS's stability already
// guards the deploy.
func needsTargetHealth(svc ecstypes.Service) bool {
	if svc.DesiredCount <= 0 {
		return false
	}
	for _, lb := range svc.LoadBalancers {
		if aws.ToString(lb.TargetGroupArn) != "" {
			return true
		}
	}
	return false
}

// checkAllTGsHealthy walks loadBalancers, skipping classic-ELB entries (empty
// TargetGroupArn), and calls DescribeTargetHealth on each TG. Returns:
//   - allHealthy: true if every TG has ≥1 healthy target
//   - msg:        a descriptive "<tg> <healthy>/<total>" string for status reporting
//   - err:        any Go error from the SDK call (caller routes through classifier)
func checkAllTGsHealthy(ctx context.Context, elbCli elbv2Client,
	loadBalancers []ecstypes.LoadBalancer) (bool, string, error) {
	var msgs []string
	allHealthy := true
	for _, lb := range loadBalancers {
		arn := aws.ToString(lb.TargetGroupArn)
		if arn == "" {
			continue
		}
		out, err := elbCli.DescribeTargetHealth(ctx, &awselbv2.DescribeTargetHealthInput{
			TargetGroupArn: &arn,
		})
		if err != nil {
			return false, "", err
		}
		healthy := 0
		for _, t := range out.TargetHealthDescriptions {
			if t.TargetHealth != nil && t.TargetHealth.State == elbv2types.TargetHealthStateEnumHealthy {
				healthy++
			}
		}
		if healthy == 0 {
			allHealthy = false
		}
		msgs = append(msgs, fmt.Sprintf("%s %d/%d", arn, healthy, len(out.TargetHealthDescriptions)))
	}
	return allHealthy, strings.Join(msgs, ", "), nil
}

// finalSuccess builds canonical NativeID, performs a final Read, and returns
// Success only if the Read returns non-empty Properties. Read failures get a
// proper classifier in Task 16.
func (s *Service) finalSuccess(ctx context.Context, req *resource.StatusRequest,
	op resource.Operation, unixStart int64, svc ecstypes.Service) (*resource.StatusResult, error) {

	if needsTargetHealth(svc) {
		elbCli, err := s.elbv2ClientFactory(s.cfg)
		if err != nil {
			return s.classifyForStatus(err, op, req, unixStart, "build ELBv2 client"), nil
		}
		healthy, msg, hErr := checkAllTGsHealthy(ctx, elbCli, svc.LoadBalancers)
		if hErr != nil {
			return s.classifyForStatus(hErr, op, req, unixStart, "DescribeTargetHealth"), nil
		}
		if !healthy {
			return s.inProgressOrTimeout(op, req, unixStart,
				"deployment stable; waiting for healthy targets: "+msg), nil
		}

		// Endpoint composition gate. Transient AWS errors here block Phase B
		// Success on the next polling tick rather than fanning out to
		// consumer fast-fail. Permanent failures (rule-routed, NLB-only,
		// AccessDenied, etc.) populate missing keys in the map; consumers
		// requesting those keys fast-fail with a clear diagnostic.
		composed := composeEndpoints(ctx, svc.LoadBalancers, elbCli, plugin.LoggerFromContext(ctx))
		if composed.TransientError != nil {
			return s.inProgressOrTimeout(op, req, unixStart,
				"deployment stable; waiting for endpoint composition: "+composed.TransientError.Error()), nil
		}
		// composed.Endpoints is intentionally not threaded through here —
		// the subsequent Read invocation re-derives the same map via
		// readWithClient, and that becomes the authoritative copy. If we
		// passed composed.Endpoints into the ProgressResult directly we'd
		// risk inconsistency with the persisted state on the next sync Read.
	}

	canonical := buildCanonicalNativeID(aws.ToString(svc.ServiceArn),
		deriveClusterShortName(req.NativeID, svc))
	readResult, readErr := s.Read(ctx, &resource.ReadRequest{
		NativeID:     canonical,
		ResourceType: req.ResourceType,
		TargetConfig: req.TargetConfig,
	})
	code, retryable, ok := classifyReadResultForFinal(readResult, readErr)
	if ok {
		return &resource.StatusResult{
			ProgressResult: &resource.ProgressResult{
				Operation:          op,
				OperationStatus:    resource.OperationStatusSuccess,
				NativeID:           canonical,
				RequestID:          "",
				ResourceProperties: []byte(readResult.Properties),
			},
		}, nil
	}
	if retryable {
		return s.inProgressOrFinalReadTimeout(op, req, unixStart,
			"post-stability Read transient: "+formatReadFailure(readResult, readErr)), nil
	}
	return &resource.StatusResult{
		ProgressResult: terminalFailurePR(op, req.NativeID, "", code,
			"post-stability Read failed: "+formatReadFailure(readResult, readErr)),
	}, nil
}

func formatReadFailure(rr *resource.ReadResult, readErr error) string {
	if readErr != nil {
		return readErr.Error()
	}
	if rr == nil {
		return "nil ReadResult"
	}
	if rr.ErrorCode != "" {
		return string(rr.ErrorCode)
	}
	return "empty Properties"
}

// deriveClusterShortName extracts the cluster short name from whichever NativeID
// shape is in flight (synthetic or canonical), or falls back to derivation from
// the DescribeServices result.
func deriveClusterShortName(nativeID string, svc ecstypes.Service) string {
	if cluster, _, ok := parseClusterAndServiceFromNativeID(nativeID); ok {
		return cluster
	}
	// Last-resort: parse from ClusterArn in svc (arn:...:cluster/<name>).
	if c := aws.ToString(svc.ClusterArn); c != "" {
		if idx := strings.LastIndex(c, "/"); idx >= 0 {
			return c[idx+1:]
		}
	}
	return ""
}
