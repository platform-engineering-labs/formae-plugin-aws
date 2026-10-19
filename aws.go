// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/cloudcontrol"
	cctypes "github.com/aws/aws-sdk-go-v2/service/cloudcontrol/types"

	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/arn"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/ccx"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres/registry"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/config"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/helper"
	pkgmodel "github.com/platform-engineering-labs/formae/pkg/model"
	"github.com/platform-engineering-labs/formae/pkg/plugin"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"

	// Import cfres to trigger init() registration of all provisioners
	_ "github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres"
)

// Plugin implements the Formae ResourcePlugin interface.
// The SDK automatically provides identity methods (Name, Version, Namespace)
// and schema methods (SupportedResources, SchemaForResourceType) by reading
// formae-plugin.pkl and schema/pkl/ at startup.
type Plugin struct {
	// oidc carries the token source the SDK installs via SetOidcTokenSource,
	// plus the plugin-lifetime credentials cache it backs. Nil until the SDK
	// calls SetOidcTokenSource (or on an agent too old to pair a broker), in
	// which case every target config threads nil deps and Oidc auth fails
	// closed rather than falling back to ambient credentials.
	oidc *config.OidcDeps

	// authPolicy is configured once at startup and copied onto every target
	// config before credentials are resolved.
	authPolicy config.AuthPolicy
}

// Compile-time check: Plugin must satisfy ResourcePlugin interface.
var _ plugin.ResourcePlugin = &Plugin{}

// Compile-time check: Plugin must satisfy OidcAware, so the SDK hands it an
// OidcTokenSource at startup.
var _ plugin.OidcAware = &Plugin{}

// Compile-time check: Plugin accepts custom settings from schema/Config.pkl.
var _ plugin.Configurable = &Plugin{}

type pluginConfig struct {
	AllowedAuthMethods []string `json:"allowedAuthMethods"`
}

// Configure applies plugin-specific startup settings. An absent or empty
// allowedAuthMethods list intentionally preserves unrestricted behaviour.
func (p *Plugin) Configure(raw json.RawMessage) error {
	var cfg pluginConfig
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return fmt.Errorf("decode AWS plugin configuration: %w", err)
		}
	}

	policy, err := config.NewAuthPolicy(cfg.AllowedAuthMethods)
	if err != nil {
		return err
	}
	p.authPolicy = policy

	return nil
}

// SetOidcTokenSource receives the token source the SDK mints OIDC identity
// tokens through. Called once at startup; every FromTargetConfig call below
// threads the resulting deps onto the parsed Config so Oidc auth blocks can
// exchange a token for AWS credentials.
func (p *Plugin) SetOidcTokenSource(src plugin.OidcTokenSource) {
	p.oidc = config.NewOidcDeps(src)
}

func (p *Plugin) targetConfig(raw json.RawMessage) *config.Config {
	return config.FromTargetConfig(raw).
		WithOidcDeps(p.oidc).
		WithAuthPolicy(p.authPolicy)
}

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
func (p *Plugin) RateLimit() pkgmodel.RateLimitConfig {
	return pkgmodel.RateLimitConfig{
		Scope:                            pkgmodel.RateLimitScopeNamespace,
		MaxRequestsPerSecondForNamespace: 2,
	}
}

