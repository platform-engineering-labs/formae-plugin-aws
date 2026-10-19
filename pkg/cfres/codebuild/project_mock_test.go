// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package codebuild

import (
	"context"

	codebuildsdk "github.com/aws/aws-sdk-go-v2/service/codebuild"
	"github.com/stretchr/testify/mock"
)

type mockProjectClient struct {
	mock.Mock
}

func (m *mockProjectClient) CreateProject(ctx context.Context, input *codebuildsdk.CreateProjectInput, _ ...func(*codebuildsdk.Options)) (*codebuildsdk.CreateProjectOutput, error) {
	args := m.Called(ctx, input)
	return args.Get(0).(*codebuildsdk.CreateProjectOutput), args.Error(1)
}

func (m *mockProjectClient) UpdateProject(ctx context.Context, input *codebuildsdk.UpdateProjectInput, _ ...func(*codebuildsdk.Options)) (*codebuildsdk.UpdateProjectOutput, error) {
	args := m.Called(ctx, input)
	return args.Get(0).(*codebuildsdk.UpdateProjectOutput), args.Error(1)
}

func (m *mockProjectClient) BatchGetProjects(ctx context.Context, input *codebuildsdk.BatchGetProjectsInput, _ ...func(*codebuildsdk.Options)) (*codebuildsdk.BatchGetProjectsOutput, error) {
	args := m.Called(ctx, input)
	return args.Get(0).(*codebuildsdk.BatchGetProjectsOutput), args.Error(1)
}

func (m *mockProjectClient) DeleteProject(ctx context.Context, input *codebuildsdk.DeleteProjectInput, _ ...func(*codebuildsdk.Options)) (*codebuildsdk.DeleteProjectOutput, error) {
	args := m.Called(ctx, input)
	return args.Get(0).(*codebuildsdk.DeleteProjectOutput), args.Error(1)
}
