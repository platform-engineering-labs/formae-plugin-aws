// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package s3

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3control"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/config"
)

func strPtr(s string) *string {
	return &s
}

func TestStorageLensGroup_Update_Success(t *testing.T) {
	ctx := context.Background()
	s3Client := &mockS3ControlClient{}
	stsClient := &mockSTSClient{}

	stsClient.On("GetCallerIdentity", ctx, mock.Anything).Return(
		&sts.GetCallerIdentityOutput{Account: strPtr("123456789012")}, nil,
	)

	s3Client.On("UpdateStorageLensGroup", ctx, mock.MatchedBy(func(input *s3control.UpdateStorageLensGroupInput) bool {
		return input.AccountId != nil && *input.AccountId == "123456789012" &&
			input.Name != nil && *input.Name == "my-group" &&
			input.StorageLensGroup != nil &&
			input.StorageLensGroup.Filter != nil &&
			len(input.StorageLensGroup.Filter.MatchAnyPrefix) == 2
	})).Return(&s3control.UpdateStorageLensGroupOutput{}, nil)

	slg := &StorageLensGroup{cfg: &config.Config{}}
	desired := map[string]any{
		"Name": "my-group",
		"Filter": map[string]any{
			"MatchAnyPrefix": []any{"logs/", "data/"},
			"MatchObjectSize": map[string]any{
				"BytesGreaterThan": float64(1024),
			},
		},
	}
	desiredJSON, _ := json.Marshal(desired)

	result, err := slg.updateWithClient(ctx, s3Client, stsClient, &resource.UpdateRequest{
		NativeID:          "my-group",
		ResourceType:      "AWS::S3::StorageLensGroup",
		DesiredProperties: desiredJSON,
	})

	assert.NoError(t, err)
	assert.Equal(t, resource.OperationStatusSuccess, result.ProgressResult.OperationStatus)
	assert.Equal(t, "my-group", result.ProgressResult.NativeID)
	assert.NotEmpty(t, result.ProgressResult.ResourceProperties)
	s3Client.AssertExpectations(t)
	stsClient.AssertExpectations(t)
}

func TestStorageLensGroup_Update_MissingFilter(t *testing.T) {
	ctx := context.Background()
	s3Client := &mockS3ControlClient{}
	stsClient := &mockSTSClient{}

	slg := &StorageLensGroup{cfg: &config.Config{}}
	desired := map[string]any{
		"Name": "my-group",
	}
	desiredJSON, _ := json.Marshal(desired)

	result, err := slg.updateWithClient(ctx, s3Client, stsClient, &resource.UpdateRequest{
		NativeID:          "my-group",
		ResourceType:      "AWS::S3::StorageLensGroup",
		DesiredProperties: desiredJSON,
	})

	assert.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "filter is required")
}

func TestStorageLensGroup_Update_APIError(t *testing.T) {
	ctx := context.Background()
	s3Client := &mockS3ControlClient{}
	stsClient := &mockSTSClient{}

	stsClient.On("GetCallerIdentity", ctx, mock.Anything).Return(
		&sts.GetCallerIdentityOutput{Account: strPtr("123456789012")}, nil,
	)

	s3Client.On("UpdateStorageLensGroup", ctx, mock.Anything).Return(
		(*s3control.UpdateStorageLensGroupOutput)(nil), fmt.Errorf("service exception"),
	)

	slg := &StorageLensGroup{cfg: &config.Config{}}
	desired := map[string]any{
		"Filter": map[string]any{
			"MatchAnyPrefix": []any{"logs/"},
		},
	}
	desiredJSON, _ := json.Marshal(desired)

	result, err := slg.updateWithClient(ctx, s3Client, stsClient, &resource.UpdateRequest{
		NativeID:          "my-group",
		ResourceType:      "AWS::S3::StorageLensGroup",
		DesiredProperties: desiredJSON,
	})

	assert.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "service exception")
	s3Client.AssertExpectations(t)
}

func TestConvertFilter_SimplePrefix(t *testing.T) {
	raw := map[string]any{
		"MatchAnyPrefix": []any{"logs/", "data/"},
	}

	filter, err := convertFilter(raw)
	assert.NoError(t, err)
	assert.Equal(t, []string{"logs/", "data/"}, filter.MatchAnyPrefix)
}

func TestConvertFilter_WithObjectSize(t *testing.T) {
	raw := map[string]any{
		"MatchAnyPrefix": []any{"logs/"},
		"MatchObjectSize": map[string]any{
			"BytesGreaterThan": float64(1024),
			"BytesLessThan":    float64(1048576),
		},
	}

	filter, err := convertFilter(raw)
	assert.NoError(t, err)
	assert.Equal(t, []string{"logs/"}, filter.MatchAnyPrefix)
	assert.NotNil(t, filter.MatchObjectSize)
	assert.Equal(t, int64(1024), filter.MatchObjectSize.BytesGreaterThan)
	assert.Equal(t, int64(1048576), filter.MatchObjectSize.BytesLessThan)
}

func TestConvertFilter_WithAndOperator(t *testing.T) {
	raw := map[string]any{
		"And": map[string]any{
			"MatchAnyPrefix": []any{"logs/"},
			"MatchAnySuffix": []any{".gz"},
		},
	}

	filter, err := convertFilter(raw)
	assert.NoError(t, err)
	assert.NotNil(t, filter.And)
	assert.Equal(t, []string{"logs/"}, filter.And.MatchAnyPrefix)
	assert.Equal(t, []string{".gz"}, filter.And.MatchAnySuffix)
}
