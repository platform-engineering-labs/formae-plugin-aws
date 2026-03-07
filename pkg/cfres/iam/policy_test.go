// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package iam

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/config"
)

func TestPolicy_Create_SingleRole(t *testing.T) {
	ctx := context.Background()
	client := &mockPolicyClient{}

	client.On("PutRolePolicy", ctx, mock.MatchedBy(func(input *iam.PutRolePolicyInput) bool {
		return *input.RoleName == "my-role" && *input.PolicyName == "my-policy"
	})).Return(&iam.PutRolePolicyOutput{}, nil)

	p := &Policy{cfg: &config.Config{}}
	props := map[string]any{
		"PolicyName": "my-policy",
		"PolicyDocument": map[string]any{
			"Version":   "2012-10-17",
			"Statement": []any{map[string]any{"Effect": "Allow", "Action": "*", "Resource": "*"}},
		},
		"Roles": []any{"my-role"},
	}
	propsJSON, _ := json.Marshal(props)

	result, err := p.createWithClient(ctx, client, &resource.CreateRequest{
		ResourceType: "AWS::IAM::Policy",
		Properties:   propsJSON,
	})

	assert.NoError(t, err)
	assert.Equal(t, resource.OperationStatusSuccess, result.ProgressResult.OperationStatus)
	assert.Equal(t, "my-policy|R:my-role", result.ProgressResult.NativeID)
	client.AssertExpectations(t)
}

func TestPolicy_Create_MultipleTargets(t *testing.T) {
	ctx := context.Background()
	client := &mockPolicyClient{}

	client.On("PutRolePolicy", ctx, mock.Anything).Return(&iam.PutRolePolicyOutput{}, nil)
	client.On("PutUserPolicy", ctx, mock.Anything).Return(&iam.PutUserPolicyOutput{}, nil)
	client.On("PutGroupPolicy", ctx, mock.Anything).Return(&iam.PutGroupPolicyOutput{}, nil)

	p := &Policy{cfg: &config.Config{}}
	props := map[string]any{
		"PolicyName":     "multi-policy",
		"PolicyDocument": map[string]any{"Version": "2012-10-17"},
		"Roles":          []any{"role-a", "role-b"},
		"Users":          []any{"user-a"},
		"Groups":         []any{"group-a"},
	}
	propsJSON, _ := json.Marshal(props)

	result, err := p.createWithClient(ctx, client, &resource.CreateRequest{
		ResourceType: "AWS::IAM::Policy",
		Properties:   propsJSON,
	})

	assert.NoError(t, err)
	assert.Equal(t, "multi-policy|R:role-a,role-b|U:user-a|G:group-a", result.ProgressResult.NativeID)
	client.AssertNumberOfCalls(t, "PutRolePolicy", 2)
	client.AssertNumberOfCalls(t, "PutUserPolicy", 1)
	client.AssertNumberOfCalls(t, "PutGroupPolicy", 1)
}

func TestPolicy_Create_MissingPolicyName(t *testing.T) {
	ctx := context.Background()
	client := &mockPolicyClient{}

	p := &Policy{cfg: &config.Config{}}
	result, err := p.createWithClient(ctx, client, &resource.CreateRequest{
		Properties: json.RawMessage(`{"PolicyDocument": {}}`),
	})

	assert.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "PolicyName is required")
}

func TestPolicy_Read_SingleRole(t *testing.T) {
	ctx := context.Background()
	client := &mockPolicyClient{}

	policyDoc := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow"}]}`
	encodedDoc := url.QueryEscape(policyDoc)

	client.On("GetRolePolicy", ctx, mock.MatchedBy(func(input *iam.GetRolePolicyInput) bool {
		return *input.RoleName == "my-role" && *input.PolicyName == "my-policy"
	})).Return(&iam.GetRolePolicyOutput{
		PolicyName:     stringPtr("my-policy"),
		PolicyDocument: &encodedDoc,
	}, nil)

	p := &Policy{cfg: &config.Config{}}
	result, err := p.readWithClient(ctx, client, &resource.ReadRequest{
		NativeID:     "my-policy|R:my-role",
		ResourceType: "AWS::IAM::Policy",
	})

	assert.NoError(t, err)
	assert.Empty(t, result.ErrorCode)

	var props map[string]any
	_ = json.Unmarshal([]byte(result.Properties), &props)
	assert.Equal(t, "my-policy", props["PolicyName"])
	assert.NotNil(t, props["PolicyDocument"])
	roles := props["Roles"].([]any)
	assert.Equal(t, "my-role", roles[0])
	client.AssertExpectations(t)
}

func TestPolicy_Read_NotFound(t *testing.T) {
	ctx := context.Background()
	client := &mockPolicyClient{}

	client.On("GetRolePolicy", ctx, mock.Anything).Return(
		(*iam.GetRolePolicyOutput)(nil),
		fmt.Errorf("wrapped: %w", &iamtypes.NoSuchEntityException{Message: stringPtr("not found")}),
	)

	p := &Policy{cfg: &config.Config{}}
	result, err := p.readWithClient(ctx, client, &resource.ReadRequest{
		NativeID:     "my-policy|R:my-role",
		ResourceType: "AWS::IAM::Policy",
	})

	assert.NoError(t, err)
	assert.Equal(t, resource.OperationErrorCodeNotFound, result.ErrorCode)
	client.AssertExpectations(t)
}

