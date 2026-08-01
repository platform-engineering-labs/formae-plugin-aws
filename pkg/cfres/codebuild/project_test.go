// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package codebuild

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	codebuildsdk "github.com/aws/aws-sdk-go-v2/service/codebuild"
	codebuildtypes "github.com/aws/aws-sdk-go-v2/service/codebuild/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/config"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
)

const testProjectName = "formae-plugin-sdk-test-cb-project"

func newTestProject(retryDelay time.Duration) *Project {
	return &Project{
		cfg:           &config.Config{Region: "us-east-1"},
		retryAttempts: 3,
		retryDelay:    retryDelay,
	}
}

// declaredProjectProperties is the full property shape a forma sends, using the
// capitalized wire names the Pkl schema emits.
func declaredProjectProperties(t *testing.T) json.RawMessage {
	t.Helper()
	js, err := json.Marshal(map[string]any{
		"Name":        testProjectName,
		"Description": "plugin sdk test project",
		"ServiceRole": "arn:aws:iam::123456789012:role/codebuild-project-role",
		"Source": map[string]any{
			"Type":      "NO_SOURCE",
			"BuildSpec": "version: 0.2\n",
		},
		"Artifacts": map[string]any{"Type": "NO_ARTIFACTS"},
		"Environment": map[string]any{
			"Type":           "LINUX_CONTAINER",
			"ComputeType":    "BUILD_GENERAL1_SMALL",
			"Image":          "aws/codebuild/standard:7.0",
			"PrivilegedMode": true,
			"EnvironmentVariables": []map[string]any{
				{"Name": "PLUGIN_SDK_TEST_RUN_ID", "Value": "verify", "Type": "PLAINTEXT"},
			},
		},
		"LogsConfig": map[string]any{
			"CloudWatchLogs": map[string]any{
				"Status":    "ENABLED",
				"GroupName": "/formae/plugin-sdk-test",
			},
		},
		"TimeoutInMinutes": 20,
		"Tags": []map[string]any{
			{"Key": "Environment", "Value": "test"},
			{"Key": "Purpose", "Value": "plugin-sdk-conformance"},
		},
	})
	require.NoError(t, err)
	return js
}

// minimalProjectProperties declares only the properties the schema requires, so
// every optional property is absent.
func minimalProjectProperties(t *testing.T) json.RawMessage {
	t.Helper()
	js, err := json.Marshal(map[string]any{
		"Name":        testProjectName,
		"ServiceRole": "arn:aws:iam::123456789012:role/codebuild-project-role",
		"Source":      map[string]any{"Type": "NO_SOURCE", "BuildSpec": "version: 0.2\n"},
		"Artifacts":   map[string]any{"Type": "NO_ARTIFACTS"},
		"Environment": map[string]any{
			"Type":        "LINUX_CONTAINER",
			"ComputeType": "BUILD_GENERAL1_SMALL",
			"Image":       "aws/codebuild/standard:7.0",
		},
	})
	require.NoError(t, err)
	return js
}

// apiProject is what CodeBuild returns for the declared project: the declared
// configuration plus the ARN and the provider-populated defaults.
func apiProject() *codebuildtypes.Project {
	return &codebuildtypes.Project{
		Name:        aws.String(testProjectName),
		Arn:         aws.String("arn:aws:codebuild:us-east-1:123456789012:project/" + testProjectName),
		Description: aws.String("plugin sdk test project"),
		ServiceRole: aws.String("arn:aws:iam::123456789012:role/codebuild-project-role"),
		Source: &codebuildtypes.ProjectSource{
			Type:      codebuildtypes.SourceTypeNoSource,
			Buildspec: aws.String("version: 0.2\n"),
		},
		Artifacts: &codebuildtypes.ProjectArtifacts{
			Type:      codebuildtypes.ArtifactsTypeNoArtifacts,
			Packaging: codebuildtypes.ArtifactPackagingNone,
		},
		Environment: &codebuildtypes.ProjectEnvironment{
			Type:                     codebuildtypes.EnvironmentTypeLinuxContainer,
			ComputeType:              codebuildtypes.ComputeTypeBuildGeneral1Small,
			Image:                    aws.String("aws/codebuild/standard:7.0"),
			PrivilegedMode:           aws.Bool(true),
			ImagePullCredentialsType: codebuildtypes.ImagePullCredentialsTypeCodebuild,
			EnvironmentVariables: []codebuildtypes.EnvironmentVariable{
				{Name: aws.String("PLUGIN_SDK_TEST_RUN_ID"), Value: aws.String("verify"), Type: codebuildtypes.EnvironmentVariableTypePlaintext},
			},
		},
		Cache: &codebuildtypes.ProjectCache{Type: codebuildtypes.CacheTypeNoCache},
		LogsConfig: &codebuildtypes.LogsConfig{
			CloudWatchLogs: &codebuildtypes.CloudWatchLogsConfig{
				Status:    codebuildtypes.LogsConfigStatusTypeEnabled,
				GroupName: aws.String("/formae/plugin-sdk-test"),
			},
			S3Logs: &codebuildtypes.S3LogsConfig{
				Status:             codebuildtypes.LogsConfigStatusTypeDisabled,
				EncryptionDisabled: aws.Bool(false),
			},
		},
		TimeoutInMinutes:       aws.Int32(20),
		QueuedTimeoutInMinutes: aws.Int32(480),
		Tags: []codebuildtypes.Tag{
			{Key: aws.String("Environment"), Value: aws.String("test")},
			{Key: aws.String("Purpose"), Value: aws.String("plugin-sdk-conformance")},
		},
	}
}

