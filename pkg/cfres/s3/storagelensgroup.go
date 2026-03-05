// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package s3

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/service/s3control"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3control/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres/prov"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres/registry"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/config"
)

type s3ControlClientInterface interface {
	UpdateStorageLensGroup(ctx context.Context, params *s3control.UpdateStorageLensGroupInput, optFns ...func(*s3control.Options)) (*s3control.UpdateStorageLensGroupOutput, error)
}

type stsClientInterface interface {
	GetCallerIdentity(ctx context.Context, params *sts.GetCallerIdentityInput, optFns ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error)
}

type StorageLensGroup struct {
	cfg *config.Config
}

var _ prov.Provisioner = &StorageLensGroup{}

func init() {
	registry.Register("AWS::S3::StorageLensGroup",
		[]resource.Operation{resource.OperationUpdate},
		func(cfg *config.Config) prov.Provisioner {
			return &StorageLensGroup{cfg: cfg}
		})
}

func (slg *StorageLensGroup) Update(ctx context.Context, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	awsCfg, err := slg.cfg.ToAwsConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading AWS config: %w", err)
	}
	s3Client := s3control.NewFromConfig(awsCfg)
	stsClient := sts.NewFromConfig(awsCfg)
	return slg.updateWithClient(ctx, s3Client, stsClient, request)
}

func (slg *StorageLensGroup) updateWithClient(ctx context.Context, client s3ControlClientInterface, stsClient stsClientInterface, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	// NativeID is the group name
	groupName := request.NativeID

	var desired map[string]any
	if err := json.Unmarshal(request.DesiredProperties, &desired); err != nil {
		return nil, fmt.Errorf("parsing desired properties: %w", err)
	}

	filterRaw, ok := desired["Filter"]
	if !ok {
		return nil, fmt.Errorf("filter is required for update")
	}

	filter, err := convertFilter(filterRaw)
	if err != nil {
		return nil, fmt.Errorf("converting filter: %w", err)
	}

	// Get account ID
	identity, err := stsClient.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return nil, fmt.Errorf("getting caller identity: %w", err)
	}

	if _, err := client.UpdateStorageLensGroup(ctx, &s3control.UpdateStorageLensGroupInput{
		AccountId: identity.Account,
		Name:      &groupName,
		StorageLensGroup: &s3types.StorageLensGroup{
			Name:   &groupName,
			Filter: filter,
		},
	}); err != nil {
		return nil, err
	}

	return &resource.UpdateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:       resource.OperationUpdate,
			OperationStatus: resource.OperationStatusSuccess,
			NativeID:        request.NativeID,
		},
	}, nil
}

func convertFilter(raw any) (*s3types.StorageLensGroupFilter, error) {
	m, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("filter must be an object")
	}

	filter := &s3types.StorageLensGroupFilter{}

	if v, ok := m["MatchAnyPrefix"]; ok {
		filter.MatchAnyPrefix = toStringSlice(v)
	}
	if v, ok := m["MatchAnySuffix"]; ok {
		filter.MatchAnySuffix = toStringSlice(v)
	}
	if v, ok := m["MatchObjectSize"]; ok {
		filter.MatchObjectSize = convertMatchObjectSize(v)
	}
	if v, ok := m["MatchObjectAge"]; ok {
		filter.MatchObjectAge = convertMatchObjectAge(v)
	}
	if v, ok := m["And"]; ok {
		and, err := convertAndOperator(v)
		if err != nil {
			return nil, err
		}
		filter.And = and
	}
	if v, ok := m["Or"]; ok {
		or, err := convertOrOperator(v)
		if err != nil {
			return nil, err
		}
		filter.Or = or
	}

	return filter, nil
}

func convertAndOperator(raw any) (*s3types.StorageLensGroupAndOperator, error) {
	m, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("and operator must be an object")
	}

	op := &s3types.StorageLensGroupAndOperator{}
	if v, ok := m["MatchAnyPrefix"]; ok {
		op.MatchAnyPrefix = toStringSlice(v)
	}
	if v, ok := m["MatchAnySuffix"]; ok {
		op.MatchAnySuffix = toStringSlice(v)
	}
	if v, ok := m["MatchObjectSize"]; ok {
		op.MatchObjectSize = convertMatchObjectSize(v)
	}
	if v, ok := m["MatchObjectAge"]; ok {
		op.MatchObjectAge = convertMatchObjectAge(v)
	}
	return op, nil
}

func convertOrOperator(raw any) (*s3types.StorageLensGroupOrOperator, error) {
	m, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("or operator must be an object")
	}

	op := &s3types.StorageLensGroupOrOperator{}
	if v, ok := m["MatchAnyPrefix"]; ok {
		op.MatchAnyPrefix = toStringSlice(v)
	}
	if v, ok := m["MatchAnySuffix"]; ok {
		op.MatchAnySuffix = toStringSlice(v)
	}
	if v, ok := m["MatchObjectSize"]; ok {
		op.MatchObjectSize = convertMatchObjectSize(v)
	}
	if v, ok := m["MatchObjectAge"]; ok {
		op.MatchObjectAge = convertMatchObjectAge(v)
	}
	return op, nil
}

func convertMatchObjectSize(raw any) *s3types.MatchObjectSize {
	m, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	result := &s3types.MatchObjectSize{}
	if v, ok := m["BytesGreaterThan"]; ok {
		result.BytesGreaterThan = toInt64(v)
	}
	if v, ok := m["BytesLessThan"]; ok {
		result.BytesLessThan = toInt64(v)
	}
	return result
}

func convertMatchObjectAge(raw any) *s3types.MatchObjectAge {
	m, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	result := &s3types.MatchObjectAge{}
	if v, ok := m["DaysGreaterThan"]; ok {
		result.DaysGreaterThan = int32(toInt64(v))
	}
	if v, ok := m["DaysLessThan"]; ok {
		result.DaysLessThan = int32(toInt64(v))
	}
	return result
}

func toStringSlice(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(arr))
	for _, item := range arr {
		if s, ok := item.(string); ok {
			result = append(result, s)
		}
	}
	return result
}

func toInt64(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int:
		return int64(n)
	case int64:
		return n
	case json.Number:
		i, _ := n.Int64()
		return i
	default:
		return 0
	}
}

func (slg *StorageLensGroup) Create(_ context.Context, _ *resource.CreateRequest) (*resource.CreateResult, error) {
	return nil, fmt.Errorf("operation not implemented - cloudcontrol handles this")
}

func (slg *StorageLensGroup) Read(_ context.Context, _ *resource.ReadRequest) (*resource.ReadResult, error) {
	return nil, fmt.Errorf("operation not implemented - cloudcontrol handles this")
}

func (slg *StorageLensGroup) Delete(_ context.Context, _ *resource.DeleteRequest) (*resource.DeleteResult, error) {
	return nil, fmt.Errorf("operation not implemented - cloudcontrol handles this")
}

func (slg *StorageLensGroup) Status(_ context.Context, _ *resource.StatusRequest) (*resource.StatusResult, error) {
	return nil, fmt.Errorf("operation not implemented - cloudcontrol handles this")
}

func (slg *StorageLensGroup) List(_ context.Context, _ *resource.ListRequest) (*resource.ListResult, error) {
	return nil, fmt.Errorf("operation not implemented - cloudcontrol handles this")
}
