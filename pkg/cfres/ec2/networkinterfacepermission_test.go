// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package ec2

import (
	"context"
	"encoding/json"
	"testing"

	ec2sdk "github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
)

// fakeNIPAPIError is a minimal smithy.APIError used to exercise the narrow
// error classification (NotFound vs. surfaced).
type fakeNIPAPIError struct {
	code string
}

func (e *fakeNIPAPIError) Error() string                 { return e.code }
func (e *fakeNIPAPIError) ErrorCode() string             { return e.code }
func (e *fakeNIPAPIError) ErrorMessage() string          { return e.code }
func (e *fakeNIPAPIError) ErrorFault() smithy.ErrorFault { return smithy.FaultServer }

func nipCreateProps() json.RawMessage {
	props := map[string]any{
		"NetworkInterfaceId": "eni-123",
		"AwsAccountId":       "123456789012",
		"Permission":         "INSTANCE-ATTACH",
	}
	b, _ := json.Marshal(props)
	return b
}

func TestNetworkInterfacePermission_Create_Success(t *testing.T) {
	ctx := context.Background()
	client := &mockNetworkInterfacePermissionClient{}

	// Pre-create lookup finds nothing.
	client.On("DescribeNetworkInterfacePermissions", ctx, mock.MatchedBy(func(input *ec2sdk.DescribeNetworkInterfacePermissionsInput) bool {
		return len(input.Filters) == 3
	})).Return(&ec2sdk.DescribeNetworkInterfacePermissionsOutput{}, nil)

	client.On("CreateNetworkInterfacePermission", ctx, mock.MatchedBy(func(input *ec2sdk.CreateNetworkInterfacePermissionInput) bool {
		return input.NetworkInterfaceId != nil && *input.NetworkInterfaceId == "eni-123" &&
			input.AwsAccountId != nil && *input.AwsAccountId == "123456789012" &&
			input.Permission == ec2types.InterfacePermissionTypeInstanceAttach
	})).Return(&ec2sdk.CreateNetworkInterfacePermissionOutput{
		InterfacePermission: &ec2types.NetworkInterfacePermission{
			NetworkInterfacePermissionId: strPtr("eni-perm-0abc123"),
			NetworkInterfaceId:           strPtr("eni-123"),
			AwsAccountId:                 strPtr("123456789012"),
			Permission:                   ec2types.InterfacePermissionTypeInstanceAttach,
		},
	}, nil)

	nip := &NetworkInterfacePermission{}
	result, err := nip.createWithClient(ctx, client, &resource.CreateRequest{
		Properties:   nipCreateProps(),
		ResourceType: "AWS::EC2::NetworkInterfacePermission",
	})

	assert.NoError(t, err)
	assert.Equal(t, resource.OperationStatusSuccess, result.ProgressResult.OperationStatus)
	assert.Equal(t, "eni-perm-0abc123", result.ProgressResult.NativeID)

	var props map[string]any
	assert.NoError(t, json.Unmarshal(result.ProgressResult.ResourceProperties, &props))
	assert.Equal(t, "eni-perm-0abc123", props["Id"])
	assert.Equal(t, "eni-123", props["NetworkInterfaceId"])
	assert.Equal(t, "123456789012", props["AwsAccountId"])
	assert.Equal(t, "INSTANCE-ATTACH", props["Permission"])
	client.AssertExpectations(t)
}

func TestNetworkInterfacePermission_Create_MissingRequiredField(t *testing.T) {
	ctx := context.Background()
	client := &mockNetworkInterfacePermissionClient{}

	props := map[string]any{
		"AwsAccountId": "123456789012",
		"Permission":   "INSTANCE-ATTACH",
	}
	propsJSON, _ := json.Marshal(props)

	nip := &NetworkInterfacePermission{}
	result, err := nip.createWithClient(ctx, client, &resource.CreateRequest{
		Properties: propsJSON,
	})

	assert.Error(t, err)
	assert.Nil(t, result)
	// No AWS calls should be made when a required field is missing.
	client.AssertNotCalled(t, "DescribeNetworkInterfacePermissions", mock.Anything, mock.Anything)
	client.AssertNotCalled(t, "CreateNetworkInterfacePermission", mock.Anything, mock.Anything)
}

