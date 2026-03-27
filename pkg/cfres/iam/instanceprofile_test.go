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

	iamsdk "github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
)

func TestInstanceProfile_Update_SwapRoles(t *testing.T) {
	ctx := context.Background()
	client := &mockInstanceProfileClient{}

	client.On("RemoveRoleFromInstanceProfile", ctx, mock.MatchedBy(func(input *iamsdk.RemoveRoleFromInstanceProfileInput) bool {
		return input.InstanceProfileName != nil && *input.InstanceProfileName == "my-profile" &&
			input.RoleName != nil && *input.RoleName == "role1"
	})).Return(&iamsdk.RemoveRoleFromInstanceProfileOutput{}, nil)

	client.On("AddRoleToInstanceProfile", ctx, mock.MatchedBy(func(input *iamsdk.AddRoleToInstanceProfileInput) bool {
		return input.InstanceProfileName != nil && *input.InstanceProfileName == "my-profile" &&
			input.RoleName != nil && *input.RoleName == "role2"
	})).Return(&iamsdk.AddRoleToInstanceProfileOutput{}, nil)

	ip := &InstanceProfile{}
	previous := map[string]any{
		"InstanceProfileName": "my-profile",
		"Roles":               []string{"role1"},
	}
	desired := map[string]any{
		"InstanceProfileName": "my-profile",
		"Roles":               []string{"role2"},
	}
	previousJSON, _ := json.Marshal(previous)
	desiredJSON, _ := json.Marshal(desired)

	result, err := ip.updateWithClient(ctx, client, &resource.UpdateRequest{
		NativeID:           "my-profile",
		ResourceType:       "AWS::IAM::InstanceProfile",
		PriorProperties: previousJSON,
		DesiredProperties:  desiredJSON,
	})

	assert.NoError(t, err)
	assert.Equal(t, resource.OperationStatusSuccess, result.ProgressResult.OperationStatus)
	assert.Equal(t, "my-profile", result.ProgressResult.NativeID)
	assert.NotEmpty(t, result.ProgressResult.ResourceProperties)
	client.AssertExpectations(t)
}

func TestInstanceProfile_Update_APIError(t *testing.T) {
	ctx := context.Background()
	client := &mockInstanceProfileClient{}

	client.On("RemoveRoleFromInstanceProfile", ctx, mock.Anything).Return(
		(*iamsdk.RemoveRoleFromInstanceProfileOutput)(nil), fmt.Errorf("access denied"),
	)

	ip := &InstanceProfile{}
	previous := map[string]any{
		"InstanceProfileName": "my-profile",
		"Roles":               []string{"role1"},
	}
	desired := map[string]any{
		"InstanceProfileName": "my-profile",
		"Roles":               []string{"role2"},
	}
	previousJSON, _ := json.Marshal(previous)
	desiredJSON, _ := json.Marshal(desired)

	result, err := ip.updateWithClient(ctx, client, &resource.UpdateRequest{
		NativeID:           "my-profile",
		ResourceType:       "AWS::IAM::InstanceProfile",
		PriorProperties: previousJSON,
		DesiredProperties:  desiredJSON,
	})

	assert.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "access denied")
	client.AssertExpectations(t)
}

func TestInstanceProfile_Update_NoRoleChange(t *testing.T) {
	ctx := context.Background()
	client := &mockInstanceProfileClient{}

	ip := &InstanceProfile{}
	previous := map[string]any{
		"InstanceProfileName": "my-profile",
		"Roles":               []string{"role1"},
	}
	desired := map[string]any{
		"InstanceProfileName": "my-profile",
		"Roles":               []string{"role1"},
	}
	previousJSON, _ := json.Marshal(previous)
	desiredJSON, _ := json.Marshal(desired)

	result, err := ip.updateWithClient(ctx, client, &resource.UpdateRequest{
		NativeID:           "my-profile",
		ResourceType:       "AWS::IAM::InstanceProfile",
		PriorProperties: previousJSON,
		DesiredProperties:  desiredJSON,
	})

	assert.NoError(t, err)
	assert.Equal(t, resource.OperationStatusSuccess, result.ProgressResult.OperationStatus)
	assert.Equal(t, "my-profile", result.ProgressResult.NativeID)
	assert.NotEmpty(t, result.ProgressResult.ResourceProperties)
	// No API calls should have been made
	client.AssertExpectations(t)
}