func TestPolicy_Delete_SingleRole(t *testing.T) {
	ctx := context.Background()
	client := &mockPolicyClient{}

	client.On("DeleteRolePolicy", ctx, mock.MatchedBy(func(input *iam.DeleteRolePolicyInput) bool {
		return *input.RoleName == "my-role" && *input.PolicyName == "my-policy"
	})).Return(&iam.DeleteRolePolicyOutput{}, nil)

	p := &Policy{cfg: &config.Config{}}
	result, err := p.deleteWithClient(ctx, client, &resource.DeleteRequest{
		NativeID:     "my-policy|R:my-role",
		ResourceType: "AWS::IAM::Policy",
	})

	assert.NoError(t, err)
	assert.Equal(t, resource.OperationStatusSuccess, result.ProgressResult.OperationStatus)
	client.AssertExpectations(t)
}

func TestPolicy_Delete_NotFound(t *testing.T) {
	ctx := context.Background()
	client := &mockPolicyClient{}

	client.On("DeleteRolePolicy", ctx, mock.Anything).Return(
		(*iam.DeleteRolePolicyOutput)(nil),
		fmt.Errorf("wrapped: %w", &iamtypes.NoSuchEntityException{Message: stringPtr("not found")}),
	)

	p := &Policy{cfg: &config.Config{}}
	result, err := p.deleteWithClient(ctx, client, &resource.DeleteRequest{
		NativeID:     "my-policy|R:my-role",
		ResourceType: "AWS::IAM::Policy",
	})

	assert.NoError(t, err)
	assert.Equal(t, resource.OperationStatusSuccess, result.ProgressResult.OperationStatus)
	client.AssertExpectations(t)
}

func TestPolicy_Update_ChangePolicyDocument(t *testing.T) {
	ctx := context.Background()
	client := &mockPolicyClient{}

	// PutRolePolicy for the update
	client.On("PutRolePolicy", ctx, mock.Anything).Return(&iam.PutRolePolicyOutput{}, nil)

	// Post-update Read
	newDoc := `{"Version":"2012-10-17","Statement":[{"Effect":"Deny"}]}`
	encodedDoc := url.QueryEscape(newDoc)
	client.On("GetRolePolicy", ctx, mock.Anything).Return(&iam.GetRolePolicyOutput{
		PolicyName:     stringPtr("my-policy"),
		PolicyDocument: &encodedDoc,
	}, nil)

	p := &Policy{cfg: &config.Config{}}
	desired := map[string]any{
		"PolicyName":     "my-policy",
		"PolicyDocument": map[string]any{"Version": "2012-10-17", "Statement": []any{map[string]any{"Effect": "Deny"}}},
		"Roles":          []any{"my-role"},
	}
	desiredJSON, _ := json.Marshal(desired)

	result, err := p.updateWithClient(ctx, client, &resource.UpdateRequest{
		NativeID:          "my-policy|R:my-role",
		ResourceType:      "AWS::IAM::Policy",
		DesiredProperties: desiredJSON,
	})

	assert.NoError(t, err)
	assert.Equal(t, resource.OperationStatusSuccess, result.ProgressResult.OperationStatus)
	assert.Equal(t, "my-policy|R:my-role", result.ProgressResult.NativeID)
	client.AssertExpectations(t)
}

func TestParsePolicyNativeID(t *testing.T) {
	tests := []struct {
		name       string
		nativeID   string
		policyName string
		roles      []string
		users      []string
		groups     []string
		wantErr    bool
	}{
		{
			name:       "single role",
			nativeID:   "my-policy|R:my-role",
			policyName: "my-policy",
			roles:      []string{"my-role"},
		},
		{
			name:       "multiple roles",
			nativeID:   "my-policy|R:role-a,role-b",
			policyName: "my-policy",
			roles:      []string{"role-a", "role-b"},
		},
		{
			name:       "all target types",
			nativeID:   "my-policy|R:role-a|U:user-a|G:group-a",
			policyName: "my-policy",
			roles:      []string{"role-a"},
			users:      []string{"user-a"},
			groups:     []string{"group-a"},
		},
		{
			name:     "invalid format",
			nativeID: "just-a-name",
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			policyName, roles, users, groups, err := parsePolicyNativeID(tt.nativeID)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, tt.policyName, policyName)
			assert.Equal(t, tt.roles, roles)
			assert.Equal(t, tt.users, users)
			assert.Equal(t, tt.groups, groups)
		})
	}
}

func TestBuildPolicyNativeID(t *testing.T) {
	assert.Equal(t, "p|R:r1", buildPolicyNativeID("p", []string{"r1"}, nil, nil))
	assert.Equal(t, "p|R:r1,r2|U:u1|G:g1", buildPolicyNativeID("p", []string{"r1", "r2"}, []string{"u1"}, []string{"g1"}))
	assert.Equal(t, "p|U:u1", buildPolicyNativeID("p", nil, []string{"u1"}, nil))
}
