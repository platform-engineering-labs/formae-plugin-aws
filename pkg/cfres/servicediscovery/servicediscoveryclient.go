// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package servicediscovery

import (
	"context"

	servicediscoverysdk "github.com/aws/aws-sdk-go-v2/service/servicediscovery"
)

// serviceDiscoveryClientInterface is the narrow surface of the Cloud Map SDK
// client this package uses. Defined explicitly (rather than aliased from the
// SDK) so unit tests can mock just the calls the provisioners actually make.
// *servicediscovery.Client satisfies it.
type serviceDiscoveryClientInterface interface {
	CreatePrivateDnsNamespace(ctx context.Context, params *servicediscoverysdk.CreatePrivateDnsNamespaceInput, optFns ...func(*servicediscoverysdk.Options)) (*servicediscoverysdk.CreatePrivateDnsNamespaceOutput, error)
	UpdatePrivateDnsNamespace(ctx context.Context, params *servicediscoverysdk.UpdatePrivateDnsNamespaceInput, optFns ...func(*servicediscoverysdk.Options)) (*servicediscoverysdk.UpdatePrivateDnsNamespaceOutput, error)
	DeleteNamespace(ctx context.Context, params *servicediscoverysdk.DeleteNamespaceInput, optFns ...func(*servicediscoverysdk.Options)) (*servicediscoverysdk.DeleteNamespaceOutput, error)
	GetNamespace(ctx context.Context, params *servicediscoverysdk.GetNamespaceInput, optFns ...func(*servicediscoverysdk.Options)) (*servicediscoverysdk.GetNamespaceOutput, error)
	ListNamespaces(ctx context.Context, params *servicediscoverysdk.ListNamespacesInput, optFns ...func(*servicediscoverysdk.Options)) (*servicediscoverysdk.ListNamespacesOutput, error)
	GetOperation(ctx context.Context, params *servicediscoverysdk.GetOperationInput, optFns ...func(*servicediscoverysdk.Options)) (*servicediscoverysdk.GetOperationOutput, error)
	TagResource(ctx context.Context, params *servicediscoverysdk.TagResourceInput, optFns ...func(*servicediscoverysdk.Options)) (*servicediscoverysdk.TagResourceOutput, error)
	UntagResource(ctx context.Context, params *servicediscoverysdk.UntagResourceInput, optFns ...func(*servicediscoverysdk.Options)) (*servicediscoverysdk.UntagResourceOutput, error)
	ListTagsForResource(ctx context.Context, params *servicediscoverysdk.ListTagsForResourceInput, optFns ...func(*servicediscoverysdk.Options)) (*servicediscoverysdk.ListTagsForResourceOutput, error)
}
