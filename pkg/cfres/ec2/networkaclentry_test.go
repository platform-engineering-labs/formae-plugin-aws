// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package ec2

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	ec2sdk "github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
)

func TestNetworkAclEntry_Create_Success(t *testing.T) {
	ctx := context.Background()
	client := &mockEC2Client{}

	client.On("CreateNetworkAclEntry", ctx, mock.MatchedBy(func(input *ec2sdk.CreateNetworkAclEntryInput) bool {
		return input.NetworkAclId != nil && *input.NetworkAclId == "acl-123" &&
			input.RuleNumber != nil && *input.RuleNumber == int32(100) &&
			input.Protocol != nil && *input.Protocol == "6" &&
			input.RuleAction == ec2types.RuleActionAllow &&
			input.Egress != nil && *input.Egress == false &&
			input.CidrBlock != nil && *input.CidrBlock == "10.0.0.0/8" &&
			input.PortRange != nil && input.PortRange.From != nil && *input.PortRange.From == int32(80)
	})).Return(&ec2sdk.CreateNetworkAclEntryOutput{}, nil)

	nae := &NetworkAclEntry{}
	props := map[string]any{
		"NetworkAclId": "acl-123",
		"RuleNumber":   float64(100),
		"Protocol":     float64(6),
		"RuleAction":   "allow",
		"Egress":       false,
		"CidrBlock":    "10.0.0.0/8",
		"PortRange": map[string]any{
			"From": float64(80),
			"To":   float64(80),
		},
	}
	propsJSON, _ := json.Marshal(props)

	result, err := nae.createWithClient(ctx, client, &resource.CreateRequest{
		Properties:   propsJSON,
		ResourceType: "AWS::EC2::NetworkAclEntry",
	})

	assert.NoError(t, err)
	assert.Equal(t, resource.OperationStatusSuccess, result.ProgressResult.OperationStatus)
	assert.Equal(t, "acl-123|100|false", result.ProgressResult.NativeID)
	client.AssertExpectations(t)
}

func TestNetworkAclEntry_Create_APIError(t *testing.T) {
	ctx := context.Background()
	client := &mockEC2Client{}

	client.On("CreateNetworkAclEntry", ctx, mock.Anything).Return(
		(*ec2sdk.CreateNetworkAclEntryOutput)(nil), fmt.Errorf("access denied"),
	)

	nae := &NetworkAclEntry{}
	props := map[string]any{
		"NetworkAclId": "acl-123",
		"RuleNumber":   float64(100),
		"Protocol":     float64(6),
		"RuleAction":   "allow",
		"Egress":       false,
	}
	propsJSON, _ := json.Marshal(props)

	result, err := nae.createWithClient(ctx, client, &resource.CreateRequest{
		Properties: propsJSON,
	})

	assert.Error(t, err)
	assert.Nil(t, result)
	client.AssertExpectations(t)
}

func TestNetworkAclEntry_Read_Success(t *testing.T) {
	ctx := context.Background()
	client := &mockEC2Client{}

	client.On("DescribeNetworkAcls", ctx, mock.MatchedBy(func(input *ec2sdk.DescribeNetworkAclsInput) bool {
		return len(input.NetworkAclIds) == 1 && input.NetworkAclIds[0] == "acl-123"
	})).Return(&ec2sdk.DescribeNetworkAclsOutput{
		NetworkAcls: []ec2types.NetworkAcl{
			{
				NetworkAclId: strPtr("acl-123"),
				Entries: []ec2types.NetworkAclEntry{
					{
						RuleNumber: intPtr(100),
						Egress:     boolPtr(false),
						Protocol:   strPtr("6"),
						RuleAction: ec2types.RuleActionAllow,
						CidrBlock:  strPtr("10.0.0.0/8"),
						PortRange: &ec2types.PortRange{
							From: intPtr(80),
							To:   intPtr(80),
						},
					},
				},
			},
		},
	}, nil)

	nae := &NetworkAclEntry{}
	result, err := nae.readWithClient(ctx, client, &resource.ReadRequest{
		NativeID:     "acl-123|100|false",
		ResourceType: "AWS::EC2::NetworkAclEntry",
	})

	assert.NoError(t, err)
	assert.Empty(t, result.ErrorCode)
	assert.NotEmpty(t, result.Properties)

	var props map[string]any
	err = json.Unmarshal([]byte(result.Properties), &props)
	assert.NoError(t, err)
	assert.Equal(t, "acl-123", props["NetworkAclId"])
	assert.Equal(t, float64(100), props["RuleNumber"])
	assert.Equal(t, "allow", props["RuleAction"])
	client.AssertExpectations(t)
}

