// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package ecs

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres/prov"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres/registry"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/config"
)

// TaskSet is a custom List-only provisioner for AWS::ECS::TaskSet. CloudControl's
// generic list handler for this type rejects requests that only pass
// Cluster+Service with "Required property: [Cluster, Service, Id]", effectively
// requiring a specific TaskSet id and making it unusable for true discovery.
// This provisioner uses the ECS SDK's DescribeTaskSets (Cluster + Service only)
// to enumerate TaskSets per parent service.
//
// Create/Read/Update/Delete/Status fall back to the default CloudControl path
// because this provisioner is only registered for OperationList.
type TaskSet struct {
	cfg *config.Config
}

type ecsTaskSetClientInterface interface {
	DescribeServices(ctx context.Context, params *ecs.DescribeServicesInput, optFns ...func(*ecs.Options)) (*ecs.DescribeServicesOutput, error)
}

var _ prov.Provisioner = &TaskSet{}

func init() {
	registry.Register("AWS::ECS::TaskSet",
		[]resource.Operation{resource.OperationList},
		func(cfg *config.Config) prov.Provisioner {
			return &TaskSet{cfg: cfg}
		})
}

func (t *TaskSet) List(ctx context.Context, request *resource.ListRequest) (*resource.ListResult, error) {
	awsCfg, err := t.cfg.ToAwsConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading AWS config: %w", err)
	}
	return t.listWithClient(ctx, ecs.NewFromConfig(awsCfg), request)
}

func (t *TaskSet) listWithClient(ctx context.Context, client ecsTaskSetClientInterface, request *resource.ListRequest) (*resource.ListResult, error) {
	cluster, ok := request.AdditionalProperties["Cluster"]
	if !ok || cluster == "" {
		return nil, fmt.Errorf("AWS::ECS::TaskSet list requires Cluster filter")
	}
	service, ok := request.AdditionalProperties["Service"]
	if !ok || service == "" {
		return nil, fmt.Errorf("AWS::ECS::TaskSet list requires Service filter")
	}

	// ECS's DescribeTaskSets rejects calls without an explicit TaskSets filter
	// ("TaskSets cannot be empty"), and there is no ListTaskSets API. Pull the
	// list from DescribeServices, which returns the service's task sets inline
	// for EXTERNAL deployment controller services.
	output, err := client.DescribeServices(ctx, &ecs.DescribeServicesInput{
		Cluster:  aws.String(cluster),
		Services: []string{service},
	})
	if err != nil {
		// Treat missing parent as empty list rather than a discovery failure:
		// the parent may have been deleted between the list operation being
		// queued and the call landing.
		var clusterNotFound *ecstypes.ClusterNotFoundException
		if errors.As(err, &clusterNotFound) {
			return &resource.ListResult{NativeIDs: []string{}}, nil
		}
		return nil, fmt.Errorf("describing services: %w", err)
	}

	if len(output.Services) == 0 || len(output.Failures) > 0 {
		// Service was filtered out (inactive) or failed lookup: treat as empty.
		return &resource.ListResult{NativeIDs: []string{}}, nil
	}

	svc := output.Services[0]
	nativeIDs := make([]string, 0, len(svc.TaskSets))
	for _, ts := range svc.TaskSets {
		if ts.Id == nil || ts.ClusterArn == nil {
			continue
		}
		// Composite native ID mirrors the CloudControl CRUD path for TaskSet:
		//   <ClusterArn>|<ServiceName>|<Id>
		// ccx.normalizeCompositeIdentifier preserves parts[0] as-is (the
		// cluster ARN CC returns) and strips parts[1]+ ARNs to short names.
		// Discovery must produce the same shape or inventory lookups diverge
		// between discovered and managed TaskSets.
		nativeIDs = append(nativeIDs, strings.Join([]string{
			aws.ToString(ts.ClusterArn),
			lastArnSegment(aws.ToString(ts.ServiceArn)),
			aws.ToString(ts.Id),
		}, "|"))
	}

	return &resource.ListResult{NativeIDs: nativeIDs}, nil
}

// The remaining Provisioner methods are not implemented because this provisioner
// is only registered for OperationList. The registry routes other operations to
// the default CloudControl path.

func (t *TaskSet) Create(_ context.Context, _ *resource.CreateRequest) (*resource.CreateResult, error) {
	return nil, fmt.Errorf("AWS::ECS::TaskSet custom provisioner only implements List")
}

func (t *TaskSet) Read(_ context.Context, _ *resource.ReadRequest) (*resource.ReadResult, error) {
	return nil, fmt.Errorf("AWS::ECS::TaskSet custom provisioner only implements List")
}

func (t *TaskSet) Update(_ context.Context, _ *resource.UpdateRequest) (*resource.UpdateResult, error) {
	return nil, fmt.Errorf("AWS::ECS::TaskSet custom provisioner only implements List")
}

func (t *TaskSet) Delete(_ context.Context, _ *resource.DeleteRequest) (*resource.DeleteResult, error) {
	return nil, fmt.Errorf("AWS::ECS::TaskSet custom provisioner only implements List")
}

func (t *TaskSet) Status(_ context.Context, _ *resource.StatusRequest) (*resource.StatusResult, error) {
	return nil, fmt.Errorf("AWS::ECS::TaskSet custom provisioner only implements List")
}

// lastArnSegment returns the segment after the final "/" in an ARN, matching
// ccx.normalizeCompositeIdentifier's logic for parts[1]+ of composite IDs.
// Non-ARN inputs are returned unchanged.
func lastArnSegment(arn string) string {
	if !strings.HasPrefix(arn, "arn:aws:") {
		return arn
	}
	if idx := strings.LastIndex(arn, "/"); idx >= 0 {
		return arn[idx+1:]
	}
	return arn
}
