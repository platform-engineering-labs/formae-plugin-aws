// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package ecs

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/ccx"
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

// Service is a custom provisioner for AWS::ECS::Service. CloudControl accepts
// either a cluster short name or a full ARN in the Cluster field on Create,
// but its Read response always returns the bare short name. When the caller
// set Cluster with an ARN-valued Resolvable (e.g. `cluster = ecsCluster.res.arn`),
// that mismatch surfaces on every reapply as a phantom createOnly diff on the
// Cluster field — the planner promotes the Update to a full Replace.
//
// This provisioner intercepts Read and re-inflates the bare cluster name back
// to a full ARN using the region and account parsed from the always-present
// ServiceArn, so the comparator sees the same shape the user sent on Create.
// Create/Update/Delete/Status continue through the generic CloudControl path.
//
// Bug reference: formae@.claude/handover/spurious-replace-ecs/BUG-2-CLUSTER-ARN-DRIFT.md.
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
	client, err := ccx.NewClient(s.cfg)
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

	cluster, _ := props["Cluster"].(string)
	if cluster == "" || strings.HasPrefix(cluster, "arn:") {
		return result, nil
	}

	// Derive region + account from ServiceArn (format:
	// arn:<partition>:ecs:<region>:<account>:service/<cluster>/<service>).
	// If ServiceArn is missing or malformed we can't safely reconstruct the
	// ARN — leave the short name in place and log enough to find it later.
	serviceArn, _ := props["ServiceArn"].(string)
	partition, region, account, ok := parseEcsArn(serviceArn)
	if !ok {
		slog.Debug("AWS::ECS::Service Read: skipping Cluster ARN re-inflation, ServiceArn unparseable",
			"cluster", cluster, "serviceArn", serviceArn, "nativeID", request.NativeID)
		return result, nil
	}
	props["Cluster"] = fmt.Sprintf("arn:%s:ecs:%s:%s:cluster/%s", partition, region, account, cluster)

	out, err := json.Marshal(props)
	if err != nil {
		return nil, fmt.Errorf("marshaling re-inflated Service properties: %w", err)
	}
	return &resource.ReadResult{
		ResourceType: result.ResourceType,
		Properties:   string(out),
	}, nil
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