func TestNetworkAclEntry_Read_NotFound(t *testing.T) {
	ctx := context.Background()
	client := &mockEC2Client{}

	client.On("DescribeNetworkAcls", ctx, mock.Anything).Return(&ec2sdk.DescribeNetworkAclsOutput{
		NetworkAcls: []ec2types.NetworkAcl{
			{
				NetworkAclId: strPtr("acl-123"),
				Entries:      []ec2types.NetworkAclEntry{},
			},
		},
	}, nil)

	nae := &NetworkAclEntry{}
	result, err := nae.readWithClient(ctx, client, &resource.ReadRequest{
		NativeID:     "acl-123|100|false",
		ResourceType: "AWS::EC2::NetworkAclEntry",
	})

	assert.NoError(t, err)
	assert.Equal(t, resource.OperationErrorCodeNotFound, result.ErrorCode)
	client.AssertExpectations(t)
}

func TestNetworkAclEntry_Update_Success(t *testing.T) {
	ctx := context.Background()
	client := &mockEC2Client{}

	client.On("ReplaceNetworkAclEntry", ctx, mock.MatchedBy(func(input *ec2sdk.ReplaceNetworkAclEntryInput) bool {
		return input.NetworkAclId != nil && *input.NetworkAclId == "acl-123" &&
			input.RuleNumber != nil && *input.RuleNumber == int32(100) &&
			input.Egress != nil && *input.Egress == false &&
			input.RuleAction == ec2types.RuleActionDeny
	})).Return(&ec2sdk.ReplaceNetworkAclEntryOutput{}, nil)

	nae := &NetworkAclEntry{}
	desired := map[string]any{
		"NetworkAclId": "acl-123",
		"RuleNumber":   float64(100),
		"Protocol":     float64(6),
		"RuleAction":   "deny",
		"Egress":       false,
		"CidrBlock":    "10.0.0.0/8",
		"PortRange": map[string]any{
			"From": float64(80),
			"To":   float64(80),
		},
	}
	desiredJSON, _ := json.Marshal(desired)

	result, err := nae.updateWithClient(ctx, client, &resource.UpdateRequest{
		NativeID:          "acl-123|100|false",
		ResourceType:      "AWS::EC2::NetworkAclEntry",
		DesiredProperties: desiredJSON,
	})

	assert.NoError(t, err)
	assert.Equal(t, resource.OperationStatusSuccess, result.ProgressResult.OperationStatus)
	assert.Equal(t, "acl-123|100|false", result.ProgressResult.NativeID)
	assert.NotEmpty(t, result.ProgressResult.ResourceProperties)
	client.AssertExpectations(t)
}

func TestNetworkAclEntry_Delete_Success(t *testing.T) {
	ctx := context.Background()
	client := &mockEC2Client{}

	client.On("DeleteNetworkAclEntry", ctx, mock.MatchedBy(func(input *ec2sdk.DeleteNetworkAclEntryInput) bool {
		return input.NetworkAclId != nil && *input.NetworkAclId == "acl-123" &&
			input.RuleNumber != nil && *input.RuleNumber == int32(100) &&
			input.Egress != nil && *input.Egress == false
	})).Return(&ec2sdk.DeleteNetworkAclEntryOutput{}, nil)

	nae := &NetworkAclEntry{}
	result, err := nae.deleteWithClient(ctx, client, &resource.DeleteRequest{
		NativeID:     "acl-123|100|false",
		ResourceType: "AWS::EC2::NetworkAclEntry",
	})

	assert.NoError(t, err)
	assert.Equal(t, resource.OperationStatusSuccess, result.ProgressResult.OperationStatus)
	client.AssertExpectations(t)
}

func TestNetworkAclEntry_ParseNativeID(t *testing.T) {
	aclID, ruleNum, egress, err := parseNetworkAclEntryNativeID("acl-123|100|false")
	assert.NoError(t, err)
	assert.Equal(t, "acl-123", aclID)
	assert.Equal(t, int32(100), ruleNum)
	assert.Equal(t, false, egress)

	aclID, ruleNum, egress, err = parseNetworkAclEntryNativeID("acl-456|200|true")
	assert.NoError(t, err)
	assert.Equal(t, "acl-456", aclID)
	assert.Equal(t, int32(200), ruleNum)
	assert.Equal(t, true, egress)

	_, _, _, err = parseNetworkAclEntryNativeID("invalid")
	assert.Error(t, err)
}

// Helper functions for pointer creation in tests
func strPtr(s string) *string { return &s }
func intPtr(i int32) *int32   { return &i }
func boolPtr(b bool) *bool    { return &b }
