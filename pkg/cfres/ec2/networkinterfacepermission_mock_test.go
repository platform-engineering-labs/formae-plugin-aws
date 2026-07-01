// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package ec2

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/stretchr/testify/mock"
)

type mockNetworkInterfacePermissionClient struct {
	mock.Mock
}

func (m *mockNetworkInterfacePermissionClient) CreateNetworkInterfacePermission(ctx context.Context, input *ec2.CreateNetworkInterfacePermissionInput, optFns ...func(*ec2.Options)) (*ec2.CreateNetworkInterfacePermissionOutput, error) {
	args := m.Called(ctx, input)
	return args.Get(0).(*ec2.CreateNetworkInterfacePermissionOutput), args.Error(1)
}

func (m *mockNetworkInterfacePermissionClient) DescribeNetworkInterfacePermissions(ctx context.Context, input *ec2.DescribeNetworkInterfacePermissionsInput, optFns ...func(*ec2.Options)) (*ec2.DescribeNetworkInterfacePermissionsOutput, error) {
	args := m.Called(ctx, input)
	return args.Get(0).(*ec2.DescribeNetworkInterfacePermissionsOutput), args.Error(1)
}

func (m *mockNetworkInterfacePermissionClient) DeleteNetworkInterfacePermission(ctx context.Context, input *ec2.DeleteNetworkInterfacePermissionInput, optFns ...func(*ec2.Options)) (*ec2.DeleteNetworkInterfacePermissionOutput, error) {
	args := m.Called(ctx, input)
	return args.Get(0).(*ec2.DeleteNetworkInterfacePermissionOutput), args.Error(1)
}
