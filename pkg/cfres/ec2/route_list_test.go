// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package ec2

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/smithy-go"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockRouteClient struct {
	output *ec2.DescribeRouteTablesOutput
	err    error
}

func (m *mockRouteClient) DescribeRouteTables(_ context.Context, _ *ec2.DescribeRouteTablesInput, _ ...func(*ec2.Options)) (*ec2.DescribeRouteTablesOutput, error) {
	return m.output, m.err
}

func TestRouteList(t *testing.T) {
	route := &Route{}

	t.Run("emits the same composite native id the create path builds", func(t *testing.T) {
		client := &mockRouteClient{output: &ec2.DescribeRouteTablesOutput{
			RouteTables: []ec2types.RouteTable{{
				Routes: []ec2types.Route{
					{DestinationCidrBlock: aws.String("10.0.0.0/16"), GatewayId: aws.String("local")},
					{DestinationCidrBlock: aws.String("0.0.0.0/0"), GatewayId: aws.String("igw-123")},
					{DestinationCidrBlock: aws.String("10.1.0.0/16"), NatGatewayId: aws.String("nat-456")},
				},
			}},
		}}
		result, err := route.listWithClient(context.Background(), client, &resource.ListRequest{
			AdditionalProperties: map[string]string{"RouteTableId": "rtb-abc"},
		})
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{
			"rtb-abc|10.0.0.0/16|GatewayId=local",
			"rtb-abc|0.0.0.0/0|GatewayId=igw-123",
			"rtb-abc|10.1.0.0/16|NatGatewayId=nat-456",
		}, result.NativeIDs)
	})

	t.Run("skips routes outside the modelled shape", func(t *testing.T) {
		client := &mockRouteClient{output: &ec2.DescribeRouteTablesOutput{
			RouteTables: []ec2types.RouteTable{{
				Routes: []ec2types.Route{
					{DestinationIpv6CidrBlock: aws.String("::/0"), GatewayId: aws.String("igw-123")},
					{DestinationPrefixListId: aws.String("pl-789"), GatewayId: aws.String("igw-123")},
					{DestinationCidrBlock: aws.String("0.0.0.0/0"), GatewayId: aws.String("igw-123")},
				},
			}},
		}}
		result, err := route.listWithClient(context.Background(), client, &resource.ListRequest{
			AdditionalProperties: map[string]string{"RouteTableId": "rtb-abc"},
		})
		require.NoError(t, err)
		assert.Equal(t, []string{"rtb-abc|0.0.0.0/0|GatewayId=igw-123"}, result.NativeIDs)
	})

	t.Run("requires the RouteTableId filter", func(t *testing.T) {
		_, err := route.listWithClient(context.Background(), &mockRouteClient{}, &resource.ListRequest{
			AdditionalProperties: map[string]string{},
		})
		assert.Error(t, err)
	})

	t.Run("treats a deleted route table as an empty list", func(t *testing.T) {
		client := &mockRouteClient{err: &smithy.GenericAPIError{Code: "InvalidRouteTableID.NotFound"}}
		result, err := route.listWithClient(context.Background(), client, &resource.ListRequest{
			AdditionalProperties: map[string]string{"RouteTableId": "rtb-gone"},
		})
		require.NoError(t, err)
		assert.Empty(t, result.NativeIDs)
	})

	t.Run("propagates other errors", func(t *testing.T) {
		client := &mockRouteClient{err: &smithy.GenericAPIError{Code: "UnauthorizedOperation"}}
		_, err := route.listWithClient(context.Background(), client, &resource.ListRequest{
			AdditionalProperties: map[string]string{"RouteTableId": "rtb-abc"},
		})
		assert.Error(t, err)
	})
}