func assumeRolePropagationError() error {
	return &smithyAPIError{
		code: "InvalidInputException",
		msg:  "CodeBuild is not authorized to perform: sts:AssumeRole on arn:aws:iam::123456789012:role/codebuild-project-role",
	}
}

func TestProjectCreateMapsPropertiesAndReturnsAPIResponse(t *testing.T) {
	client := &mockProjectClient{}
	p := newTestProject(time.Millisecond)

	client.On("CreateProject", mock.Anything, mock.Anything).
		Return(&codebuildsdk.CreateProjectOutput{Project: apiProject()}, nil).Once()

	res, err := p.createWithClient(context.Background(), client, &resource.CreateRequest{
		ResourceType: projectResourceType,
		Properties:   declaredProjectProperties(t),
	})
	require.NoError(t, err)

	pr := res.ProgressResult
	assert.Equal(t, resource.OperationCreate, pr.Operation)
	assert.Equal(t, resource.OperationStatusSuccess, pr.OperationStatus)
	assert.Equal(t, testProjectName, pr.NativeID)

	in := client.Calls[0].Arguments.Get(1).(*codebuildsdk.CreateProjectInput)
	assert.Equal(t, testProjectName, aws.ToString(in.Name))
	assert.Equal(t, "plugin sdk test project", aws.ToString(in.Description))
	assert.Equal(t, "arn:aws:iam::123456789012:role/codebuild-project-role", aws.ToString(in.ServiceRole))
	require.NotNil(t, in.Source)
	assert.Equal(t, codebuildtypes.SourceTypeNoSource, in.Source.Type)
	assert.Equal(t, "version: 0.2\n", aws.ToString(in.Source.Buildspec))
	require.NotNil(t, in.Artifacts)
	assert.Equal(t, codebuildtypes.ArtifactsTypeNoArtifacts, in.Artifacts.Type)
	require.NotNil(t, in.Environment)
	assert.Equal(t, codebuildtypes.EnvironmentTypeLinuxContainer, in.Environment.Type)
	assert.Equal(t, codebuildtypes.ComputeTypeBuildGeneral1Small, in.Environment.ComputeType)
	assert.Equal(t, "aws/codebuild/standard:7.0", aws.ToString(in.Environment.Image))
	assert.True(t, aws.ToBool(in.Environment.PrivilegedMode))
	require.Len(t, in.Environment.EnvironmentVariables, 1)
	assert.Equal(t, "PLUGIN_SDK_TEST_RUN_ID", aws.ToString(in.Environment.EnvironmentVariables[0].Name))
	assert.Equal(t, "verify", aws.ToString(in.Environment.EnvironmentVariables[0].Value))
	assert.Equal(t, codebuildtypes.EnvironmentVariableTypePlaintext, in.Environment.EnvironmentVariables[0].Type)
	require.NotNil(t, in.LogsConfig)
	require.NotNil(t, in.LogsConfig.CloudWatchLogs)
	assert.Equal(t, codebuildtypes.LogsConfigStatusTypeEnabled, in.LogsConfig.CloudWatchLogs.Status)
	assert.Equal(t, "/formae/plugin-sdk-test", aws.ToString(in.LogsConfig.CloudWatchLogs.GroupName))
	assert.Equal(t, int32(20), aws.ToInt32(in.TimeoutInMinutes))
	require.Len(t, in.Tags, 2)
	assert.Equal(t, "Environment", aws.ToString(in.Tags[0].Key))

	// The API response — not the declared properties — is what is persisted, so
	// the provider-populated ARN and defaults come back.
	var props map[string]any
	require.NoError(t, json.Unmarshal(pr.ResourceProperties, &props))
	assert.Equal(t, "arn:aws:codebuild:us-east-1:123456789012:project/"+testProjectName, props["Arn"])
	assert.Equal(t, map[string]any{"Type": "NO_CACHE"}, props["Cache"])
	assert.Equal(t, float64(480), props["QueuedTimeoutInMinutes"])
	assert.Equal(t, "NONE", props["Artifacts"].(map[string]any)["Packaging"])
	assert.Equal(t, "CODEBUILD", props["Environment"].(map[string]any)["ImagePullCredentialsType"])

	client.AssertExpectations(t)
}

