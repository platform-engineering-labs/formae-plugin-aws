// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package servicediscovery

import (
	"context"

	servicediscoverysdk "github.com/aws/aws-sdk-go-v2/service/servicediscovery"
	"github.com/stretchr/testify/mock"
)

type mockServiceDiscoveryClient struct {
	mock.Mock
}

func (m *mockServiceDiscoveryClient) CreatePrivateDnsNamespace(ctx context.Context, input *servicediscoverysdk.CreatePrivateDnsNamespaceInput, _ ...func(*servicediscoverysdk.Options)) (*servicediscoverysdk.CreatePrivateDnsNamespaceOutput, error) {
	args := m.Called(ctx, input)
	out, _ := args.Get(0).(*servicediscoverysdk.CreatePrivateDnsNamespaceOutput)
	return out, args.Error(1)
}

func (m *mockServiceDiscoveryClient) UpdatePrivateDnsNamespace(ctx context.Context, input *servicediscoverysdk.UpdatePrivateDnsNamespaceInput, _ ...func(*servicediscoverysdk.Options)) (*servicediscoverysdk.UpdatePrivateDnsNamespaceOutput, error) {
	args := m.Called(ctx, input)
	out, _ := args.Get(0).(*servicediscoverysdk.UpdatePrivateDnsNamespaceOutput)
	return out, args.Error(1)
}

func (m *mockServiceDiscoveryClient) DeleteNamespace(ctx context.Context, input *servicediscoverysdk.DeleteNamespaceInput, _ ...func(*servicediscoverysdk.Options)) (*servicediscoverysdk.DeleteNamespaceOutput, error) {
	args := m.Called(ctx, input)
	out, _ := args.Get(0).(*servicediscoverysdk.DeleteNamespaceOutput)
	return out, args.Error(1)
}

func (m *mockServiceDiscoveryClient) GetNamespace(ctx context.Context, input *servicediscoverysdk.GetNamespaceInput, _ ...func(*servicediscoverysdk.Options)) (*servicediscoverysdk.GetNamespaceOutput, error) {
	args := m.Called(ctx, input)
	out, _ := args.Get(0).(*servicediscoverysdk.GetNamespaceOutput)
	return out, args.Error(1)
}

func (m *mockServiceDiscoveryClient) ListNamespaces(ctx context.Context, input *servicediscoverysdk.ListNamespacesInput, _ ...func(*servicediscoverysdk.Options)) (*servicediscoverysdk.ListNamespacesOutput, error) {
	args := m.Called(ctx, input)
	out, _ := args.Get(0).(*servicediscoverysdk.ListNamespacesOutput)
	return out, args.Error(1)
}

func (m *mockServiceDiscoveryClient) GetOperation(ctx context.Context, input *servicediscoverysdk.GetOperationInput, _ ...func(*servicediscoverysdk.Options)) (*servicediscoverysdk.GetOperationOutput, error) {
	args := m.Called(ctx, input)
	out, _ := args.Get(0).(*servicediscoverysdk.GetOperationOutput)
	return out, args.Error(1)
}

func (m *mockServiceDiscoveryClient) TagResource(ctx context.Context, input *servicediscoverysdk.TagResourceInput, _ ...func(*servicediscoverysdk.Options)) (*servicediscoverysdk.TagResourceOutput, error) {
	args := m.Called(ctx, input)
	out, _ := args.Get(0).(*servicediscoverysdk.TagResourceOutput)
	return out, args.Error(1)
}

func (m *mockServiceDiscoveryClient) UntagResource(ctx context.Context, input *servicediscoverysdk.UntagResourceInput, _ ...func(*servicediscoverysdk.Options)) (*servicediscoverysdk.UntagResourceOutput, error) {
	args := m.Called(ctx, input)
	out, _ := args.Get(0).(*servicediscoverysdk.UntagResourceOutput)
	return out, args.Error(1)
}

func (m *mockServiceDiscoveryClient) ListTagsForResource(ctx context.Context, input *servicediscoverysdk.ListTagsForResourceInput, _ ...func(*servicediscoverysdk.Options)) (*servicediscoverysdk.ListTagsForResourceOutput, error) {
	args := m.Called(ctx, input)
	out, _ := args.Get(0).(*servicediscoverysdk.ListTagsForResourceOutput)
	return out, args.Error(1)
}
