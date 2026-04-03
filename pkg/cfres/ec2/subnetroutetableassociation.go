// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package ec2

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/aws/aws-sdk-go-v2/aws"
	ccsdk "github.com/aws/aws-sdk-go-v2/service/cloudcontrol"
	ec2sdk "github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres/prov"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres/registry"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/config"
)

// SubnetRouteTableAssociation provides a custom List implementation that filters
// out main/default route table associations. CloudControl's ListResources returns
// ALL route table associations including the implicit main association for each VPC,
// but GetResource rejects these with InvalidRequest ("does not belong to a subnet").
//
// TODO: Consider modeling main route table associations as a separate resource type
// (similar to Terraform's aws_main_route_table_association) so they can be discovered
// and managed independently.
type SubnetRouteTableAssociation struct {
	cfg *config.Config
}

type ccListClient interface {
	ListResources(ctx context.Context, params *ccsdk.ListResourcesInput, optFns ...func(*ccsdk.Options)) (*ccsdk.ListResourcesOutput, error)
}

type describeRouteTablesClient interface {
	DescribeRouteTables(ctx context.Context, params *ec2sdk.DescribeRouteTablesInput, optFns ...func(*ec2sdk.Options)) (*ec2sdk.DescribeRouteTablesOutput, error)
}

var _ prov.Provisioner = &SubnetRouteTableAssociation{}

func init() {
	registry.Register("AWS::EC2::SubnetRouteTableAssociation",
		[]resource.Operation{resource.OperationList},
		func(cfg *config.Config) prov.Provisioner {
			return &SubnetRouteTableAssociation{cfg: cfg}
		})
}

func (s *SubnetRouteTableAssociation) List(ctx context.Context, request *resource.ListRequest) (*resource.ListResult, error) {
	awsCfg, err := s.cfg.ToAwsConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading AWS config: %w", err)
	}

	ccClient := ccsdk.NewFromConfig(awsCfg)
	ec2Client := ec2sdk.NewFromConfig(awsCfg)

	return s.listWithClients(ctx, ccClient, ec2Client, request)
}

// listWithClients allows DI of the CC and EC2 clients for testing.
func (s *SubnetRouteTableAssociation) listWithClients(ctx context.Context, ccClient ccListClient, ec2Client describeRouteTablesClient, request *resource.ListRequest) (*resource.ListResult, error) {
	typeName := request.ResourceType
	result, err := ccClient.ListResources(ctx, &ccsdk.ListResourcesInput{
		TypeName:   &typeName,
		MaxResults: &request.PageSize,
		NextToken:  request.PageToken,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list SubnetRouteTableAssociations: %w", err)
	}

	var allIDs []string
	for _, r := range result.ResourceDescriptions {
		allIDs = append(allIDs, *r.Identifier)
	}

	if len(allIDs) == 0 {
		return &resource.ListResult{
			NativeIDs:     []string{},
			NextPageToken: result.NextToken,
		}, nil
	}

	// Identify main route table associations via DescribeRouteTables.
	// One API call covers all associations — no per-resource calls needed.
	mainAssociations, err := getMainRouteTableAssociations(ctx, ec2Client, allIDs)
	if err != nil {
		slog.Warn("Failed to identify main route table associations, returning all",
			"error", err, "count", len(allIDs))
		return &resource.ListResult{
			NativeIDs:     allIDs,
			NextPageToken: result.NextToken,
		}, nil
	}

	var filtered []string
	for _, id := range allIDs {
		if mainAssociations[id] {
			slog.Debug("Filtering out main route table association from discovery", "associationId", id)
			continue
		}
		filtered = append(filtered, id)
	}

	return &resource.ListResult{
		NativeIDs:     filtered,
		NextPageToken: result.NextToken,
	}, nil
}

// getMainRouteTableAssociations returns a set of association IDs that are main/default
// associations (VPC-level, not subnet-level).
func getMainRouteTableAssociations(ctx context.Context, client describeRouteTablesClient, associationIDs []string) (map[string]bool, error) {
	resp, err := client.DescribeRouteTables(ctx, &ec2sdk.DescribeRouteTablesInput{
		Filters: []ec2types.Filter{
			{
				Name:   aws.String("association.route-table-association-id"),
				Values: associationIDs,
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("DescribeRouteTables failed: %w", err)
	}

	mainIDs := make(map[string]bool)
	for _, rt := range resp.RouteTables {
		for _, assoc := range rt.Associations {
			if assoc.Main != nil && *assoc.Main && assoc.RouteTableAssociationId != nil {
				mainIDs[*assoc.RouteTableAssociationId] = true
			}
		}
	}

	return mainIDs, nil
}

func (s *SubnetRouteTableAssociation) Create(_ context.Context, _ *resource.CreateRequest) (*resource.CreateResult, error) {
	return nil, fmt.Errorf("create not implemented - cloudcontrol handles this operation")
}

func (s *SubnetRouteTableAssociation) Update(_ context.Context, _ *resource.UpdateRequest) (*resource.UpdateResult, error) {
	return nil, fmt.Errorf("update not implemented - cloudcontrol handles this operation")
}

func (s *SubnetRouteTableAssociation) Delete(_ context.Context, _ *resource.DeleteRequest) (*resource.DeleteResult, error) {
	return nil, fmt.Errorf("delete not implemented - cloudcontrol handles this operation")
}

func (s *SubnetRouteTableAssociation) Status(_ context.Context, _ *resource.StatusRequest) (*resource.StatusResult, error) {
	return nil, fmt.Errorf("status not implemented - cloudcontrol handles this operation")
}

func (s *SubnetRouteTableAssociation) Read(_ context.Context, _ *resource.ReadRequest) (*resource.ReadResult, error) {
	return nil, fmt.Errorf("read not implemented - cloudcontrol handles this operation")
}