func TestNetworkInterfacePermission_Create_AdoptsExistingGrant(t *testing.T) {
	ctx := context.Background()
	client := &mockNetworkInterfacePermissionClient{}

	// Pre-create lookup already finds a matching grant — adopt its id, do not
	// create a second one.
	client.On("DescribeNetworkInterfacePermissions", ctx, mock.MatchedBy(func(input *ec2sdk.DescribeNetworkInterfacePermissionsInput) bool {
		return len(input.Filters) == 3
	})).Return(&ec2sdk.DescribeNetworkInterfacePermissionsOutput{
		NetworkInterfacePermissions: []ec2types.NetworkInterfacePermission{
			{
				NetworkInterfacePermissionId: strPtr("eni-perm-existing"),
				NetworkInterfaceId:           strPtr("eni-123"),
				AwsAccountId:                 strPtr("123456789012"),
				Permission:                   ec2types.InterfacePermissionTypeInstanceAttach,
			},
		},
	}, nil)

	nip := &NetworkInterfacePermission{}
	result, err := nip.createWithClient(ctx, client, &resource.CreateRequest{
		Properties:   nipCreateProps(),
		ResourceType: "AWS::EC2::NetworkInterfacePermission",
	})

	assert.NoError(t, err)
	assert.Equal(t, resource.OperationStatusSuccess, result.ProgressResult.OperationStatus)
	assert.Equal(t, "eni-perm-existing", result.ProgressResult.NativeID)
	client.AssertNotCalled(t, "CreateNetworkInterfacePermission", mock.Anything, mock.Anything)
	client.AssertExpectations(t)
}

func TestNetworkInterfacePermission_Read_Found(t *testing.T) {
	ctx := context.Background()
	client := &mockNetworkInterfacePermissionClient{}

	client.On("DescribeNetworkInterfacePermissions", ctx, mock.MatchedBy(func(input *ec2sdk.DescribeNetworkInterfacePermissionsInput) bool {
		return len(input.NetworkInterfacePermissionIds) == 1 && input.NetworkInterfacePermissionIds[0] == "eni-perm-0abc123"
	})).Return(&ec2sdk.DescribeNetworkInterfacePermissionsOutput{
		NetworkInterfacePermissions: []ec2types.NetworkInterfacePermission{
			{
				NetworkInterfacePermissionId: strPtr("eni-perm-0abc123"),
				NetworkInterfaceId:           strPtr("eni-123"),
				AwsAccountId:                 strPtr("123456789012"),
				Permission:                   ec2types.InterfacePermissionTypeInstanceAttach,
			},
		},
	}, nil)

	nip := &NetworkInterfacePermission{}
	result, err := nip.readWithClient(ctx, client, &resource.ReadRequest{
		NativeID:     "eni-perm-0abc123",
		ResourceType: "AWS::EC2::NetworkInterfacePermission",
	})

	assert.NoError(t, err)
	assert.Empty(t, result.ErrorCode)

	var props map[string]any
	assert.NoError(t, json.Unmarshal([]byte(result.Properties), &props))
	assert.Equal(t, "eni-perm-0abc123", props["Id"])
	assert.Equal(t, "eni-123", props["NetworkInterfaceId"])
	assert.Equal(t, "123456789012", props["AwsAccountId"])
	assert.Equal(t, "INSTANCE-ATTACH", props["Permission"])
	client.AssertExpectations(t)
}

func TestNetworkInterfacePermission_Read_NotFound_ByErrorCode(t *testing.T) {
	ctx := context.Background()
	client := &mockNetworkInterfacePermissionClient{}

	client.On("DescribeNetworkInterfacePermissions", ctx, mock.Anything).Return(
		(*ec2sdk.DescribeNetworkInterfacePermissionsOutput)(nil),
		&fakeNIPAPIError{code: "InvalidNetworkInterfacePermissionID.NotFound"},
	)

	nip := &NetworkInterfacePermission{}
	result, err := nip.readWithClient(ctx, client, &resource.ReadRequest{
		NativeID:     "eni-perm-gone",
		ResourceType: "AWS::EC2::NetworkInterfacePermission",
	})

	assert.NoError(t, err)
	assert.Equal(t, resource.OperationErrorCodeNotFound, result.ErrorCode)
	client.AssertExpectations(t)
}

// EC2 returns the not-found code with inconsistent "Id"/"ID" casing across the
// network-interface family. The lowercase-"Id" form is what a deleted permission
// yields in practice (this is the out-of-band-delete sync path); classification
// must be case-insensitive so sync tombstones the resource instead of erroring.
func TestNetworkInterfacePermission_Read_NotFound_ByErrorCode_LowercaseId(t *testing.T) {
	ctx := context.Background()
	client := &mockNetworkInterfacePermissionClient{}

	client.On("DescribeNetworkInterfacePermissions", ctx, mock.Anything).Return(
		(*ec2sdk.DescribeNetworkInterfacePermissionsOutput)(nil),
		&fakeNIPAPIError{code: "InvalidNetworkInterfacePermissionId.NotFound"},
	)

	nip := &NetworkInterfacePermission{}
	result, err := nip.readWithClient(ctx, client, &resource.ReadRequest{
		NativeID:     "eni-perm-gone",
		ResourceType: "AWS::EC2::NetworkInterfacePermission",
	})

	assert.NoError(t, err)
	assert.Equal(t, resource.OperationErrorCodeNotFound, result.ErrorCode)
	client.AssertExpectations(t)
}