// DiscoveryFilters returns declarative filters for excluding resources from discovery.
// Uses RFC 9535 JSONPath with match() regex function to filter EKS Automode-managed resources.
func (p *Plugin) DiscoveryFilters() []pkgmodel.MatchFilter {
	return []pkgmodel.MatchFilter{
		{
			// Anything formae created in order to run in this account or to
			// reach it. Chiefly the connect role and the account-global OIDC
			// provider its trust policy names: discovered like any other
			// resource, they could be imported and then reconciled away, which
			// severs formae's own access to the account.
			//
			// The marker answers one question, whether formae created the
			// thing. It does not say who may delete it, which is why it is
			// separate from the formae-ai:owner provenance tag the connect
			// tooling reads. A name prefix is not ownership: unrelated roles
			// share the "formae" prefix and must stay visible.
			Conditions: []pkgmodel.FilterCondition{
				{
					PropertyPath:  `$.Tags[?(@.Key=='formae-owned')].Value`,
					PropertyValue: "true",
				},
			},
		},
		{
			// EFS is the exception to $.Tags. FileSystem and AccessPoint expose
			// their tags under per-type properties, so the filter above never
			// sees them and they would leak.
			ResourceTypes: []string{"AWS::EFS::FileSystem"},
			Conditions: []pkgmodel.FilterCondition{
				{
					PropertyPath:  `$.FileSystemTags[?(@.Key=='formae-owned')].Value`,
					PropertyValue: "true",
				},
			},
		},
		{
			ResourceTypes: []string{"AWS::EFS::AccessPoint"},
			Conditions: []pkgmodel.FilterCondition{
				{
					PropertyPath:  `$.AccessPointTags[?(@.Key=='formae-owned')].Value`,
					PropertyValue: "true",
				},
			},
		},
		{
			// Filter out EKS Automode-managed resources.
			// These resources are tagged with "kubernetes.io/cluster/<cluster-name>" = "owned".
			// Using RFC 9535 match() function for regex pattern matching on tag keys.
			ResourceTypes: EKSAutomodeResourceTypes,
			Conditions: []pkgmodel.FilterCondition{
				{
					PropertyPath:  `$.Tags[?match(@.Key, "kubernetes\\.io/cluster/.*")].Value`,
					PropertyValue: "owned",
				},
			},
		},
		{
			// Filter out AWS-owned managed prefix lists (com.amazonaws.*). They
			// exist in every account, cannot be modified or deleted, and their
			// prefix-list id carries no ownership signal, so the owner is only
			// visible after the read.
			ResourceTypes: []string{"AWS::EC2::PrefixList"},
			Conditions: []pkgmodel.FilterCondition{
				{
					PropertyPath:  "$.OwnerId",
					PropertyValue: "AWS",
				},
			},
		},
		{
			// Filter out the implicit local route every route table carries for
			// its VPC CIDR. It is created and deleted with the route table and
			// cannot be managed on its own.
			ResourceTypes: []string{"AWS::EC2::Route"},
			Conditions: []pkgmodel.FilterCondition{
				{
					PropertyPath:  "$.GatewayId",
					PropertyValue: "local",
				},
			},
		},
	}
}

// discoveryListExclusions skips resources whose native id alone identifies
// them as AWS-managed, before the per-resource read that discovery performs
// on every listed id. DiscoveryFilters run agent-side only after that read,
// so types with large AWS-managed populations (IAM ships ~1400 managed
// policies) would spend the whole rate-limit budget reading resources that
// are then discarded.
var discoveryListExclusions = map[string]func(nativeID string) bool{
	"AWS::IAM::ManagedPolicy": func(id string) bool {
		return strings.HasPrefix(id, "arn:aws:iam::aws:policy/")
	},
	"AWS::KMS::Alias": func(id string) bool {
		return strings.HasPrefix(id, "alias/aws/")
	},
	// CloudControl's version list includes the $LATEST pseudo-version, which
	// is not a published version and whose read always fails.
	"AWS::Lambda::Version": func(id string) bool {
		return strings.HasSuffix(id, ":$LATEST")
	},
}

// LabelConfig returns the label extraction configuration for discovered AWS resources.
// Most AWS resources use the Name tag for labels, but some resources don't support tags
// or have a more natural identifier property.
func (p *Plugin) LabelConfig() pkgmodel.LabelConfig {
	return pkgmodel.LabelConfig{
		DefaultQuery: `$.Tags[?(@.Key=='Name')].Value`,
		ResourceOverrides: map[string]string{
			// IAM resources typically don't have Name tags
			"AWS::IAM::Policy":        "$.PolicyName",
			"AWS::IAM::ManagedPolicy": "$.ManagedPolicyName",
			"AWS::IAM::Role":          "$.RoleName",
			"AWS::IAM::User":          "$.UserName",
			"AWS::IAM::Group":         "$.GroupName",
			// Route53 records need both Name and Type to be unique (e.g., SOA vs NS for same domain)
			"AWS::Route53::RecordSet": "$['Name','Type']",
			// ACM certificates use the domain name as their natural label
			// (Name tags are not standard for ACM).
			"AWS::CertificateManager::Certificate": "$.DomainName",
			// S3 objects use Key property
			"AWS::S3::Object": "$.Key",
			// CloudTrail trails have no Name tag; use the trail name as the label.
			"AWS::CloudTrail::Trail": "$.TrailName",
			// Cloud Map namespaces and services are identified by their name;
			// their tags are not required to carry one.
			"AWS::ServiceDiscovery::PrivateDnsNamespace": "$.Name",
			"AWS::ServiceDiscovery::Service":             "$.Name",
			// Resources that represent relationships use parent IDs
			"AWS::EC2::VPCGatewayAttachment":          "$.VpcId",
			"AWS::EC2::SubnetRouteTableAssociation":   "$.SubnetId",
			"AWS::EC2::VPCEndpointServicePermissions": "$.ServiceId",
		},
	}
}

