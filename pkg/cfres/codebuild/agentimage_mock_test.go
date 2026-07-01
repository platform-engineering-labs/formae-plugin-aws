// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package codebuild

import (
	"context"

	codebuildsdk "github.com/aws/aws-sdk-go-v2/service/codebuild"
	ecrsdk "github.com/aws/aws-sdk-go-v2/service/ecr"
	iamsdk "github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/stretchr/testify/mock"
)

type mockCodeBuildClient struct {
	mock.Mock
}

func (m *mockCodeBuildClient) BatchGetProjects(ctx context.Context, input *codebuildsdk.BatchGetProjectsInput, _ ...func(*codebuildsdk.Options)) (*codebuildsdk.BatchGetProjectsOutput, error) {
	args := m.Called(ctx, input)
	return args.Get(0).(*codebuildsdk.BatchGetProjectsOutput), args.Error(1)
}

func (m *mockCodeBuildClient) CreateProject(ctx context.Context, input *codebuildsdk.CreateProjectInput, _ ...func(*codebuildsdk.Options)) (*codebuildsdk.CreateProjectOutput, error) {
	args := m.Called(ctx, input)
	return args.Get(0).(*codebuildsdk.CreateProjectOutput), args.Error(1)
}

func (m *mockCodeBuildClient) UpdateProject(ctx context.Context, input *codebuildsdk.UpdateProjectInput, _ ...func(*codebuildsdk.Options)) (*codebuildsdk.UpdateProjectOutput, error) {
	args := m.Called(ctx, input)
	return args.Get(0).(*codebuildsdk.UpdateProjectOutput), args.Error(1)
}

func (m *mockCodeBuildClient) DeleteProject(ctx context.Context, input *codebuildsdk.DeleteProjectInput, _ ...func(*codebuildsdk.Options)) (*codebuildsdk.DeleteProjectOutput, error) {
	args := m.Called(ctx, input)
	return args.Get(0).(*codebuildsdk.DeleteProjectOutput), args.Error(1)
}

func (m *mockCodeBuildClient) StartBuild(ctx context.Context, input *codebuildsdk.StartBuildInput, _ ...func(*codebuildsdk.Options)) (*codebuildsdk.StartBuildOutput, error) {
	args := m.Called(ctx, input)
	return args.Get(0).(*codebuildsdk.StartBuildOutput), args.Error(1)
}

func (m *mockCodeBuildClient) BatchGetBuilds(ctx context.Context, input *codebuildsdk.BatchGetBuildsInput, _ ...func(*codebuildsdk.Options)) (*codebuildsdk.BatchGetBuildsOutput, error) {
	args := m.Called(ctx, input)
	return args.Get(0).(*codebuildsdk.BatchGetBuildsOutput), args.Error(1)
}

func (m *mockCodeBuildClient) ListBuildsForProject(ctx context.Context, input *codebuildsdk.ListBuildsForProjectInput, _ ...func(*codebuildsdk.Options)) (*codebuildsdk.ListBuildsForProjectOutput, error) {
	args := m.Called(ctx, input)
	return args.Get(0).(*codebuildsdk.ListBuildsForProjectOutput), args.Error(1)
}

func (m *mockCodeBuildClient) StopBuild(ctx context.Context, input *codebuildsdk.StopBuildInput, _ ...func(*codebuildsdk.Options)) (*codebuildsdk.StopBuildOutput, error) {
	args := m.Called(ctx, input)
	return args.Get(0).(*codebuildsdk.StopBuildOutput), args.Error(1)
}

type mockECRClient struct {
	mock.Mock
}

func (m *mockECRClient) DescribeImages(ctx context.Context, input *ecrsdk.DescribeImagesInput, _ ...func(*ecrsdk.Options)) (*ecrsdk.DescribeImagesOutput, error) {
	args := m.Called(ctx, input)
	return args.Get(0).(*ecrsdk.DescribeImagesOutput), args.Error(1)
}

func (m *mockECRClient) BatchDeleteImage(ctx context.Context, input *ecrsdk.BatchDeleteImageInput, _ ...func(*ecrsdk.Options)) (*ecrsdk.BatchDeleteImageOutput, error) {
	args := m.Called(ctx, input)
	return args.Get(0).(*ecrsdk.BatchDeleteImageOutput), args.Error(1)
}

type mockIAMClient struct {
	mock.Mock
}

func (m *mockIAMClient) GetRole(ctx context.Context, input *iamsdk.GetRoleInput, _ ...func(*iamsdk.Options)) (*iamsdk.GetRoleOutput, error) {
	args := m.Called(ctx, input)
	return args.Get(0).(*iamsdk.GetRoleOutput), args.Error(1)
}

func (m *mockIAMClient) CreateRole(ctx context.Context, input *iamsdk.CreateRoleInput, _ ...func(*iamsdk.Options)) (*iamsdk.CreateRoleOutput, error) {
	args := m.Called(ctx, input)
	return args.Get(0).(*iamsdk.CreateRoleOutput), args.Error(1)
}

func (m *mockIAMClient) PutRolePolicy(ctx context.Context, input *iamsdk.PutRolePolicyInput, _ ...func(*iamsdk.Options)) (*iamsdk.PutRolePolicyOutput, error) {
	args := m.Called(ctx, input)
	return args.Get(0).(*iamsdk.PutRolePolicyOutput), args.Error(1)
}

func (m *mockIAMClient) DeleteRolePolicy(ctx context.Context, input *iamsdk.DeleteRolePolicyInput, _ ...func(*iamsdk.Options)) (*iamsdk.DeleteRolePolicyOutput, error) {
	args := m.Called(ctx, input)
	return args.Get(0).(*iamsdk.DeleteRolePolicyOutput), args.Error(1)
}

func (m *mockIAMClient) DeleteRole(ctx context.Context, input *iamsdk.DeleteRoleInput, _ ...func(*iamsdk.Options)) (*iamsdk.DeleteRoleOutput, error) {
	args := m.Called(ctx, input)
	return args.Get(0).(*iamsdk.DeleteRoleOutput), args.Error(1)
}
