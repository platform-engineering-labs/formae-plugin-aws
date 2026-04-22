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

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/ccx"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres/prov"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres/registry"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/config"
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
	cfg *config.Config
}

type ccxReadClient interface {
	ReadResource(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error)
}

var _ prov.Provisioner = &Service{}

func init() {
	registry.Register("AWS::ECS::Service",
		[]resource.Operation{resource.OperationRead},
		func(cfg *config.Config) prov.Provisioner {
			return &Service{cfg: cfg}
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

func (s *Service) Create(_ context.Context, _ *resource.CreateRequest) (*resource.CreateResult, error) {
	return nil, fmt.Errorf("AWS::ECS::Service custom provisioner only implements Read")
}

func (s *Service) Update(_ context.Context, _ *resource.UpdateRequest) (*resource.UpdateResult, error) {
	return nil, fmt.Errorf("AWS::ECS::Service custom provisioner only implements Read")
}

func (s *Service) Delete(_ context.Context, _ *resource.DeleteRequest) (*resource.DeleteResult, error) {
	return nil, fmt.Errorf("AWS::ECS::Service custom provisioner only implements Read")
}

func (s *Service) Status(_ context.Context, _ *resource.StatusRequest) (*resource.StatusResult, error) {
	return nil, fmt.Errorf("AWS::ECS::Service custom provisioner only implements Read")
}

func (s *Service) List(_ context.Context, _ *resource.ListRequest) (*resource.ListResult, error) {
	return nil, fmt.Errorf("AWS::ECS::Service custom provisioner only implements Read")
}
