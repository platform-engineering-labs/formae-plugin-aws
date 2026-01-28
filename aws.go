// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package main

import (
	"context"
	"encoding/json"

	"github.com/aws/aws-sdk-go-v2/service/cloudcontrol"
	cctypes "github.com/aws/aws-sdk-go-v2/service/cloudcontrol/types"

	"github.com/platform-engineering-labs/formae/pkg/plugin"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/ccx"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres/registry"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/config"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/helper"

	// Import cfres to trigger init() registration of all provisioners
	_ "github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres"
)

// Plugin implements the Formae ResourcePlugin interface.
// The SDK automatically provides identity methods (Name, Version, Namespace)
// and schema methods (SupportedResources, SchemaForResourceType) by reading
// formae-plugin.pkl and schema/pkl/ at startup.
type Plugin struct{}

// Compile-time check: Plugin must satisfy ResourcePlugin interface.
var _ plugin.ResourcePlugin = &Plugin{}

// EKSAutomodeResourceTypes lists AWS CloudFormation resource types that EKS Automode manages.
// These resources are tagged with "kubernetes.io/cluster/<cluster-name>" = "owned".
var EKSAutomodeResourceTypes = []string{
	"AWS::EC2::Instance",                        // Worker nodes
	"AWS::EC2::SecurityGroup",                   // Pod and node security groups
	"AWS::EC2::NetworkInterface",                // ENIs for pod networking
	"AWS::EC2::LaunchTemplate",                  // Instance configuration templates
	"AWS::AutoScaling::AutoScalingGroup",        // For scaling worker nodes
	"AWS::EC2::VPCEndpoint",                     // If using private API access
	"AWS::EC2::RouteTable",                      // If creating custom routing
	"AWS::EC2::Subnet",                          // If creating new subnets
	"AWS::EC2::Volume",                          // EBS volumes for persistent storage
	"AWS::EFS::FileSystem",                      // If using EFS for persistent storage
	"AWS::EFS::MountTarget",                     // If using EFS
	"AWS::IAM::Role",                            // Service accounts and pod execution roles
	"AWS::IAM::InstanceProfile",                 // EC2 instance permissions
	"AWS::ElasticLoadBalancingV2::LoadBalancer", // If using ALB/NLB
	"AWS::ElasticLoadBalancingV2::TargetGroup",  // If using ALB/NLB
	"AWS::Logs::LogGroup",                       // If using CloudWatch logging
}

// RateLimit returns the rate limit configuration for this plugin
func (p *Plugin) RateLimit() plugin.RateLimitConfig {
	return plugin.RateLimitConfig{
		Scope:                            plugin.RateLimitScopeNamespace,
		MaxRequestsPerSecondForNamespace: 2,
	}
}

// DiscoveryFilters returns declarative filters for excluding resources from discovery.
// Uses RFC 9535 JSONPath with match() regex function to filter EKS Automode-managed resources.
func (p *Plugin) DiscoveryFilters() []plugin.MatchFilter {
	return []plugin.MatchFilter{
		{
			// Filter out EKS Automode-managed resources.
			// These resources are tagged with "kubernetes.io/cluster/<cluster-name>" = "owned".
			// Using RFC 9535 match() function for regex pattern matching on tag keys.
			ResourceTypes: EKSAutomodeResourceTypes,
			Conditions: []plugin.FilterCondition{
				{
					PropertyPath:  `$.Tags[?match(@.Key, "kubernetes\\.io/cluster/.*")].Value`,
					PropertyValue: "owned",
				},
			},
		},
	}
}

// LabelConfig returns the label extraction configuration for discovered AWS resources.
// Most AWS resources use the Name tag for labels, but some resources don't support tags
// or have a more natural identifier property.
func (p *Plugin) LabelConfig() plugin.LabelConfig {
	return plugin.LabelConfig{
		DefaultQuery: `$.Tags[?(@.Key=='Name')].Value`,
		ResourceOverrides: map[string]string{
			// IAM resources typically don't have Name tags
			"AWS::IAM::Policy":        "$.PolicyName",
			"AWS::IAM::ManagedPolicy": "$.ManagedPolicyName",
			"AWS::IAM::Role":          "$.RoleName",
			"AWS::IAM::User":          "$.UserName",
			"AWS::IAM::Group":         "$.GroupName",
			// Route53 records use Name property
			"AWS::Route53::RecordSet": "$.Name",
			// Resources that represent relationships use parent IDs
			"AWS::EC2::VPCGatewayAttachment":          "$.VpcId",
			"AWS::EC2::SubnetRouteTableAssociation":   "$.SubnetId",
			"AWS::EC2::VPCEndpointServicePermissions": "$.ServiceId",
		},
	}
}

