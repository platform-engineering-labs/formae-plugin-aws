// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package ec2

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	ec2sdk "github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	ccsdk "github.com/aws/aws-sdk-go-v2/service/cloudcontrol"
	cctypes "github.com/aws/aws-sdk-go-v2/service/cloudcontrol/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
)

func TestSubnetRouteTableAssociation_List_FiltersMainAssociations(t *testing.T) {
	ctx := context.Background()
	ccClient := &mockCCClient{}
	ec2Client := &mockSRTAClient{}

	// CC ListResources returns both a main association and a subnet association
	ccClient.On("ListResources", ctx, mock.MatchedBy(func(input *ccsdk.ListResourcesInput) bool {
		return *input.TypeName == "AWS::EC2::SubnetRouteTableAssociation"
	})).Return(&ccsdk.ListResourcesOutput{
		ResourceDescriptions: []cctypes.ResourceDescription{
			{Identifier: aws.String("rtbassoc-main-111")},
			{Identifier: aws.String("rtbassoc-subnet-222")},
		},
	}, nil)

	// DescribeRouteTables returns one route table with two associations:
	// one main (no subnet) and one explicit subnet association
	ec2Client.On("DescribeRouteTables", ctx, mock.Anything).Return(&ec2sdk.DescribeRouteTablesOutput{
		RouteTables: []ec2types.RouteTable{
			{
				Associations: []ec2types.RouteTableAssociation{
					{
						RouteTableAssociationId: aws.String("rtbassoc-main-111"),
						Main:                    aws.Bool(true),
						SubnetId:                nil,
					},
					{
						RouteTableAssociationId: aws.String("rtbassoc-subnet-222"),
						Main:                    aws.Bool(false),
						SubnetId:                aws.String("subnet-aaa"),
					},
				},
			},
		},
	}, nil)

	srta := &SubnetRouteTableAssociation{}
	result, err := srta.listWithClients(ctx, ccClient, ec2Client, &resource.ListRequest{
		ResourceType: "AWS::EC2::SubnetRouteTableAssociation",
		PageSize:     100,
	})

	assert.NoError(t, err)
	assert.Equal(t, []string{"rtbassoc-subnet-222"}, result.NativeIDs,
		"main route table associations should be filtered out")
	ccClient.AssertExpectations(t)
	ec2Client.AssertExpectations(t)
}

func TestSubnetRouteTableAssociation_List_AllSubnetAssociations(t *testing.T) {
	ctx := context.Background()
	ccClient := &mockCCClient{}
	ec2Client := &mockSRTAClient{}

	// CC ListResources returns only subnet associations
	ccClient.On("ListResources", ctx, mock.Anything).Return(&ccsdk.ListResourcesOutput{
		ResourceDescriptions: []cctypes.ResourceDescription{
			{Identifier: aws.String("rtbassoc-aaa")},
			{Identifier: aws.String("rtbassoc-bbb")},
		},
	}, nil)

	// No main associations
	ec2Client.On("DescribeRouteTables", ctx, mock.Anything).Return(&ec2sdk.DescribeRouteTablesOutput{
		RouteTables: []ec2types.RouteTable{
			{
				Associations: []ec2types.RouteTableAssociation{
					{
						RouteTableAssociationId: aws.String("rtbassoc-aaa"),
						Main:                    aws.Bool(false),
						SubnetId:                aws.String("subnet-111"),
					},
					{
						RouteTableAssociationId: aws.String("rtbassoc-bbb"),
						Main:                    aws.Bool(false),
						SubnetId:                aws.String("subnet-222"),
					},
				},
			},
		},
	}, nil)

	srta := &SubnetRouteTableAssociation{}
	result, err := srta.listWithClients(ctx, ccClient, ec2Client, &resource.ListRequest{
		ResourceType: "AWS::EC2::SubnetRouteTableAssociation",
		PageSize:     100,
	})

	assert.NoError(t, err)
	assert.Equal(t, []string{"rtbassoc-aaa", "rtbassoc-bbb"}, result.NativeIDs,
		"all subnet associations should be returned")
}

func TestSubnetRouteTableAssociation_List_EmptyList(t *testing.T) {
	ctx := context.Background()
	ccClient := &mockCCClient{}
	ec2Client := &mockSRTAClient{}

	ccClient.On("ListResources", ctx, mock.Anything).Return(&ccsdk.ListResourcesOutput{
		ResourceDescriptions: []cctypes.ResourceDescription{},
	}, nil)

	srta := &SubnetRouteTableAssociation{}
	result, err := srta.listWithClients(ctx, ccClient, ec2Client, &resource.ListRequest{
		ResourceType: "AWS::EC2::SubnetRouteTableAssociation",
		PageSize:     100,
	})

	assert.NoError(t, err)
	assert.Empty(t, result.NativeIDs)
	// DescribeRouteTables should NOT be called when CC returns no resources
	ec2Client.AssertNotCalled(t, "DescribeRouteTables", mock.Anything, mock.Anything)
}
