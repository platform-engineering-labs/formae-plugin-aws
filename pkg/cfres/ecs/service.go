// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package ecs

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/platform-engineering-labs/formae/pkg/plugin"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres/prov"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres/registry"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/config"
)

// Constants used across Phase A/Phase B. Some are wired in subsequent tasks;
// staged here so later diffs stay focused on behavior rather than constants.
const (
	phaseBPrefix            = "formae-ecs/"
	opSegCreate             = "create"
	opSegUpdate             = "update"
	defaultOperationTimeout = 20 * time.Minute
	defaultFinalReadGrace   = 2 * time.Minute
	updateGraceWindow       = 60 * time.Second
	primaryCreatedSlack     = 10 * time.Second
)

// Service is a custom provisioner for AWS::ECS::Service that implements:
//
//  1. Cluster ARN re-inflation on Read (legacy behavior). CloudControl's Read
//     response always returns the bare cluster short name, which causes phantom
//     createOnly diffs when the caller used a full ARN. Read re-inflates the
//     short name back to the ARN using region/account parsed from ServiceArn.
//
//  2. Two-phase stability tracking on Create/Update for REPLICA + ECS-controller
//     services. Phase A polls CCAPI's request token; once CCAPI accepts the
//     registration, Phase B polls DescribeServices + DescribeTargetHealth until
//     the deployment is stable (rolloutState=COMPLETED, runningCount=desiredCount,
//     ≥1 healthy target per attached ELBv2 target group). State is encoded in
//     a composite RequestID ("formae-ecs/<op>/<unixStart>/<ccapiToken>") set
//     once at Create/Update return and preserved across polls by the operator.
//
// Non-default shapes (CODE_DEPLOY / EXTERNAL controllers, DAEMON scheduling,
// classic-ELB attachments) fall through to generic CCAPI Status via the shape
// gate in Create/Update.
//
// Design: ~/dev/personal/engineering-notes/formae-plugin-aws/design/2026-05-18-ecs-service-stability-tracking.md
// Bug:    ~/dev/personal/engineering-notes/formae-plugin-aws/2026-05-17-ecs-service-create-should-report-inprogress-until-deployment-stable.md
type Service struct {
	cfg                *config.Config
	ccxClientFactory   func(*config.Config) (ccxClient, error)
	ecsClientFactory   func(*config.Config) (ecsClient, error)
	elbv2ClientFactory func(*config.Config) (elbv2Client, error)
	now                func() time.Time
	operationTimeout   time.Duration
	finalReadGrace     time.Duration
}

type ccxReadClient interface {
	ReadResource(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error)
}

var _ prov.Provisioner = &Service{}

func init() {
	registry.Register("AWS::ECS::Service",
		[]resource.Operation{
			resource.OperationRead,
			resource.OperationCreate,
			resource.OperationUpdate,
			resource.OperationCheckStatus,
			// Delete intentionally NOT registered — generic CCAPI Delete polls correctly
			// per the bug report's 6-minute deleting state observation.
		},
		func(cfg *config.Config) prov.Provisioner {
			return &Service{
				cfg:                cfg,
				ccxClientFactory:   defaultCCXClientFactory,
				ecsClientFactory:   defaultECSClientFactory,
				elbv2ClientFactory: defaultELBv2ClientFactory,
				now:                time.Now,
				operationTimeout:   defaultOperationTimeout,
				finalReadGrace:     defaultFinalReadGrace,
			}
		})
}

func (s *Service) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	client, err := s.ccxClientFactory(s.cfg)
	if err != nil {
		return nil, fmt.Errorf("loading CloudControl client: %w", err)
	}
	return s.readWithClient(ctx, client, request)
}

