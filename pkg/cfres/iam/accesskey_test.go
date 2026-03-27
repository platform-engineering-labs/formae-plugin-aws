// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package iam

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/config"
)

func TestAccessKey_Create_Success(t *testing.T) {
	ctx := context.Background()
	client := &mockAccessKeyClient{}

	client.On("CreateAccessKey", ctx, mock.MatchedBy(func(input *iam.CreateAccessKeyInput) bool {
		return input.UserName != nil && *input.UserName == "test-user"
	})).Return(&iam.CreateAccessKeyOutput{
		AccessKey: &iamtypes.AccessKey{
			AccessKeyId:     stringPtr("AKIAIOSFODNN7EXAMPLE"),
			SecretAccessKey: stringPtr("wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"),
			UserName:        stringPtr("test-user"),
			Status:          iamtypes.StatusTypeActive,
		},
	}, nil)

	ak := &AccessKey{cfg: &config.Config{}}
	props := map[string]any{"UserName": "test-user"}
	propsJSON, _ := json.Marshal(props)

	result, err := ak.createWithClient(ctx, client, &resource.CreateRequest{
		ResourceType: "AWS::IAM::AccessKey",
		Properties:   propsJSON,
	})

	assert.NoError(t, err)
	assert.Equal(t, resource.OperationStatusSuccess, result.ProgressResult.OperationStatus)
	assert.Equal(t, "AKIAIOSFODNN7EXAMPLE|test-user", result.ProgressResult.NativeID)
	assert.NotNil(t, result.ProgressResult.ResourceProperties)

	var resultProps map[string]any
	_ = json.Unmarshal(result.ProgressResult.ResourceProperties, &resultProps)
	assert.Equal(t, "AKIAIOSFODNN7EXAMPLE", resultProps["AccessKeyId"])
	assert.Equal(t, "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", resultProps["SecretAccessKey"])
	assert.Equal(t, "Active", resultProps["Status"])
	assert.Equal(t, "test-user", resultProps["UserName"])
	client.AssertExpectations(t)
}

func TestAccessKey_Create_MissingUserName(t *testing.T) {
	ctx := context.Background()
	client := &mockAccessKeyClient{}

	ak := &AccessKey{cfg: &config.Config{}}
	result, err := ak.createWithClient(ctx, client, &resource.CreateRequest{
		ResourceType: "AWS::IAM::AccessKey",
		Properties:   json.RawMessage(`{}`),
	})

	assert.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "UserName is required")
}

func TestAccessKey_Read_Success(t *testing.T) {
	ctx := context.Background()
	client := &mockAccessKeyClient{}

	client.On("ListAccessKeys", ctx, mock.MatchedBy(func(input *iam.ListAccessKeysInput) bool {
		return input.UserName != nil && *input.UserName == "test-user"
	})).Return(&iam.ListAccessKeysOutput{
		AccessKeyMetadata: []iamtypes.AccessKeyMetadata{
			{
				AccessKeyId: stringPtr("AKIAIOSFODNN7EXAMPLE"),
				UserName:    stringPtr("test-user"),
				Status:      iamtypes.StatusTypeActive,
			},
			{
				AccessKeyId: stringPtr("AKIAI44QH8DHBOTHER"),
				UserName:    stringPtr("test-user"),
				Status:      iamtypes.StatusTypeInactive,
			},
		},
	}, nil)

	ak := &AccessKey{cfg: &config.Config{}}
	result, err := ak.readWithClient(ctx, client, &resource.ReadRequest{
		NativeID:     "AKIAIOSFODNN7EXAMPLE|test-user",
		ResourceType: "AWS::IAM::AccessKey",
	})

	assert.NoError(t, err)
	assert.Empty(t, result.ErrorCode)

	var props map[string]any
	_ = json.Unmarshal([]byte(result.Properties), &props)
	assert.Equal(t, "AKIAIOSFODNN7EXAMPLE", props["AccessKeyId"])
	assert.Equal(t, "Active", props["Status"])
	assert.Equal(t, "test-user", props["UserName"])
	client.AssertExpectations(t)
}

func TestAccessKey_Read_NotFound(t *testing.T) {
	ctx := context.Background()
	client := &mockAccessKeyClient{}

	client.On("ListAccessKeys", ctx, mock.Anything).Return(&iam.ListAccessKeysOutput{
		AccessKeyMetadata: []iamtypes.AccessKeyMetadata{},
	}, nil)

	ak := &AccessKey{cfg: &config.Config{}}
	result, err := ak.readWithClient(ctx, client, &resource.ReadRequest{
		NativeID:     "AKIANONEXISTENT|test-user",
		ResourceType: "AWS::IAM::AccessKey",
	})

	assert.NoError(t, err)
	assert.Equal(t, resource.OperationErrorCodeNotFound, result.ErrorCode)
	client.AssertExpectations(t)
}