func TestProjectCreateRetriesOnAssumeRolePropagation(t *testing.T) {
	client := &mockProjectClient{}
	p := newTestProject(time.Millisecond)

	client.On("CreateProject", mock.Anything, mock.Anything).
		Return(&codebuildsdk.CreateProjectOutput{}, assumeRolePropagationError()).Once()
	client.On("CreateProject", mock.Anything, mock.Anything).
		Return(&codebuildsdk.CreateProjectOutput{Project: apiProject()}, nil).Once()

	res, err := p.createWithClient(context.Background(), client, &resource.CreateRequest{
		ResourceType: projectResourceType,
		Properties:   declaredProjectProperties(t),
	})
	require.NoError(t, err)
	assert.Equal(t, resource.OperationStatusSuccess, res.ProgressResult.OperationStatus)
	client.AssertNumberOfCalls(t, "CreateProject", 2)
}

func TestProjectUpdateRetriesOnAssumeRolePropagation(t *testing.T) {
	client := &mockProjectClient{}
	p := newTestProject(time.Millisecond)

	client.On("UpdateProject", mock.Anything, mock.Anything).
		Return(&codebuildsdk.UpdateProjectOutput{}, assumeRolePropagationError()).Once()
	client.On("UpdateProject", mock.Anything, mock.Anything).
		Return(&codebuildsdk.UpdateProjectOutput{Project: apiProject()}, nil).Once()

	res, err := p.updateWithClient(context.Background(), client, &resource.UpdateRequest{
		ResourceType:      projectResourceType,
		NativeID:          testProjectName,
		DesiredProperties: declaredProjectProperties(t),
	})
	require.NoError(t, err)
	assert.Equal(t, resource.OperationStatusSuccess, res.ProgressResult.OperationStatus)
	assert.Equal(t, testProjectName, res.ProgressResult.NativeID)
	client.AssertNumberOfCalls(t, "UpdateProject", 2)
}

// TestProjectCreateCancelledDuringRetryWaitReturnsPromptly asserts the retry wait
// selects on ctx.Done(): a cancelled apply returns the context error long before
// the retry interval elapses, instead of blocking for the full wait.
func TestProjectCreateCancelledDuringRetryWaitReturnsPromptly(t *testing.T) {
	client := &mockProjectClient{}
	const retryDelay = 30 * time.Second
	p := newTestProject(retryDelay)

	client.On("CreateProject", mock.Anything, mock.Anything).
		Return(&codebuildsdk.CreateProjectOutput{}, assumeRolePropagationError())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	timer := time.AfterFunc(10*time.Millisecond, cancel)
	defer timer.Stop()

	start := time.Now()
	_, err := p.createWithClient(ctx, client, &resource.CreateRequest{
		ResourceType: projectResourceType,
		Properties:   declaredProjectProperties(t),
	})
	elapsed := time.Since(start)

	require.ErrorIs(t, err, context.Canceled)
	assert.Less(t, elapsed, retryDelay/2, "retry wait must abort on cancellation, not run to completion")
	client.AssertNumberOfCalls(t, "CreateProject", 1)
}

// TestProjectCreateRejectsExistingProject asserts a name collision on the very
// first attempt is a hard error: the project was not created by this apply, and
// nothing is adopted.
func TestProjectCreateRejectsExistingProject(t *testing.T) {
	client := &mockProjectClient{}
	p := newTestProject(time.Millisecond)

	client.On("CreateProject", mock.Anything, mock.Anything).
		Return(&codebuildsdk.CreateProjectOutput{}, &codebuildtypes.ResourceAlreadyExistsException{
			Message: aws.String("Project already exists: " + testProjectName),
		}).Once()

	_, err := p.createWithClient(context.Background(), client, &resource.CreateRequest{
		ResourceType: projectResourceType,
		Properties:   declaredProjectProperties(t),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), testProjectName)
	assert.Contains(t, err.Error(), "already exists")
	client.AssertNotCalled(t, "BatchGetProjects", mock.Anything, mock.Anything)
	client.AssertNumberOfCalls(t, "CreateProject", 1)
}