func (s *Service) readWithClient(ctx context.Context, client ccxReadClient, request *resource.ReadRequest) (*resource.ReadResult, error) {
	result, err := client.ReadResource(ctx, request)
	if err != nil {
		return nil, err
	}
	if result == nil || result.ErrorCode != "" || result.Properties == "" {
		return result, nil
	}

	var props map[string]any
	if err := json.Unmarshal([]byte(result.Properties), &props); err != nil {
		return nil, fmt.Errorf("parsing Service properties: %w", err)
	}

	// Cluster ARN re-inflation (legacy behavior).
	if cluster, ok := props["Cluster"].(string); ok && cluster != "" && !strings.HasPrefix(cluster, "arn:") {
		serviceArn, _ := props["ServiceArn"].(string)
		partition, region, account, parsed := parseEcsArn(serviceArn)
		if !parsed {
			plugin.LoggerFromContext(ctx).Debug("AWS::ECS::Service Read: skipping Cluster ARN re-inflation, ServiceArn unparseable",
				"cluster", cluster, "serviceArn", serviceArn, "nativeID", request.NativeID)
		} else {
			props["Cluster"] = fmt.Sprintf("arn:%s:ecs:%s:%s:cluster/%s", partition, region, account, cluster)
		}
	}

	// Endpoint composition: derive `Endpoints` from `LoadBalancers[]`.
	if s.elbv2ClientFactory != nil {
		lbs := decodeLoadBalancersFromProps(props)
		elb, err := s.elbv2ClientFactory(s.cfg)
		if err != nil {
			return nil, fmt.Errorf("building ELBv2 client for endpoint composition: %w", err)
		}
		composed := composeEndpoints(ctx, lbs, elb, plugin.LoggerFromContext(ctx))
		if composed.TransientError != nil {
			// Surface as a recoverable plugin error so ResolveCache and the
			// sync loop retry the Plugin Read. Do not return partial
			// Endpoints data — preserve last-known-good in persisted state.
			return &resource.ReadResult{
				ResourceType: result.ResourceType,
				ErrorCode:    resource.OperationErrorCodeThrottling,
			}, nil
		}
		props["Endpoints"] = composed.Endpoints
	}

	out, err := json.Marshal(props)
	if err != nil {
		return nil, fmt.Errorf("marshaling Service properties: %w", err)
	}
	return &resource.ReadResult{
		ResourceType: result.ResourceType,
		Properties:   string(out),
	}, nil
}

// decodeLoadBalancersFromProps converts the JSON-shaped LoadBalancers field
// from the CCAPI Read response into the ECS SDK LoadBalancer type for use
// with composeEndpoints. Returns nil if the field is missing or malformed.
func decodeLoadBalancersFromProps(props map[string]any) []ecstypes.LoadBalancer {
	raw, ok := props["LoadBalancers"].([]any)
	if !ok {
		return nil
	}
	out := make([]ecstypes.LoadBalancer, 0, len(raw))
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		entry := ecstypes.LoadBalancer{}
		if v, ok := m["ContainerName"].(string); ok && v != "" {
			vc := v
			entry.ContainerName = &vc
		}
		if v, ok := m["ContainerPort"].(float64); ok {
			vi := int32(v)
			entry.ContainerPort = &vi
		}
		if v, ok := m["TargetGroupArn"].(string); ok && v != "" {
			vc := v
			entry.TargetGroupArn = &vc
		}
		out = append(out, entry)
	}
	return out
}

// parseEcsArn splits any ECS ARN (cluster, service, task-set, ...) into its
// partition, region, and account. Returns ok=false for anything that doesn't
// look like an ECS ARN — the caller is expected to treat that as "leave the
// response unchanged" rather than as an error.
func parseEcsArn(arn string) (partition, region, account string, ok bool) {
	if !strings.HasPrefix(arn, "arn:") {
		return "", "", "", false
	}
	parts := strings.Split(arn, ":")
	if len(parts) < 6 || parts[2] != "ecs" {
		return "", "", "", false
	}
	return parts[1], parts[3], parts[4], true
}

func (s *Service) Create(ctx context.Context, req *resource.CreateRequest) (*resource.CreateResult, error) {
	// Shape gate FIRST. Non-Phase-B shapes (CODE_DEPLOY/EXTERNAL/DAEMON) skip
	// the wrap entirely and fall through to generic CCAPI Status.
	usePhaseB := shapeSupportsPhaseB(req.Properties)

	var cluster, service string
	if usePhaseB {
		c, s2, err := parseCreateClusterAndService(req.Properties)
		if err != nil {
			return &resource.CreateResult{
				ProgressResult: terminalFailurePR(resource.OperationCreate, "", "",
					resource.OperationErrorCodeInvalidRequest, err.Error()),
			}, nil
		}
		cluster, service = c, s2
	}

	cli, err := s.ccxClientFactory(s.cfg)
	if err != nil {
		return &resource.CreateResult{
			ProgressResult: classifyForEntry(err, resource.OperationCreate, "", "build CCAPI client"),
		}, nil
	}
	res, err := cli.CreateResource(ctx, req)
	if err != nil {
		return &resource.CreateResult{
			ProgressResult: classifyForEntry(err, resource.OperationCreate, "", "ccx.CreateResource"),
		}, nil
	}
	if usePhaseB {
		s.wrapForCreate(res.ProgressResult, cluster, service)
	}
	return res, nil
}

