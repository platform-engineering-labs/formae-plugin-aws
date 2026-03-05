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

	s3sdk "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/config"
)

func TestBucketPolicy_Update_Success(t *testing.T) {
	ctx := context.Background()
	client := &mockS3Client{}

	client.On("PutBucketPolicy", ctx, mock.MatchedBy(func(input *s3sdk.PutBucketPolicyInput) bool {
		return input.Bucket != nil && *input.Bucket == "my-bucket" &&
			input.Policy != nil && *input.Policy != ""
	})).Return(&s3sdk.PutBucketPolicyOutput{}, nil)

	bp := &BucketPolicy{cfg: &config.Config{}}
	desired := map[string]any{
		"Bucket": "my-bucket",
		"PolicyDocument": map[string]any{
			"Version": "2012-10-17",
			"Statement": []any{
				map[string]any{
					"Effect":    "Allow",
					"Principal": "*",
					"Action":    "s3:GetObject",
					"Resource":  "arn:aws:s3:::my-bucket/*",
				},
			},
		},
	}
	desiredJSON, _ := json.Marshal(desired)

	result, err := bp.updateWithClient(ctx, client, &resource.UpdateRequest{
		NativeID:          "my-bucket",
		ResourceType:      "AWS::S3::BucketPolicy",
		DesiredProperties: desiredJSON,
	})

	assert.NoError(t, err)
	assert.Equal(t, resource.OperationStatusSuccess, result.ProgressResult.OperationStatus)
	assert.Equal(t, "my-bucket", result.ProgressResult.NativeID)
	client.AssertExpectations(t)
}

func TestBucketPolicy_Update_MissingPolicyDocument(t *testing.T) {
	ctx := context.Background()
	client := &mockS3Client{}

	bp := &BucketPolicy{cfg: &config.Config{}}
	desired := map[string]any{
		"Bucket": "my-bucket",
	}
	desiredJSON, _ := json.Marshal(desired)

	result, err := bp.updateWithClient(ctx, client, &resource.UpdateRequest{
		NativeID:          "my-bucket",
		ResourceType:      "AWS::S3::BucketPolicy",
		DesiredProperties: desiredJSON,
	})

	assert.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "policyDocument is required")
}

func TestBucketPolicy_Update_APIError(t *testing.T) {
	ctx := context.Background()
	client := &mockS3Client{}

	client.On("PutBucketPolicy", ctx, mock.Anything).Return(
		(*s3sdk.PutBucketPolicyOutput)(nil), fmt.Errorf("access denied"),
	)

	bp := &BucketPolicy{cfg: &config.Config{}}
	desired := map[string]any{
		"PolicyDocument": map[string]any{"Version": "2012-10-17"},
	}
	desiredJSON, _ := json.Marshal(desired)

	result, err := bp.updateWithClient(ctx, client, &resource.UpdateRequest{
		NativeID:          "my-bucket",
		ResourceType:      "AWS::S3::BucketPolicy",
		DesiredProperties: desiredJSON,
	})

	assert.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "access denied")
	client.AssertExpectations(t)
}
