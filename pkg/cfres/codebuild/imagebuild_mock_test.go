// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package codebuild

import (
	"context"

	codebuildsdk "github.com/aws/aws-sdk-go-v2/service/codebuild"
	ecrsdk "github.com/aws/aws-sdk-go-v2/service/ecr"
	"github.com/stretchr/testify/mock"
)

// mockCodeBuildClient implements the ImageBuild resource's client. It carries no
// CreateProject / UpdateProject / DeleteProject method, so a build path that tried
// to manage a project's lifecycle would not compile.
type mockCodeBuildClient struct {
	mock.Mock
}

func (m *mockCodeBuildClient) BatchGetProjects(ctx context.Context, input *codebuildsdk.BatchGetProjectsInput, _ ...func(*codebuildsdk.Options)) (*codebuildsdk.BatchGetProjectsOutput, error) {
	args := m.Called(ctx, input)
	return args.Get(0).(*codebuildsdk.BatchGetProjectsOutput), args.Error(1)
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
