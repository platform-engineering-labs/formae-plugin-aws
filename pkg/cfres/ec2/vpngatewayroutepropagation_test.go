// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package ec2

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ec2sdk "github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
)

func testVPNGatewayRoutePropagation() *VPNGatewayRoutePropagation {
	return &VPNGatewayRoutePropagation{
		readAttempts: 3,
		readBackoff:  0,
		sleep:        func(_ time.Duration) {},
	}
}

func routeTableWithVgw(vgwID string) *ec2sdk.DescribeRouteTablesOutput {
	return &ec2sdk.DescribeRouteTablesOutput{
		RouteTables: []ec2types.RouteTable{
			{
				PropagatingVgws: []ec2types.PropagatingVgw{
					{GatewayId: aws.String(vgwID)},
				},
			},
		},
	}
}

func routeTableWithoutVgw() *ec2sdk.DescribeRouteTablesOutput {
	return &ec2sdk.DescribeRouteTablesOutput{
		RouteTables: []ec2types.RouteTable{
			{PropagatingVgws: []ec2types.PropagatingVgw{}},
		},
	}
}

func createRequest(t *testing.T, routeTableID, vpnGatewayID string) *resource.CreateRequest {
	t.Helper()
	props, err := json.Marshal(map[string]any{
		"RouteTableId": routeTableID,
		"VpnGatewayId": vpnGatewayID,
	})
	require.NoError(t, err)
	return &resource.CreateRequest{Properties: props}
}

func TestVPNGatewayRoutePropagation_Create_HappyPath(t *testing.T) {
	client := &mockVgwRoutePropagationClient{}
	// Not yet propagating, then propagating after enable.
	client.On("DescribeRouteTables", mock.Anything, mock.Anything).
		Return(routeTableWithoutVgw(), nil).Once()
	client.On("EnableVgwRoutePropagation", mock.Anything, &ec2sdk.EnableVgwRoutePropagationInput{
		RouteTableId: aws.String("rtb-1"),
		GatewayId:    aws.String("vgw-1"),
	}).Return(&ec2sdk.EnableVgwRoutePropagationOutput{}, nil).Once()
	client.On("DescribeRouteTables", mock.Anything, mock.Anything).
		Return(routeTableWithVgw("vgw-1"), nil).Once()

	res, err := testVPNGatewayRoutePropagation().createWithClient(context.Background(), client, createRequest(t, "rtb-1", "vgw-1"))

	require.NoError(t, err)
	assert.Equal(t, resource.OperationStatusSuccess, res.ProgressResult.OperationStatus)
	assert.Equal(t, "rtb-1|vgw-1", res.ProgressResult.NativeID)
	client.AssertExpectations(t)
}

func TestVPNGatewayRoutePropagation_Create_AlreadyEnabled(t *testing.T) {
	client := &mockVgwRoutePropagationClient{}
	// Already propagating: Create is idempotent, Enable must not be called.
	client.On("DescribeRouteTables", mock.Anything, mock.Anything).
		Return(routeTableWithVgw("vgw-1"), nil).Once()

	res, err := testVPNGatewayRoutePropagation().createWithClient(context.Background(), client, createRequest(t, "rtb-1", "vgw-1"))

	require.NoError(t, err)
	assert.Equal(t, "rtb-1|vgw-1", res.ProgressResult.NativeID)
	client.AssertNotCalled(t, "EnableVgwRoutePropagation", mock.Anything, mock.Anything)
	client.AssertExpectations(t)
}

func TestVPNGatewayRoutePropagation_Create_StaleThenPresent(t *testing.T) {
	client := &mockVgwRoutePropagationClient{}
	client.On("DescribeRouteTables", mock.Anything, mock.Anything).
		Return(routeTableWithoutVgw(), nil).Once()
	client.On("EnableVgwRoutePropagation", mock.Anything, mock.Anything).
		Return(&ec2sdk.EnableVgwRoutePropagationOutput{}, nil).Once()
	// First read after enable is stale (not yet reflected), second reflects.
	client.On("DescribeRouteTables", mock.Anything, mock.Anything).
		Return(routeTableWithoutVgw(), nil).Once()
	client.On("DescribeRouteTables", mock.Anything, mock.Anything).
		Return(routeTableWithVgw("vgw-1"), nil).Once()

	res, err := testVPNGatewayRoutePropagation().createWithClient(context.Background(), client, createRequest(t, "rtb-1", "vgw-1"))

	require.NoError(t, err)
	assert.Equal(t, resource.OperationStatusSuccess, res.ProgressResult.OperationStatus)
	client.AssertExpectations(t)
}

func TestVPNGatewayRoutePropagation_Read_Found(t *testing.T) {
	client := &mockVgwRoutePropagationClient{}
	client.On("DescribeRouteTables", mock.Anything, &ec2sdk.DescribeRouteTablesInput{
		RouteTableIds: []string{"rtb-1"},
	}).Return(routeTableWithVgw("vgw-1"), nil).Once()

	res, err := testVPNGatewayRoutePropagation().readWithClient(context.Background(), client, &resource.ReadRequest{NativeID: "rtb-1|vgw-1"})

	require.NoError(t, err)
	assert.Empty(t, res.ErrorCode)
	var props map[string]any
	require.NoError(t, json.Unmarshal([]byte(res.Properties), &props))
	assert.Equal(t, "rtb-1", props["RouteTableId"])
	assert.Equal(t, "vgw-1", props["VpnGatewayId"])
	client.AssertExpectations(t)
}