func (s *Service) Update(ctx context.Context, req *resource.UpdateRequest) (*resource.UpdateResult, error) {
	// Shape gate FIRST. PriorProperties is authoritative — DesiredProperties may
	// omit createOnly fields (deploymentController, schedulingStrategy) in patch shape.
	usePhaseB := shapeSupportsPhaseB(req.PriorProperties)

	var cluster, service string
	if usePhaseB {
		c, s2, err := parseUpdateClusterAndService(req.NativeID)
		if err != nil {
			return &resource.UpdateResult{
				ProgressResult: terminalFailurePR(resource.OperationUpdate, req.NativeID, "",
					resource.OperationErrorCodeInvalidRequest, err.Error()),
			}, nil
		}
		cluster, service = c, s2
	}

	cli, err := s.ccxClientFactory(s.cfg)
	if err != nil {
		return &resource.UpdateResult{
			ProgressResult: classifyForEntry(err, resource.OperationUpdate, req.NativeID, "build CCAPI client"),
		}, nil
	}
	res, err := cli.UpdateResource(ctx, req)
	if err != nil {
		return &resource.UpdateResult{
			ProgressResult: classifyForEntry(err, resource.OperationUpdate, req.NativeID, "ccx.UpdateResource"),
		}, nil
	}

	// Sync-preflight OOB-delete intercept: ccx.UpdateResource preflights with
	// GetResource (pkg/ccx/client.go:171-180); ResourceNotFoundException there
	// wraps as Failure with ErrorCode=NotFound. NotFound is in the SDK's
	// recoverableErrorCodes table, so propagating it would loop the operator.
	// Swap to GeneralServiceException (non-recoverable).
	if res.ProgressResult != nil &&
		res.ProgressResult.OperationStatus == resource.OperationStatusFailure &&
		res.ProgressResult.ErrorCode == resource.OperationErrorCodeNotFound {
		return &resource.UpdateResult{
			ProgressResult: terminalFailurePR(resource.OperationUpdate, req.NativeID, "",
				resource.OperationErrorCodeGeneralServiceException,
				"ECS service deleted out-of-band before Update could fire (CCAPI preflight NotFound): "+
					res.ProgressResult.StatusMessage),
		}, nil
	}

	if usePhaseB {
		s.wrapForUpdate(res.ProgressResult, req.NativeID, cluster, service)
	}
	return res, nil
}

func (s *Service) Status(ctx context.Context, req *resource.StatusRequest) (*resource.StatusResult, error) {
	op, unixStart, ccapiToken, ok := parseComposite(req.RequestID)
	if !ok {
		return s.delegateRawStatus(ctx, req)
	}
	cluster, service, ok := parseClusterAndServiceFromNativeID(req.NativeID)
	if !ok {
		return &resource.StatusResult{
			ProgressResult: terminalFailurePR(op, req.NativeID, req.RequestID,
				resource.OperationErrorCodeInvalidRequest,
				"malformed NativeID: "+req.NativeID),
		}, nil
	}

	// Always poll CCAPI first; Phase B runs only after CCAPI Success.
	phaseAResult, ccapiSuccess := s.checkPhaseA(ctx, req, op, unixStart, ccapiToken)
	if !ccapiSuccess {
		return phaseAResult, nil
	}

	// Phase B path.
	return s.statusPhaseB(ctx, req, op, unixStart, cluster, service)
}

// delegateRawStatus passes a non-composite request straight through to
// ccx.StatusResource. Used for CODE_DEPLOY/EXTERNAL/DAEMON services whose
// Create/Update declined to wrap the RequestID, plus legacy replays.
func (s *Service) delegateRawStatus(ctx context.Context, req *resource.StatusRequest) (*resource.StatusResult, error) {
	cli, err := s.ccxClientFactory(s.cfg)
	if err != nil {
		return nil, err
	}
	return cli.StatusResource(ctx, req, s.Read)
}

func (s *Service) Delete(_ context.Context, _ *resource.DeleteRequest) (*resource.DeleteResult, error) {
	return nil, fmt.Errorf("AWS::ECS::Service custom provisioner does not implement Delete")
}

func (s *Service) List(_ context.Context, _ *resource.ListRequest) (*resource.ListResult, error) {
	return nil, fmt.Errorf("AWS::ECS::Service custom provisioner does not implement List")
}