func TestNetworkInterfacePermission_Read_NotFound_EmptyList(t *testing.T) {
	ctx := context.Background()
	client := &mockNetworkInterfacePermissionClient{}

	client.On("DescribeNetworkInterfacePermissions", ctx, mock.Anything).Return(
		&ec2sdk.DescribeNetworkInterfacePermissionsOutput{}, nil,
	)

	nip := &NetworkInterfacePermission{}
	result, err := nip.readWithClient(ctx, client, &resource.ReadRequest{
		NativeID:     "eni-perm-0abc123",
		ResourceType: "AWS::EC2::NetworkInterfacePermission",
	})

	assert.NoError(t, err)
	assert.Equal(t, resource.OperationErrorCodeNotFound, result.ErrorCode)
	client.AssertExpectations(t)
}

func TestNetworkInterfacePermission_Read_GenericErrorSurfaced(t *testing.T) {
	ctx := context.Background()
	client := &mockNetworkInterfacePermissionClient{}

	client.On("DescribeNetworkInterfacePermissions", ctx, mock.Anything).Return(
		(*ec2sdk.DescribeNetworkInterfacePermissionsOutput)(nil),
		&fakeNIPAPIError{code: "UnauthorizedOperation"},
	)

	nip := &NetworkInterfacePermission{}
	result, err := nip.readWithClient(ctx, client, &resource.ReadRequest{
		NativeID:     "eni-perm-0abc123",
		ResourceType: "AWS::EC2::NetworkInterfacePermission",
	})

	// A generic AWS error must be surfaced, never masked as NotFound.
	assert.Error(t, err)
	assert.Nil(t, result)
	client.AssertExpectations(t)
}

func TestNetworkInterfacePermission_Delete_Success(t *testing.T) {
	ctx := context.Background()
	client := &mockNetworkInterfacePermissionClient{}

	// Read-before-delete finds the grant.
	client.On("DescribeNetworkInterfacePermissions", ctx, mock.MatchedBy(func(input *ec2sdk.DescribeNetworkInterfacePermissionsInput) bool {
		return len(input.NetworkInterfacePermissionIds) == 1 && input.NetworkInterfacePermissionIds[0] == "eni-perm-0abc123"
	})).Return(&ec2sdk.DescribeNetworkInterfacePermissionsOutput{
		NetworkInterfacePermissions: []ec2types.NetworkInterfacePermission{
			{NetworkInterfacePermissionId: strPtr("eni-perm-0abc123")},
		},
	}, nil)

	client.On("DeleteNetworkInterfacePermission", ctx, mock.MatchedBy(func(input *ec2sdk.DeleteNetworkInterfacePermissionInput) bool {
		return input.NetworkInterfacePermissionId != nil && *input.NetworkInterfacePermissionId == "eni-perm-0abc123"
	})).Return(&ec2sdk.DeleteNetworkInterfacePermissionOutput{}, nil)

	nip := &NetworkInterfacePermission{}
	result, err := nip.deleteWithClient(ctx, client, &resource.DeleteRequest{
		NativeID:     "eni-perm-0abc123",
		ResourceType: "AWS::EC2::NetworkInterfacePermission",
	})

	assert.NoError(t, err)
	assert.Equal(t, resource.OperationStatusSuccess, result.ProgressResult.OperationStatus)
	client.AssertExpectations(t)
}

func TestNetworkInterfacePermission_Delete_IdempotentWhenGone(t *testing.T) {
	ctx := context.Background()
	client := &mockNetworkInterfacePermissionClient{}

	// Read-before-delete reports it is already gone.
	client.On("DescribeNetworkInterfacePermissions", ctx, mock.Anything).Return(
		(*ec2sdk.DescribeNetworkInterfacePermissionsOutput)(nil),
		&fakeNIPAPIError{code: "InvalidNetworkInterfacePermissionID.NotFound"},
	)

	nip := &NetworkInterfacePermission{}
	result, err := nip.deleteWithClient(ctx, client, &resource.DeleteRequest{
		NativeID:     "eni-perm-gone",
		ResourceType: "AWS::EC2::NetworkInterfacePermission",
	})

	assert.NoError(t, err)
	assert.Equal(t, resource.OperationStatusSuccess, result.ProgressResult.OperationStatus)
	client.AssertNotCalled(t, "DeleteNetworkInterfacePermission", mock.Anything, mock.Anything)
	client.AssertExpectations(t)
}

func TestNetworkInterfacePermission_ParseNativeID(t *testing.T) {
	id, err := parseNetworkInterfacePermissionNativeID("eni-perm-0abc123")
	assert.NoError(t, err)
	assert.Equal(t, "eni-perm-0abc123", id)

	_, err = parseNetworkInterfacePermissionNativeID("")
	assert.Error(t, err)
}

func TestNetworkInterfacePermission_Update_FailsLoud(t *testing.T) {
	nip := &NetworkInterfacePermission{}
	_, err := nip.Update(context.Background(), &resource.UpdateRequest{})
	assert.Error(t, err)
}

func TestNetworkInterfacePermission_Status_FailsLoud(t *testing.T) {
	nip := &NetworkInterfacePermission{}
	_, err := nip.Status(context.Background(), &resource.StatusRequest{})
	assert.Error(t, err)
}