func TestVPNGatewayRoutePropagation_Read_NotFound_RouteTableMissing(t *testing.T) {
	client := &mockVgwRoutePropagationClient{}
	client.On("DescribeRouteTables", mock.Anything, mock.Anything).
		Return((*ec2sdk.DescribeRouteTablesOutput)(nil), &smithy.GenericAPIError{
			Code:    "InvalidRouteTableID.NotFound",
			Message: "route table not found",
		}).Once()

	res, err := testVPNGatewayRoutePropagation().readWithClient(context.Background(), client, &resource.ReadRequest{NativeID: "rtb-gone|vgw-1"})

	require.NoError(t, err)
	assert.Equal(t, resource.OperationErrorCodeNotFound, res.ErrorCode)
	client.AssertExpectations(t)
}

func TestVPNGatewayRoutePropagation_Read_NotFound_VgwAbsent(t *testing.T) {
	client := &mockVgwRoutePropagationClient{}
	client.On("DescribeRouteTables", mock.Anything, mock.Anything).
		Return(routeTableWithVgw("vgw-other"), nil).Once()

	res, err := testVPNGatewayRoutePropagation().readWithClient(context.Background(), client, &resource.ReadRequest{NativeID: "rtb-1|vgw-1"})

	require.NoError(t, err)
	assert.Equal(t, resource.OperationErrorCodeNotFound, res.ErrorCode)
	client.AssertExpectations(t)
}

func TestVPNGatewayRoutePropagation_Read_GenericAPIError_Surfaced(t *testing.T) {
	client := &mockVgwRoutePropagationClient{}
	client.On("DescribeRouteTables", mock.Anything, mock.Anything).
		Return((*ec2sdk.DescribeRouteTablesOutput)(nil), &smithy.GenericAPIError{
			Code:    "UnauthorizedOperation",
			Message: "access denied",
		}).Once()

	res, err := testVPNGatewayRoutePropagation().readWithClient(context.Background(), client, &resource.ReadRequest{NativeID: "rtb-1|vgw-1"})

	require.Error(t, err)
	assert.Nil(t, res)
	client.AssertExpectations(t)
}

func TestVPNGatewayRoutePropagation_Delete_HappyPath(t *testing.T) {
	client := &mockVgwRoutePropagationClient{}
	client.On("DescribeRouteTables", mock.Anything, mock.Anything).
		Return(routeTableWithVgw("vgw-1"), nil).Once()
	client.On("DisableVgwRoutePropagation", mock.Anything, &ec2sdk.DisableVgwRoutePropagationInput{
		RouteTableId: aws.String("rtb-1"),
		GatewayId:    aws.String("vgw-1"),
	}).Return(&ec2sdk.DisableVgwRoutePropagationOutput{}, nil).Once()

	res, err := testVPNGatewayRoutePropagation().deleteWithClient(context.Background(), client, &resource.DeleteRequest{NativeID: "rtb-1|vgw-1"})

	require.NoError(t, err)
	assert.Equal(t, resource.OperationStatusSuccess, res.ProgressResult.OperationStatus)
	assert.Equal(t, "rtb-1|vgw-1", res.ProgressResult.NativeID)
	client.AssertExpectations(t)
}

func TestVPNGatewayRoutePropagation_Delete_AlreadyDisabled(t *testing.T) {
	client := &mockVgwRoutePropagationClient{}
	// Not propagating: delete is idempotent, Disable must not be called.
	client.On("DescribeRouteTables", mock.Anything, mock.Anything).
		Return(routeTableWithoutVgw(), nil).Once()

	res, err := testVPNGatewayRoutePropagation().deleteWithClient(context.Background(), client, &resource.DeleteRequest{NativeID: "rtb-1|vgw-1"})

	require.NoError(t, err)
	assert.Equal(t, resource.OperationStatusSuccess, res.ProgressResult.OperationStatus)
	client.AssertNotCalled(t, "DisableVgwRoutePropagation", mock.Anything, mock.Anything)
	client.AssertExpectations(t)
}

func TestVPNGatewayRoutePropagation_Delete_GenericAPIError_Surfaced(t *testing.T) {
	client := &mockVgwRoutePropagationClient{}
	client.On("DescribeRouteTables", mock.Anything, mock.Anything).
		Return((*ec2sdk.DescribeRouteTablesOutput)(nil), &smithy.GenericAPIError{
			Code:    "UnauthorizedOperation",
			Message: "access denied",
		}).Once()

	res, err := testVPNGatewayRoutePropagation().deleteWithClient(context.Background(), client, &resource.DeleteRequest{NativeID: "rtb-1|vgw-1"})

	require.Error(t, err)
	assert.Nil(t, res)
	client.AssertNotCalled(t, "DisableVgwRoutePropagation", mock.Anything, mock.Anything)
	client.AssertExpectations(t)
}

func TestParseVPNGatewayRoutePropagationNativeID(t *testing.T) {
	cases := []struct {
		name     string
		nativeID string
		wantRT   string
		wantVgw  string
		wantErr  bool
	}{
		{name: "valid", nativeID: "rtb-1|vgw-1", wantRT: "rtb-1", wantVgw: "vgw-1"},
		{name: "empty", nativeID: "", wantErr: true},
		{name: "delimiter only", nativeID: "|", wantErr: true},
		{name: "missing vgw", nativeID: "rtb-1|", wantErr: true},
		{name: "missing route table", nativeID: "|vgw-1", wantErr: true},
		{name: "extra delimiter", nativeID: "a|b|c", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rt, vgw, err := parseVPNGatewayRoutePropagationNativeID(tc.nativeID)
			if tc.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantRT, rt)
			assert.Equal(t, tc.wantVgw, vgw)
		})
	}
}