// TestProjectCreateAdoptsOwnLostCreateOnRetry asserts that a name collision seen
// only after one of our own retries means an earlier attempt succeeded
// server-side and lost its response: the project is read back and reported as
// created.
func TestProjectCreateAdoptsOwnLostCreateOnRetry(t *testing.T) {
	client := &mockProjectClient{}
	p := newTestProject(time.Millisecond)

	client.On("CreateProject", mock.Anything, mock.Anything).
		Return(&codebuildsdk.CreateProjectOutput{}, assumeRolePropagationError()).Once()
	client.On("CreateProject", mock.Anything, mock.Anything).
		Return(&codebuildsdk.CreateProjectOutput{}, &codebuildtypes.ResourceAlreadyExistsException{
			Message: aws.String("Project already exists: " + testProjectName),
		}).Once()
	client.On("BatchGetProjects", mock.Anything, mock.Anything).
		Return(&codebuildsdk.BatchGetProjectsOutput{Projects: []codebuildtypes.Project{*apiProject()}}, nil).Once()

	res, err := p.createWithClient(context.Background(), client, &resource.CreateRequest{
		ResourceType: projectResourceType,
		Properties:   declaredProjectProperties(t),
	})
	require.NoError(t, err)
	assert.Equal(t, resource.OperationStatusSuccess, res.ProgressResult.OperationStatus)
	assert.Equal(t, testProjectName, res.ProgressResult.NativeID)

	var props map[string]any
	require.NoError(t, json.Unmarshal(res.ProgressResult.ResourceProperties, &props))
	assert.Equal(t, "arn:aws:codebuild:us-east-1:123456789012:project/"+testProjectName, props["Arn"])
	client.AssertExpectations(t)
}

func TestProjectReadMapsProject(t *testing.T) {
	client := &mockProjectClient{}
	p := newTestProject(time.Millisecond)

	client.On("BatchGetProjects", mock.Anything, mock.Anything).
		Return(&codebuildsdk.BatchGetProjectsOutput{Projects: []codebuildtypes.Project{*apiProject()}}, nil).Once()

	res, err := p.readWithClient(context.Background(), client, &resource.ReadRequest{
		ResourceType: projectResourceType,
		NativeID:     testProjectName,
	})
	require.NoError(t, err)
	assert.Empty(t, res.ErrorCode)
	assert.Equal(t, projectResourceType, res.ResourceType)

	in := client.Calls[0].Arguments.Get(1).(*codebuildsdk.BatchGetProjectsInput)
	assert.Equal(t, []string{testProjectName}, in.Names)

	var props map[string]any
	require.NoError(t, json.Unmarshal([]byte(res.Properties), &props))
	assert.Equal(t, testProjectName, props["Name"])
	assert.Equal(t, "arn:aws:codebuild:us-east-1:123456789012:project/"+testProjectName, props["Arn"])
	assert.Equal(t, "plugin sdk test project", props["Description"])
	assert.Equal(t, "arn:aws:iam::123456789012:role/codebuild-project-role", props["ServiceRole"])
	assert.Equal(t, float64(20), props["TimeoutInMinutes"])
	assert.Equal(t, []any{
		map[string]any{"Key": "Environment", "Value": "test"},
		map[string]any{"Key": "Purpose", "Value": "plugin-sdk-conformance"},
	}, props["Tags"])
	assert.Equal(t, map[string]any{"Type": "NO_SOURCE", "BuildSpec": "version: 0.2\n"}, props["Source"])
	logs := props["LogsConfig"].(map[string]any)
	assert.Equal(t, map[string]any{"Status": "ENABLED", "GroupName": "/formae/plugin-sdk-test"}, logs["CloudWatchLogs"])
	assert.Equal(t, map[string]any{"Status": "DISABLED", "EncryptionDisabled": false}, logs["S3Logs"])
	env := props["Environment"].(map[string]any)
	assert.Equal(t, []any{map[string]any{"Name": "PLUGIN_SDK_TEST_RUN_ID", "Value": "verify", "Type": "PLAINTEXT"}}, env["EnvironmentVariables"])
}

