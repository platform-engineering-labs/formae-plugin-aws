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

type mockEC2Client struct {
	mock.Mock
}

func (m *mockEC2Client) CreateNetworkAclEntry(ctx context.Context, input *ec2.CreateNetworkAclEntryInput, optFns ...func(*ec2.Options)) (*ec2.CreateNetworkAclEntryOutput, error) {
	args := m.Called(ctx, input)
	return args.Get(0).(*ec2.CreateNetworkAclEntryOutput), args.Error(1)
}

func (m *mockEC2Client) ReplaceNetworkAclEntry(ctx context.Context, input *ec2.ReplaceNetworkAclEntryInput, optFns ...func(*ec2.Options)) (*ec2.ReplaceNetworkAclEntryOutput, error) {
	args := m.Called(ctx, input)
	return args.Get(0).(*ec2.ReplaceNetworkAclEntryOutput), args.Error(1)
}

func (m *mockEC2Client) DeleteNetworkAclEntry(ctx context.Context, input *ec2.DeleteNetworkAclEntryInput, optFns ...func(*ec2.Options)) (*ec2.DeleteNetworkAclEntryOutput, error) {
	args := m.Called(ctx, input)
	return args.Get(0).(*ec2.DeleteNetworkAclEntryOutput), args.Error(1)
}

func (m *mockEC2Client) DescribeNetworkAcls(ctx context.Context, input *ec2.DescribeNetworkAclsInput, optFns ...func(*ec2.Options)) (*ec2.DescribeNetworkAclsOutput, error) {
	args := m.Called(ctx, input)
	return args.Get(0).(*ec2.DescribeNetworkAclsOutput), args.Error(1)
}