func TestAccessKey_Update_Success(t *testing.T) {
	ctx := context.Background()
	client := &mockAccessKeyClient{}

	client.On("UpdateAccessKey", ctx, mock.MatchedBy(func(input *iam.UpdateAccessKeyInput) bool {
		return input.AccessKeyId != nil && *input.AccessKeyId == "AKIAIOSFODNN7EXAMPLE" &&
			input.UserName != nil && *input.UserName == "test-user" &&
			input.Status == iamtypes.StatusTypeInactive
	})).Return(&iam.UpdateAccessKeyOutput{}, nil)

	// Post-update Read
	client.On("ListAccessKeys", ctx, mock.Anything).Return(&iam.ListAccessKeysOutput{
		AccessKeyMetadata: []iamtypes.AccessKeyMetadata{
			{
				AccessKeyId: stringPtr("AKIAIOSFODNN7EXAMPLE"),
				UserName:    stringPtr("test-user"),
				Status:      iamtypes.StatusTypeInactive,
			},
		},
	}, nil)

	ak := &AccessKey{cfg: &config.Config{}}
	desired := map[string]any{"UserName": "test-user", "Status": "Inactive"}
	desiredJSON, _ := json.Marshal(desired)

	result, err := ak.updateWithClient(ctx, client, &resource.UpdateRequest{
		NativeID:          "AKIAIOSFODNN7EXAMPLE|test-user",
		ResourceType:      "AWS::IAM::AccessKey",
		DesiredProperties: desiredJSON,
	})

	assert.NoError(t, err)
	assert.Equal(t, resource.OperationStatusSuccess, result.ProgressResult.OperationStatus)
	assert.Equal(t, "AKIAIOSFODNN7EXAMPLE|test-user", result.ProgressResult.NativeID)
	client.AssertExpectations(t)
}

func TestAccessKey_Delete_Success(t *testing.T) {
	ctx := context.Background()
	client := &mockAccessKeyClient{}

	client.On("DeleteAccessKey", ctx, mock.MatchedBy(func(input *iam.DeleteAccessKeyInput) bool {
		return input.AccessKeyId != nil && *input.AccessKeyId == "AKIAIOSFODNN7EXAMPLE" &&
			input.UserName != nil && *input.UserName == "test-user"
	})).Return(&iam.DeleteAccessKeyOutput{}, nil)

	ak := &AccessKey{cfg: &config.Config{}}
	result, err := ak.deleteWithClient(ctx, client, &resource.DeleteRequest{
		NativeID:     "AKIAIOSFODNN7EXAMPLE|test-user",
		ResourceType: "AWS::IAM::AccessKey",
	})

	assert.NoError(t, err)
	assert.Equal(t, resource.OperationStatusSuccess, result.ProgressResult.OperationStatus)
	client.AssertExpectations(t)
}

func TestAccessKey_Delete_NotFound(t *testing.T) {
	ctx := context.Background()
	client := &mockAccessKeyClient{}

	var noSuchEntity *iamtypes.NoSuchEntityException
	_ = noSuchEntity
	client.On("DeleteAccessKey", ctx, mock.Anything).Return(
		(*iam.DeleteAccessKeyOutput)(nil),
		fmt.Errorf("NoSuchEntity: %w", &iamtypes.NoSuchEntityException{Message: stringPtr("not found")}),
	)

	ak := &AccessKey{cfg: &config.Config{}}
	result, err := ak.deleteWithClient(ctx, client, &resource.DeleteRequest{
		NativeID:     "AKIANONEXISTENT|test-user",
		ResourceType: "AWS::IAM::AccessKey",
	})

	assert.NoError(t, err)
	assert.Equal(t, resource.OperationStatusSuccess, result.ProgressResult.OperationStatus)
	client.AssertExpectations(t)
}

func TestAccessKey_InvalidNativeID(t *testing.T) {
	ctx := context.Background()
	client := &mockAccessKeyClient{}

	ak := &AccessKey{cfg: &config.Config{}}
	_, err := ak.readWithClient(ctx, client, &resource.ReadRequest{
		NativeID: "no-pipe",
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid NativeID")
}
