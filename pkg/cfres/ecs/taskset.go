// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package ecs

import (
	"context"
	"encoding/json"
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

// TaskSet is a custom provisioner for AWS::ECS::TaskSet. CloudControl is broken
// for two operations on this resource type and we route both through the ECS
// SDK directly:
//
//   - List: CloudControl's generic list handler rejects Cluster+Service-only
//     requests with "Required property: [Cluster, Service, Id]", making it
//     unusable for discovery. We enumerate TaskSets via DescribeServices.
//   - Update: CloudControl's update handler hangs indefinitely when patching
//     Scale (the only mutable field per AWS's UpdateTaskSet API), timing out
//     the test harness. We call UpdateTaskSet directly.
//
// Create/Read/Delete/Status fall back to the default CloudControl path.
type TaskSet struct {
	cfg *config.Config
}

type ecsTaskSetClientInterface interface {
	DescribeServices(ctx context.Context, params *ecs.DescribeServicesInput, optFns ...func(*ecs.Options)) (*ecs.DescribeServicesOutput, error)
	UpdateTaskSet(ctx context.Context, params *ecs.UpdateTaskSetInput, optFns ...func(*ecs.Options)) (*ecs.UpdateTaskSetOutput, error)
}

var _ prov.Provisioner = &TaskSet{}

func init() {
	registry.Register("AWS::ECS::TaskSet",
		[]resource.Operation{resource.OperationList, resource.OperationUpdate},
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

func (t *TaskSet) Update(ctx context.Context, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	awsCfg, err := t.cfg.ToAwsConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading AWS config: %w", err)
	}
	return t.updateWithClient(ctx, ecs.NewFromConfig(awsCfg), request)
}

func (t *TaskSet) updateWithClient(ctx context.Context, client ecsTaskSetClientInterface, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	// NativeID is the composite <ClusterArn>|<ServiceName>|<Id> produced by
	// CloudControl on create and normalized by ccx.normalizeCompositeIdentifier.
	parts := strings.Split(request.NativeID, "|")
	if len(parts) != 3 {
		return nil, fmt.Errorf("AWS::ECS::TaskSet NativeID must be composite Cluster|Service|Id, got %q", request.NativeID)
	}
	cluster, service, taskSetID := parts[0], parts[1], parts[2]

	scale, err := extractScale(request.DesiredProperties)
	if err != nil {
		return nil, err
	}

	output, err := client.UpdateTaskSet(ctx, &ecs.UpdateTaskSetInput{
		Cluster: aws.String(cluster),
		Service: aws.String(service),
		TaskSet: aws.String(taskSetID),
		Scale:   scale,
	})
	if err != nil {
		return nil, fmt.Errorf("updating task set: %w", err)
	}

	// Merge the updated Scale into the caller's desired properties so
	// downstream idempotency checks see the new state. Other fields on the
	// TaskSet are createOnly and can't change through Update.
	var props map[string]any
	if len(request.DesiredProperties) > 0 {
		if err := json.Unmarshal(request.DesiredProperties, &props); err != nil {
			return nil, fmt.Errorf("parsing desired properties: %w", err)
		}
	}
	if props == nil {
		props = map[string]any{}
	}
	if output.TaskSet != nil && output.TaskSet.Scale != nil {
		props["Scale"] = map[string]any{
			"Unit":  string(output.TaskSet.Scale.Unit),
			"Value": output.TaskSet.Scale.Value,
		}
	}
	resultJSON, err := json.Marshal(props)
	if err != nil {
		return nil, fmt.Errorf("marshalling result properties: %w", err)
	}

	return &resource.UpdateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationUpdate,
			OperationStatus:    resource.OperationStatusSuccess,
			NativeID:           request.NativeID,
			ResourceProperties: resultJSON,
		},
	}, nil
}

func (t *TaskSet) Delete(_ context.Context, _ *resource.DeleteRequest) (*resource.DeleteResult, error) {
	return nil, fmt.Errorf("AWS::ECS::TaskSet custom provisioner only implements List")
}

func (t *TaskSet) Status(_ context.Context, _ *resource.StatusRequest) (*resource.StatusResult, error) {
	return nil, fmt.Errorf("AWS::ECS::TaskSet custom provisioner only implements List")
}

// extractScale pulls the Scale sub-resource out of a TaskSet's desired
// properties and converts it to the ECS SDK's Scale type. Returns an error
// if Scale is absent — the ECS UpdateTaskSet API requires it and calling
// without it is a usage error, not a valid empty update.
func extractScale(desired json.RawMessage) (*ecstypes.Scale, error) {
	if len(desired) == 0 {
		return nil, fmt.Errorf("AWS::ECS::TaskSet update requires DesiredProperties with Scale")
	}
	var props struct {
		Scale *struct {
			Unit  string  `json:"Unit"`
			Value float64 `json:"Value"`
		} `json:"Scale"`
	}
	if err := json.Unmarshal(desired, &props); err != nil {
		return nil, fmt.Errorf("parsing desired properties: %w", err)
	}
	if props.Scale == nil {
		return nil, fmt.Errorf("AWS::ECS::TaskSet update requires Scale in desired properties")
	}
	return &ecstypes.Scale{
		Unit:  ecstypes.ScaleUnit(props.Scale.Unit),
		Value: props.Scale.Value,
	}, nil
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