func (p *Plugin) Create(ctx context.Context, request *resource.CreateRequest) (*resource.CreateResult, error) {
	targetConfig := config.FromTargetConfig(request.TargetConfig)
	if registry.HasProvisioner(request.ResourceType, resource.OperationCreate) {
		provisioner := registry.Get(request.ResourceType, resource.OperationCreate, targetConfig)
		return provisioner.Create(ctx, request)
	}

	client, err := ccx.NewClient(targetConfig)
	if err != nil {
		return nil, err
	}

	return client.CreateResource(ctx, request)
}

func (p *Plugin) Update(ctx context.Context, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	if registry.HasProvisioner(request.ResourceType, resource.OperationUpdate) {
		provisioner := registry.Get(request.ResourceType, resource.OperationUpdate, config.FromTargetConfig(request.TargetConfig))
		return provisioner.Update(ctx, request)
	}

	client, err := ccx.NewClient(config.FromTargetConfig(request.TargetConfig))
	if err != nil {
		return nil, err
	}

	return client.UpdateResource(ctx, request)
}

func (p *Plugin) Status(ctx context.Context, request *resource.StatusRequest) (*resource.StatusResult, error) {
	if request.ResourceType != "" {
		if registry.HasProvisioner(request.ResourceType, resource.OperationCheckStatus) {
			provisioner := registry.Get(request.ResourceType, resource.OperationCheckStatus, config.FromTargetConfig(request.TargetConfig))
			return provisioner.Status(ctx, request)
		}
	}

	client, err := ccx.NewClient(config.FromTargetConfig(request.TargetConfig))
	if err != nil {
		return nil, err
	}

	return client.StatusResource(ctx, request, p.Read)
}

func (p *Plugin) Delete(ctx context.Context, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	if registry.HasProvisioner(request.ResourceType, resource.OperationDelete) {
		provisioner := registry.Get(request.ResourceType, resource.OperationDelete, config.FromTargetConfig(request.TargetConfig))
		return provisioner.Delete(ctx, request)
	}

	client, err := ccx.NewClient(config.FromTargetConfig(request.TargetConfig))
	if err != nil {
		return nil, err
	}

	return client.DeleteResource(ctx, request)
}

func (p *Plugin) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	if registry.HasProvisioner(request.ResourceType, resource.OperationRead) {
		provisioner := registry.Get(request.ResourceType, resource.OperationRead, config.FromTargetConfig(request.TargetConfig))
		return provisioner.Read(ctx, request)
	}

	client, err := ccx.NewClient(config.FromTargetConfig(request.TargetConfig))
	if err != nil {
		return nil, err
	}

	return client.ReadResource(ctx, request)
}

func (p *Plugin) List(ctx context.Context, request *resource.ListRequest) (*resource.ListResult, error) {
	if registry.HasProvisioner(request.ResourceType, resource.OperationList) {
		provisioner := registry.Get(request.ResourceType, resource.OperationList, config.FromTargetConfig(request.TargetConfig))
		return provisioner.List(ctx, request)
	}

	client, err := ccx.NewClient(config.FromTargetConfig(request.TargetConfig))
	if err != nil {
		return nil, err
	}

	var resourceModel *string
	if len(request.AdditionalProperties) > 0 {
		jsonBytes, err := json.Marshal(request.AdditionalProperties)
		if err != nil {
			return nil, err
		}
		resourceModelStr := string(jsonBytes)
		resourceModel = &resourceModelStr
	}
	var nativeIDs []string
	result, err := client.ListResources(ctx, &cloudcontrol.ListResourcesInput{TypeName: &request.ResourceType, MaxResults: &request.PageSize, NextToken: request.PageToken, ResourceModel: resourceModel})
	if err != nil {
		// If the parent resource doesn't exist (404), return an empty list instead of an error
		errorCode, isCloudControlError := helper.HandleCloudControlError(err)
		if isCloudControlError && errorCode == cctypes.HandlerErrorCodeNotFound {
			return &resource.ListResult{
				NativeIDs:     []string{},
				NextPageToken: nil,
			}, nil
		}
		return nil, err
	}
	for _, r := range result.ResourceDescriptions {
		nativeIDs = append(nativeIDs, *r.Identifier)
	}

	return &resource.ListResult{
		NativeIDs:     nativeIDs,
		NextPageToken: result.NextToken,
	}, nil
}