func TestProjectReadMissingReturnsNotFound(t *testing.T) {
	for _, tc := range []struct {
		name string
		out  *codebuildsdk.BatchGetProjectsOutput
	}{
		{"no-projects", &codebuildsdk.BatchGetProjectsOutput{}},
		{"reported-not-found", &codebuildsdk.BatchGetProjectsOutput{ProjectsNotFound: []string{testProjectName}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &mockProjectClient{}
			p := newTestProject(time.Millisecond)
			client.On("BatchGetProjects", mock.Anything, mock.Anything).Return(tc.out, nil).Once()

			res, err := p.readWithClient(context.Background(), client, &resource.ReadRequest{
				ResourceType: projectResourceType,
				NativeID:     testProjectName,
			})
			require.NoError(t, err)
			assert.Equal(t, resource.OperationErrorCodeNotFound, res.ErrorCode)
			assert.Empty(t, res.Properties)
		})
	}
}

// TestProjectUpdateSendsClearingRepresentations asserts every optional property
// absent from the forma is sent in its clearing representation: UpdateProject
// leaves an omitted field untouched, so omitting one would silently keep a
// removed value alive.
func TestProjectUpdateSendsClearingRepresentations(t *testing.T) {
	client := &mockProjectClient{}
	p := newTestProject(time.Millisecond)

	client.On("UpdateProject", mock.Anything, mock.Anything).
		Return(&codebuildsdk.UpdateProjectOutput{Project: apiProject()}, nil).Once()

	_, err := p.updateWithClient(context.Background(), client, &resource.UpdateRequest{
		ResourceType:      projectResourceType,
		NativeID:          testProjectName,
		DesiredProperties: minimalProjectProperties(t),
	})
	require.NoError(t, err)

	in := client.Calls[0].Arguments.Get(1).(*codebuildsdk.UpdateProjectInput)
	assert.Equal(t, testProjectName, aws.ToString(in.Name))

	require.NotNil(t, in.Description)
	assert.Equal(t, "", aws.ToString(in.Description))

	require.NotNil(t, in.Cache)
	assert.Equal(t, codebuildtypes.CacheTypeNoCache, in.Cache.Type)

	require.NotNil(t, in.LogsConfig)
	require.NotNil(t, in.LogsConfig.CloudWatchLogs)
	assert.Equal(t, codebuildtypes.LogsConfigStatusTypeDisabled, in.LogsConfig.CloudWatchLogs.Status)
	require.NotNil(t, in.LogsConfig.S3Logs)
	assert.Equal(t, codebuildtypes.LogsConfigStatusTypeDisabled, in.LogsConfig.S3Logs.Status)

	require.NotNil(t, in.Environment)
	assert.NotNil(t, in.Environment.EnvironmentVariables)
	assert.Empty(t, in.Environment.EnvironmentVariables)

	assert.NotNil(t, in.Tags)
	assert.Empty(t, in.Tags)

	assert.Equal(t, int32(-1), aws.ToInt32(in.ConcurrentBuildLimit))
	assert.Equal(t, int32(defaultProjectTimeoutInMinutes), aws.ToInt32(in.TimeoutInMinutes))
	assert.Equal(t, int32(defaultProjectQueuedTimeoutInMinutes), aws.ToInt32(in.QueuedTimeoutInMinutes))
}

func TestProjectDelete(t *testing.T) {
	for _, tc := range []struct {
		name      string
		deleteErr error
	}{
		{"present", nil},
		{"already-gone", &codebuildtypes.ResourceNotFoundException{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &mockProjectClient{}
			p := newTestProject(time.Millisecond)
			client.On("DeleteProject", mock.Anything, mock.Anything).
				Return(&codebuildsdk.DeleteProjectOutput{}, tc.deleteErr).Once()

			res, err := p.deleteWithClient(context.Background(), client, &resource.DeleteRequest{
				ResourceType: projectResourceType,
				NativeID:     testProjectName,
			})
			require.NoError(t, err)
			assert.Equal(t, resource.OperationDelete, res.ProgressResult.Operation)
			assert.Equal(t, resource.OperationStatusSuccess, res.ProgressResult.OperationStatus)
			assert.Equal(t, testProjectName, res.ProgressResult.NativeID)

			in := client.Calls[0].Arguments.Get(1).(*codebuildsdk.DeleteProjectInput)
			assert.Equal(t, testProjectName, aws.ToString(in.Name))
		})
	}
}

func TestProjectListReturnsEmpty(t *testing.T) {
	p := newTestProject(time.Millisecond)
	res, err := p.List(context.Background(), &resource.ListRequest{ResourceType: projectResourceType})
	require.NoError(t, err)
	assert.Empty(t, res.NativeIDs)
}

func TestProjectStatusIsNotImplemented(t *testing.T) {
	p := newTestProject(time.Millisecond)
	_, err := p.Status(context.Background(), &resource.StatusRequest{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not implemented")
}