func (p *Plugin) Create(ctx context.Context, request *resource.CreateRequest) (*resource.CreateResult, error) {
	targetConfig := p.targetConfig(request.TargetConfig)
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
	targetConfig := p.targetConfig(request.TargetConfig)
	if registry.HasProvisioner(request.ResourceType, resource.OperationUpdate) {
		provisioner := registry.Get(request.ResourceType, resource.OperationUpdate, targetConfig)
		return provisioner.Update(ctx, request)
	}

	client, err := ccx.NewClient(targetConfig)
	if err != nil {
		return nil, err
	}

	return client.UpdateResource(ctx, request)
}

func (p *Plugin) Status(ctx context.Context, request *resource.StatusRequest) (*resource.StatusResult, error) {
	targetConfig := p.targetConfig(request.TargetConfig)
	if request.ResourceType != "" {
		if registry.HasProvisioner(request.ResourceType, resource.OperationCheckStatus) {
			provisioner := registry.Get(request.ResourceType, resource.OperationCheckStatus, targetConfig)
			return provisioner.Status(ctx, request)
		}
	}

	client, err := ccx.NewClient(targetConfig)
	if err != nil {
		return nil, err
	}

	return client.StatusResource(ctx, request, p.Read)
}

func (p *Plugin) Delete(ctx context.Context, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	targetConfig := p.targetConfig(request.TargetConfig)
	if registry.HasProvisioner(request.ResourceType, resource.OperationDelete) {
		provisioner := registry.Get(request.ResourceType, resource.OperationDelete, targetConfig)
		return provisioner.Delete(ctx, request)
	}

	client, err := ccx.NewClient(targetConfig)
	if err != nil {
		return nil, err
	}

	return client.DeleteResource(ctx, request)
}

func (p *Plugin) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	targetConfig := p.targetConfig(request.TargetConfig)
	if registry.HasProvisioner(request.ResourceType, resource.OperationRead) {
		provisioner := registry.Get(request.ResourceType, resource.OperationRead, targetConfig)
		return provisioner.Read(ctx, request)
	}

	client, err := ccx.NewClient(targetConfig)
	if err != nil {
		return nil, err
	}

	return client.ReadResource(ctx, request)
}

func (p *Plugin) List(ctx context.Context, request *resource.ListRequest) (*resource.ListResult, error) {
	targetConfig := p.targetConfig(request.TargetConfig)
	if registry.HasProvisioner(request.ResourceType, resource.OperationList) {
		provisioner := registry.Get(request.ResourceType, resource.OperationList, targetConfig)
		return provisioner.List(ctx, request)
	}

	client, err := ccx.NewClient(targetConfig)
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
	excluded := discoveryListExclusions[request.ResourceType]
	for _, r := range result.ResourceDescriptions {
		// CloudControl does not reliably filter by ResourceModel for all resource types.
		// Post-filter results to ensure each resource's properties match the requested filter.
		if len(request.AdditionalProperties) > 0 && r.Properties != nil {
			if !matchesFilter(*r.Properties, request.AdditionalProperties) {
				continue
			}
		}
		if excluded != nil && excluded(*r.Identifier) {
			continue
		}
		nativeIDs = append(nativeIDs, *r.Identifier)
	}

	return &resource.ListResult{
		NativeIDs:     nativeIDs,
		NextPageToken: result.NextToken,
	}, nil
}

// matchesFilter checks if a resource's properties (JSON string from CloudControl)
// match all the requested filter key-value pairs. This compensates for CloudControl
// not reliably honoring ResourceModel filters across all resource types.
func matchesFilter(propertiesJSON string, filter map[string]string) bool {
	var props map[string]json.RawMessage
	if err := json.Unmarshal([]byte(propertiesJSON), &props); err != nil {
		// If we can't parse properties, include the resource (don't filter out what we can't verify)
		return true
	}

	for filterKey, filterValue := range filter {
		rawValue, exists := props[filterKey]
		if !exists {
			// Property not in response — can't verify, include the resource
			continue
		}
		// Unmarshal the property value as a string for comparison
		var propValue string
		if err := json.Unmarshal(rawValue, &propValue); err != nil {
			// Not a simple string (could be object/array) — can't compare, include the resource
			continue
		}
		if !filterValuesEquivalent(propValue, filterValue) {
			return false
		}
	}
	return true
}

// filterValuesEquivalent reports whether a listed resource's property value and
// a filter value name the same thing. CloudControl echoes some properties in a
// different form than the one used to scope the list: Lambda::Permission, for
// example, lists with a FunctionName but echoes it back as the function ARN.
// Two values are equivalent when they are equal, or when one is an ARN whose
// trailing resource id equals the other.
func filterValuesEquivalent(propValue, filterValue string) bool {
	if propValue == filterValue {
		return true
	}
	if strings.HasPrefix(propValue, "arn:") && arn.IdFrom(propValue) == filterValue {
		return true
	}
	if strings.HasPrefix(filterValue, "arn:") && arn.IdFrom(filterValue) == propValue {
		return true
	}
	return false
}
