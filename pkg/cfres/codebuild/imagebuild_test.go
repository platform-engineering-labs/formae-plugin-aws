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
	ecrsdk "github.com/aws/aws-sdk-go-v2/service/ecr"
	ecrtypes "github.com/aws/aws-sdk-go-v2/service/ecr/types"
	iamsdk "github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/config"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
)

const testRepoURI = "123456789012.dkr.ecr.us-east-1.amazonaws.com/formae-agent"

func newTestProvisioner(cb *mockCodeBuildClient, ecr *mockECRClient, iam *mockIAMClient) *ImageBuild {
	return &ImageBuild{
		cfg:              &config.Config{Region: "us-east-1"},
		codeBuildFactory: func(*config.Config) (codeBuildClientInterface, error) { return cb, nil },
		ecrFactory:       func(*config.Config) (ecrClientInterface, error) { return ecr, nil },
		iamFactory:       func(*config.Config) (iamClientInterface, error) { return iam, nil },
		now:              func() time.Time { return time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC) },
		sleep:            func(time.Duration) {},
	}
}

func createProps(t *testing.T) json.RawMessage {
	t.Helper()
	js, err := json.Marshal(map[string]any{
		"EcrRepositoryUri": testRepoURI,
		"ImageTag":         "0.87.0-custom.1",
		"Dockerfile":       "FROM public.ecr.aws/docker/library/alpine:3.20\nRUN true\n",
		"BuildArgs":        map[string]string{"VERSION": "1.2.3"},
	})
	require.NoError(t, err)
	return js
}

func TestCreateCreatesRoleProjectAndStartsBuild(t *testing.T) {
	cb := &mockCodeBuildClient{}
	iam := &mockIAMClient{}
	p := newTestProvisioner(cb, nil, iam)

	iam.On("GetRole", mock.Anything, mock.Anything).Return(&iamsdk.GetRoleOutput{}, &iamtypes.NoSuchEntityException{})
	iam.On("CreateRole", mock.Anything, mock.Anything).Return(&iamsdk.CreateRoleOutput{
		Role: &iamtypes.Role{Arn: aws.String("arn:aws:iam::123456789012:role/formae-agentimg-x")},
	}, nil)
	iam.On("PutRolePolicy", mock.Anything, mock.Anything).Return(&iamsdk.PutRolePolicyOutput{}, nil)
	cb.On("BatchGetProjects", mock.Anything, mock.Anything).Return(&codebuildsdk.BatchGetProjectsOutput{}, nil)
	cb.On("CreateProject", mock.Anything, mock.Anything).Return(&codebuildsdk.CreateProjectOutput{}, nil)
	cb.On("StartBuild", mock.Anything, mock.Anything).Return(&codebuildsdk.StartBuildOutput{
		Build: &codebuildtypes.Build{Id: aws.String("proj:build-123")},
	}, nil)

	res, err := p.Create(context.Background(), &resource.CreateRequest{Properties: createProps(t)})
	require.NoError(t, err)
	pr := res.ProgressResult
	assert.Equal(t, resource.OperationStatusInProgress, pr.OperationStatus)
	assert.Equal(t, encodeNativeID(testRepoURI, "0.87.0-custom.1"), pr.NativeID)

	state, err := decodeRequestID(pr.RequestID)
	require.NoError(t, err)
	assert.Equal(t, "proj:build-123", state.BuildID)
	assert.Equal(t, string(resource.OperationCreate), state.Operation)
	assert.NotEmpty(t, state.BuildConfigHash)

	// The build env carries a base64 Dockerfile and the push target.
	startInput := cb.Calls[len(cb.Calls)-1].Arguments.Get(1).(*codebuildsdk.StartBuildInput)
	envByName := map[string]string{}
	for _, e := range startInput.EnvironmentVariablesOverride {
		envByName[aws.ToString(e.Name)] = aws.ToString(e.Value)
	}
	assert.NotEmpty(t, envByName[dockerfileEnvVar])
	assert.NotEmpty(t, envByName[buildArgsEnvVar])
	assert.Equal(t, testRepoURI+":0.87.0-custom.1", envByName[imageURIEnvVar])

	iam.AssertExpectations(t)
	cb.AssertExpectations(t)
}

func TestCreateWithByoRoleSkipsRoleManagement(t *testing.T) {
	cb := &mockCodeBuildClient{}
	iam := &mockIAMClient{}
	p := newTestProvisioner(cb, nil, iam)

	cb.On("BatchGetProjects", mock.Anything, mock.Anything).Return(&codebuildsdk.BatchGetProjectsOutput{}, nil)
	cb.On("CreateProject", mock.Anything, mock.Anything).Return(&codebuildsdk.CreateProjectOutput{}, nil)
	cb.On("StartBuild", mock.Anything, mock.Anything).Return(&codebuildsdk.StartBuildOutput{
		Build: &codebuildtypes.Build{Id: aws.String("proj:build-1")},
	}, nil)

	props, _ := json.Marshal(map[string]any{
		"EcrRepositoryUri": testRepoURI,
		"ImageTag":         "0.1.0",
		"Dockerfile":       "FROM public.ecr.aws/docker/library/alpine:3.20\n",
		"ServiceRoleArn":   "arn:aws:iam::123456789012:role/my-own-role",
	})
	_, err := p.Create(context.Background(), &resource.CreateRequest{Properties: props})
	require.NoError(t, err)

	// The provided role ARN is used and no role is created/mutated.
	iam.AssertNotCalled(t, "CreateRole", mock.Anything, mock.Anything)
	iam.AssertNotCalled(t, "PutRolePolicy", mock.Anything, mock.Anything)
	createInput := cb.Calls[1].Arguments.Get(1).(*codebuildsdk.CreateProjectInput)
	assert.Equal(t, "arn:aws:iam::123456789012:role/my-own-role", aws.ToString(createInput.ServiceRole))
}

// TestCreateRejectsCrossRegionRepository asserts a push target whose ECR region
// differs from the target region is rejected up front (the build project, its log
// group, and the ECR clients all run in the target region), before any build starts.
func TestCreateRejectsCrossRegionRepository(t *testing.T) {
	cb := &mockCodeBuildClient{}
	iam := &mockIAMClient{}
	p := newTestProvisioner(cb, nil, iam) // target region us-east-1

	props, _ := json.Marshal(map[string]any{
		"EcrRepositoryUri": "123456789012.dkr.ecr.us-west-2.amazonaws.com/formae-agent",
		"ImageTag":         "0.1.0",
		"Dockerfile":       "FROM public.ecr.aws/docker/library/alpine:3.20\n",
	})
	_, err := p.Create(context.Background(), &resource.CreateRequest{Properties: props})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must match the target region")
	cb.AssertNotCalled(t, "StartBuild", mock.Anything, mock.Anything)
}

// TestCreateRejectsCrossAccountRepository asserts a push target in a different
// account than the target (the account of the service role) is rejected before any
// build starts: the CodeBuild log group is created in the target account and the
// role only has permission there.
func TestCreateRejectsCrossAccountRepository(t *testing.T) {
	cb := &mockCodeBuildClient{}
	iam := &mockIAMClient{}
	p := newTestProvisioner(cb, nil, iam)

	iam.On("GetRole", mock.Anything, mock.Anything).Return(&iamsdk.GetRoleOutput{}, &iamtypes.NoSuchEntityException{})
	iam.On("CreateRole", mock.Anything, mock.Anything).Return(&iamsdk.CreateRoleOutput{
		Role: &iamtypes.Role{Arn: aws.String("arn:aws:iam::123456789012:role/formae-imgbuild-x")},
	}, nil)
	iam.On("PutRolePolicy", mock.Anything, mock.Anything).Return(&iamsdk.PutRolePolicyOutput{}, nil)

	props, _ := json.Marshal(map[string]any{
		"EcrRepositoryUri": "999999999999.dkr.ecr.us-east-1.amazonaws.com/formae-agent",
		"ImageTag":         "0.1.0",
		"Dockerfile":       "FROM public.ecr.aws/docker/library/alpine:3.20\n",
	})
	_, err := p.Create(context.Background(), &resource.CreateRequest{Properties: props})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must match the target account")
	cb.AssertNotCalled(t, "StartBuild", mock.Anything, mock.Anything)
}

func TestCreateProjectRetriesOnAssumeRolePropagation(t *testing.T) {
	cb := &mockCodeBuildClient{}
	iam := &mockIAMClient{}
	p := newTestProvisioner(cb, nil, iam)

	iam.On("GetRole", mock.Anything, mock.Anything).Return(&iamsdk.GetRoleOutput{
		Role: &iamtypes.Role{Arn: aws.String("arn:aws:iam::123456789012:role/formae-agentimg-x")},
	}, nil)
	iam.On("PutRolePolicy", mock.Anything, mock.Anything).Return(&iamsdk.PutRolePolicyOutput{}, nil)
	cb.On("BatchGetProjects", mock.Anything, mock.Anything).Return(&codebuildsdk.BatchGetProjectsOutput{}, nil)
	// First CreateProject fails with the propagation race, second succeeds.
	cb.On("CreateProject", mock.Anything, mock.Anything).
		Return(&codebuildsdk.CreateProjectOutput{}, &smithyAPIError{code: "InvalidInputException", msg: "CodeBuild is not authorized to perform: sts:AssumeRole on ..."}).Once()
	cb.On("CreateProject", mock.Anything, mock.Anything).Return(&codebuildsdk.CreateProjectOutput{}, nil).Once()
	cb.On("StartBuild", mock.Anything, mock.Anything).Return(&codebuildsdk.StartBuildOutput{
		Build: &codebuildtypes.Build{Id: aws.String("proj:build-1")},
	}, nil)

	_, err := p.Create(context.Background(), &resource.CreateRequest{Properties: createProps(t)})
	require.NoError(t, err)
	cb.AssertNumberOfCalls(t, "CreateProject", 2)
}

func TestStatusSucceededReturnsOutputs(t *testing.T) {
	cb := &mockCodeBuildClient{}
	p := newTestProvisioner(cb, nil, nil)

	cb.On("BatchGetBuilds", mock.Anything, mock.Anything).Return(&codebuildsdk.BatchGetBuildsOutput{
		Builds: []codebuildtypes.Build{{
			Id:          aws.String("proj:build-1"),
			BuildStatus: codebuildtypes.StatusTypeSucceeded,
			ExportedEnvironmentVariables: []codebuildtypes.ExportedEnvironmentVariable{
				{Name: aws.String(exportedDigestVar), Value: aws.String("sha256:deadbeef")},
				{Name: aws.String(exportedImageRefVar), Value: aws.String(testRepoURI + "@sha256:deadbeef")},
				{Name: aws.String(exportedImageURIVar), Value: aws.String(testRepoURI + ":0.1.0")},
			},
		}},
	}, nil)

	state := requestState{Operation: string(resource.OperationCreate), BuildID: "proj:build-1", RepoURI: testRepoURI, Tag: "0.1.0", Deadline: time.Date(2026, 7, 1, 1, 0, 0, 0, time.UTC), BuildConfigHash: "hash1"}
	res, err := p.Status(context.Background(), &resource.StatusRequest{RequestID: encodeRequestID(state)})
	require.NoError(t, err)
	assert.Equal(t, resource.OperationStatusSuccess, res.ProgressResult.OperationStatus)

	var out imageBuildOutputs
	require.NoError(t, json.Unmarshal(res.ProgressResult.ResourceProperties, &out))
	assert.Equal(t, "sha256:deadbeef", out.ImageDigest)
	assert.Equal(t, testRepoURI+"@sha256:deadbeef", out.ImageRef)
	assert.Equal(t, testRepoURI+":0.1.0", out.ImageURI)
	assert.Equal(t, "0.1.0", out.ImageTag)
	assert.Equal(t, "hash1", out.BuildConfigHash)
}

func TestStatusSucceededMissingDigestFails(t *testing.T) {
	cb := &mockCodeBuildClient{}
	p := newTestProvisioner(cb, nil, nil)
	cb.On("BatchGetBuilds", mock.Anything, mock.Anything).Return(&codebuildsdk.BatchGetBuildsOutput{
		Builds: []codebuildtypes.Build{{BuildStatus: codebuildtypes.StatusTypeSucceeded}},
	}, nil)
	state := requestState{Operation: "Create", BuildID: "b", RepoURI: testRepoURI, Tag: "0.1.0", Deadline: time.Now().Add(time.Hour)}
	res, err := p.Status(context.Background(), &resource.StatusRequest{RequestID: encodeRequestID(state)})
	require.NoError(t, err)
	assert.Equal(t, resource.OperationStatusFailure, res.ProgressResult.OperationStatus)
}

func TestStatusInProgressAndDeadline(t *testing.T) {
	cb := &mockCodeBuildClient{}
	p := newTestProvisioner(cb, nil, nil)
	cb.On("BatchGetBuilds", mock.Anything, mock.Anything).Return(&codebuildsdk.BatchGetBuildsOutput{
		Builds: []codebuildtypes.Build{{BuildStatus: codebuildtypes.StatusTypeInProgress, CurrentPhase: aws.String("BUILD")}},
	}, nil)

	// Before deadline → InProgress.
	future := requestState{Operation: "Create", BuildID: "b", RepoURI: testRepoURI, Tag: "0.1.0", Deadline: time.Date(2026, 7, 1, 1, 0, 0, 0, time.UTC)}
	res, err := p.Status(context.Background(), &resource.StatusRequest{RequestID: encodeRequestID(future)})
	require.NoError(t, err)
	assert.Equal(t, resource.OperationStatusInProgress, res.ProgressResult.OperationStatus)

	// Past deadline → Failure.
	past := requestState{Operation: "Create", BuildID: "b", RepoURI: testRepoURI, Tag: "0.1.0", Deadline: time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)}
	res, err = p.Status(context.Background(), &resource.StatusRequest{RequestID: encodeRequestID(past)})
	require.NoError(t, err)
	assert.Equal(t, resource.OperationStatusFailure, res.ProgressResult.OperationStatus)
}

func TestStatusFailedBuild(t *testing.T) {
	cb := &mockCodeBuildClient{}
	p := newTestProvisioner(cb, nil, nil)
	cb.On("BatchGetBuilds", mock.Anything, mock.Anything).Return(&codebuildsdk.BatchGetBuildsOutput{
		Builds: []codebuildtypes.Build{{BuildStatus: codebuildtypes.StatusTypeFailed, CurrentPhase: aws.String("BUILD")}},
	}, nil)
	state := requestState{Operation: "Create", BuildID: "b", RepoURI: testRepoURI, Tag: "0.1.0", Deadline: time.Now().Add(time.Hour)}
	res, err := p.Status(context.Background(), &resource.StatusRequest{RequestID: encodeRequestID(state)})
	require.NoError(t, err)
	assert.Equal(t, resource.OperationStatusFailure, res.ProgressResult.OperationStatus)
}

func TestReadFoundAndNotFound(t *testing.T) {
	ecr := &mockECRClient{}
	p := newTestProvisioner(nil, ecr, nil)
	ecr.On("DescribeImages", mock.Anything, mock.Anything).Return(&ecrsdk.DescribeImagesOutput{
		ImageDetails: []ecrtypes.ImageDetail{{ImageDigest: aws.String("sha256:cafe")}},
	}, nil).Once()

	res, err := p.Read(context.Background(), &resource.ReadRequest{NativeID: encodeNativeID(testRepoURI, "0.1.0"), ResourceType: resourceType})
	require.NoError(t, err)
	assert.Empty(t, res.ErrorCode)
	var out imageBuildOutputs
	require.NoError(t, json.Unmarshal([]byte(res.Properties), &out))
	assert.Equal(t, "sha256:cafe", out.ImageDigest)
	assert.Equal(t, testRepoURI+"@sha256:cafe", out.ImageRef)

	ecr.On("DescribeImages", mock.Anything, mock.Anything).Return(&ecrsdk.DescribeImagesOutput{}, &ecrtypes.ImageNotFoundException{}).Once()
	res, err = p.Read(context.Background(), &resource.ReadRequest{NativeID: encodeNativeID(testRepoURI, "missing"), ResourceType: resourceType})
	require.NoError(t, err)
	assert.Equal(t, resource.OperationErrorCodeNotFound, res.ErrorCode)
}

func TestUpdateNoopWhenHashUnchangedAndDigestPresent(t *testing.T) {
	ecr := &mockECRClient{}
	p := newTestProvisioner(nil, ecr, nil)

	desired := validInput()
	desiredJSON, _ := json.Marshal(map[string]any{
		"EcrRepositoryUri": desired.EcrRepositoryURI,
		"ImageTag":         desired.ImageTag,
		"Dockerfile":       desired.Dockerfile,
	})
	prior := imageBuildOutputs{BuildConfigHash: computeBuildConfigHash(desired), ImageDigest: "sha256:cafe", ImageRef: desired.EcrRepositoryURI + "@sha256:cafe"}
	priorJSON, _ := json.Marshal(prior)

	ecr.On("DescribeImages", mock.Anything, mock.Anything).Return(&ecrsdk.DescribeImagesOutput{
		ImageDetails: []ecrtypes.ImageDetail{{ImageDigest: aws.String("sha256:cafe")}},
	}, nil)

	res, err := p.Update(context.Background(), &resource.UpdateRequest{
		NativeID:          encodeNativeID(desired.EcrRepositoryURI, desired.ImageTag),
		PriorProperties:   priorJSON,
		DesiredProperties: desiredJSON,
	})
	require.NoError(t, err)
	assert.Equal(t, resource.OperationStatusSuccess, res.ProgressResult.OperationStatus)
}

func TestUpdateRebuildsWhenHashChanges(t *testing.T) {
	cb := &mockCodeBuildClient{}
	ecr := &mockECRClient{}
	iam := &mockIAMClient{}
	p := newTestProvisioner(cb, ecr, iam)

	iam.On("GetRole", mock.Anything, mock.Anything).Return(&iamsdk.GetRoleOutput{
		Role: &iamtypes.Role{Arn: aws.String("arn:aws:iam::123456789012:role/formae-agentimg-x")},
	}, nil)
	iam.On("PutRolePolicy", mock.Anything, mock.Anything).Return(&iamsdk.PutRolePolicyOutput{}, nil)
	cb.On("BatchGetProjects", mock.Anything, mock.Anything).Return(&codebuildsdk.BatchGetProjectsOutput{
		Projects: []codebuildtypes.Project{{Name: aws.String("p")}},
	}, nil)
	cb.On("UpdateProject", mock.Anything, mock.Anything).Return(&codebuildsdk.UpdateProjectOutput{}, nil)
	cb.On("StartBuild", mock.Anything, mock.Anything).Return(&codebuildsdk.StartBuildOutput{
		Build: &codebuildtypes.Build{Id: aws.String("proj:build-2")},
	}, nil)

	desiredJSON, _ := json.Marshal(map[string]any{
		"EcrRepositoryUri": testRepoURI,
		"ImageTag":         "0.1.0",
		"Dockerfile":       "FROM public.ecr.aws/docker/library/alpine:3.21\n",
	})
	priorJSON, _ := json.Marshal(imageBuildOutputs{BuildConfigHash: "stale-hash", ImageDigest: "sha256:old"})

	res, err := p.Update(context.Background(), &resource.UpdateRequest{
		NativeID:          encodeNativeID(testRepoURI, "0.1.0"),
		PriorProperties:   priorJSON,
		DesiredProperties: desiredJSON,
	})
	require.NoError(t, err)
	assert.Equal(t, resource.OperationStatusInProgress, res.ProgressResult.OperationStatus)
	cb.AssertCalled(t, "UpdateProject", mock.Anything, mock.Anything)

	// The rebuild carries the prior digest forward so Status can prune it.
	state, err := decodeRequestID(res.ProgressResult.RequestID)
	require.NoError(t, err)
	assert.Equal(t, "sha256:old", state.PriorDigest)
}

// TestStatusSucceededPrunesPriorDigestOnRebuild asserts that once an in-place
// rebuild succeeds, the now-untagged prior manifest is pruned so the repository
// stays empty enough to tear down.
func TestStatusSucceededPrunesPriorDigestOnRebuild(t *testing.T) {
	cb := &mockCodeBuildClient{}
	ecr := &mockECRClient{}
	p := newTestProvisioner(cb, ecr, nil)

	cb.On("BatchGetBuilds", mock.Anything, mock.Anything).Return(&codebuildsdk.BatchGetBuildsOutput{
		Builds: []codebuildtypes.Build{{
			Id:          aws.String("proj:build-9"),
			BuildStatus: codebuildtypes.StatusTypeSucceeded,
			ExportedEnvironmentVariables: []codebuildtypes.ExportedEnvironmentVariable{
				{Name: aws.String(exportedDigestVar), Value: aws.String("sha256:new")},
			},
		}},
	}, nil)
	// The prior digest is now untagged, so it is pruned.
	ecr.On("DescribeImages", mock.Anything, mock.MatchedBy(func(in *ecrsdk.DescribeImagesInput) bool {
		return len(in.ImageIds) == 1 && aws.ToString(in.ImageIds[0].ImageDigest) == "sha256:old"
	})).Return(&ecrsdk.DescribeImagesOutput{
		ImageDetails: []ecrtypes.ImageDetail{{ImageDigest: aws.String("sha256:old")}},
	}, nil)
	ecr.On("BatchDeleteImage", mock.Anything, mock.MatchedBy(func(in *ecrsdk.BatchDeleteImageInput) bool {
		return len(in.ImageIds) == 1 && aws.ToString(in.ImageIds[0].ImageDigest) == "sha256:old"
	})).Return(&ecrsdk.BatchDeleteImageOutput{}, nil)

	state := requestState{Operation: string(resource.OperationUpdate), BuildID: "proj:build-9", RepoURI: testRepoURI, Tag: "0.1.0", Deadline: time.Date(2026, 7, 1, 1, 0, 0, 0, time.UTC), BuildConfigHash: "h", PriorDigest: "sha256:old"}
	res, err := p.Status(context.Background(), &resource.StatusRequest{RequestID: encodeRequestID(state)})
	require.NoError(t, err)
	assert.Equal(t, resource.OperationStatusSuccess, res.ProgressResult.OperationStatus)
	ecr.AssertCalled(t, "BatchDeleteImage", mock.Anything, mock.Anything)
}

// TestStatusSucceededSkipsPruneWhenPriorStillTagged asserts the prune leaves a
// prior digest alone when it is still referenced by another tag, so an identical
// image shared in the repository is never deleted out from under its owner.
func TestStatusSucceededSkipsPruneWhenPriorStillTagged(t *testing.T) {
	cb := &mockCodeBuildClient{}
	ecr := &mockECRClient{}
	p := newTestProvisioner(cb, ecr, nil)

	cb.On("BatchGetBuilds", mock.Anything, mock.Anything).Return(&codebuildsdk.BatchGetBuildsOutput{
		Builds: []codebuildtypes.Build{{
			Id:          aws.String("proj:build-9"),
			BuildStatus: codebuildtypes.StatusTypeSucceeded,
			ExportedEnvironmentVariables: []codebuildtypes.ExportedEnvironmentVariable{
				{Name: aws.String(exportedDigestVar), Value: aws.String("sha256:new")},
			},
		}},
	}, nil)
	ecr.On("DescribeImages", mock.Anything, mock.Anything).Return(&ecrsdk.DescribeImagesOutput{
		ImageDetails: []ecrtypes.ImageDetail{{ImageDigest: aws.String("sha256:old"), ImageTags: []string{"other-tag"}}},
	}, nil)

	state := requestState{Operation: string(resource.OperationUpdate), BuildID: "proj:build-9", RepoURI: testRepoURI, Tag: "0.1.0", Deadline: time.Date(2026, 7, 1, 1, 0, 0, 0, time.UTC), BuildConfigHash: "h", PriorDigest: "sha256:old"}
	res, err := p.Status(context.Background(), &resource.StatusRequest{RequestID: encodeRequestID(state)})
	require.NoError(t, err)
	assert.Equal(t, resource.OperationStatusSuccess, res.ProgressResult.OperationStatus)
	ecr.AssertNotCalled(t, "BatchDeleteImage", mock.Anything, mock.Anything)
}

// TestUpdateRebuildsWhenTagDrifted asserts the no-op skip only fires when the
// declared tag still resolves to the recorded digest. If the tag was moved to a
// different image out of band, an Update rebuilds rather than reporting success
// against a stale reference.
func TestUpdateRebuildsWhenTagDrifted(t *testing.T) {
	cb := &mockCodeBuildClient{}
	ecr := &mockECRClient{}
	iam := &mockIAMClient{}
	p := newTestProvisioner(cb, ecr, iam)

	desired := validInput()
	desiredJSON, _ := json.Marshal(map[string]any{
		"EcrRepositoryUri": desired.EcrRepositoryURI,
		"ImageTag":         desired.ImageTag,
		"Dockerfile":       desired.Dockerfile,
	})
	prior := imageBuildOutputs{BuildConfigHash: computeBuildConfigHash(desired), ImageDigest: "sha256:original"}
	priorJSON, _ := json.Marshal(prior)

	// The tag now resolves to a different image than the one we built.
	ecr.On("DescribeImages", mock.Anything, mock.Anything).Return(&ecrsdk.DescribeImagesOutput{
		ImageDetails: []ecrtypes.ImageDetail{{ImageDigest: aws.String("sha256:drifted")}},
	}, nil)
	iam.On("GetRole", mock.Anything, mock.Anything).Return(&iamsdk.GetRoleOutput{
		Role: &iamtypes.Role{Arn: aws.String("arn:aws:iam::123456789012:role/formae-agentimg-x")},
	}, nil)
	iam.On("PutRolePolicy", mock.Anything, mock.Anything).Return(&iamsdk.PutRolePolicyOutput{}, nil)
	cb.On("BatchGetProjects", mock.Anything, mock.Anything).Return(&codebuildsdk.BatchGetProjectsOutput{
		Projects: []codebuildtypes.Project{{Name: aws.String("p")}},
	}, nil)
	cb.On("UpdateProject", mock.Anything, mock.Anything).Return(&codebuildsdk.UpdateProjectOutput{}, nil)
	cb.On("StartBuild", mock.Anything, mock.Anything).Return(&codebuildsdk.StartBuildOutput{
		Build: &codebuildtypes.Build{Id: aws.String("proj:build-3")},
	}, nil)

	res, err := p.Update(context.Background(), &resource.UpdateRequest{
		NativeID:          encodeNativeID(desired.EcrRepositoryURI, desired.ImageTag),
		PriorProperties:   priorJSON,
		DesiredProperties: desiredJSON,
	})
	require.NoError(t, err)
	assert.Equal(t, resource.OperationStatusInProgress, res.ProgressResult.OperationStatus)
	cb.AssertCalled(t, "StartBuild", mock.Anything, mock.Anything)
}

// TestDeleteToleratesMissingProject asserts an already-deleted CodeBuild project
// does not abort the delete: the pushed image and IAM role are still cleaned up so
// a partially-completed delete can be retried to success.
func TestDeleteToleratesMissingProject(t *testing.T) {
	cb := &mockCodeBuildClient{}
	ecr := &mockECRClient{}
	iam := &mockIAMClient{}
	p := newTestProvisioner(cb, ecr, iam)

	cb.On("ListBuildsForProject", mock.Anything, mock.Anything).Return(&codebuildsdk.ListBuildsForProjectOutput{}, nil)
	cb.On("DeleteProject", mock.Anything, mock.Anything).Return(&codebuildsdk.DeleteProjectOutput{}, &codebuildtypes.ResourceNotFoundException{})
	ecr.On("BatchDeleteImage", mock.Anything, mock.Anything).Return(&ecrsdk.BatchDeleteImageOutput{}, nil)
	iam.On("GetRole", mock.Anything, mock.Anything).Return(&iamsdk.GetRoleOutput{
		Role: &iamtypes.Role{Arn: aws.String("arn:aws:iam::123456789012:role/formae-agentimg-x")},
	}, nil)
	iam.On("DeleteRolePolicy", mock.Anything, mock.Anything).Return(&iamsdk.DeleteRolePolicyOutput{}, nil)
	iam.On("DeleteRole", mock.Anything, mock.Anything).Return(&iamsdk.DeleteRoleOutput{}, nil)

	res, err := p.Delete(context.Background(), &resource.DeleteRequest{NativeID: encodeNativeID(testRepoURI, "0.1.0")})
	require.NoError(t, err)
	assert.Equal(t, resource.OperationStatusSuccess, res.ProgressResult.OperationStatus)
	ecr.AssertCalled(t, "BatchDeleteImage", mock.Anything, mock.Anything)
	iam.AssertCalled(t, "DeleteRole", mock.Anything, mock.Anything)
}

func TestDeleteStopsBuildAndCleansUp(t *testing.T) {
	cb := &mockCodeBuildClient{}
	ecr := &mockECRClient{}
	iam := &mockIAMClient{}
	p := newTestProvisioner(cb, ecr, iam)

	cb.On("ListBuildsForProject", mock.Anything, mock.Anything).Return(&codebuildsdk.ListBuildsForProjectOutput{Ids: []string{"proj:build-1"}}, nil)
	cb.On("BatchGetBuilds", mock.Anything, mock.Anything).Return(&codebuildsdk.BatchGetBuildsOutput{
		Builds: []codebuildtypes.Build{{Id: aws.String("proj:build-1"), BuildStatus: codebuildtypes.StatusTypeInProgress}},
	}, nil)
	cb.On("StopBuild", mock.Anything, mock.Anything).Return(&codebuildsdk.StopBuildOutput{}, nil)
	cb.On("DeleteProject", mock.Anything, mock.Anything).Return(&codebuildsdk.DeleteProjectOutput{}, nil)
	ecr.On("BatchDeleteImage", mock.Anything, mock.Anything).Return(&ecrsdk.BatchDeleteImageOutput{}, nil)
	iam.On("GetRole", mock.Anything, mock.Anything).Return(&iamsdk.GetRoleOutput{
		Role: &iamtypes.Role{Arn: aws.String("arn:aws:iam::123456789012:role/formae-agentimg-x")},
	}, nil)
	iam.On("DeleteRolePolicy", mock.Anything, mock.Anything).Return(&iamsdk.DeleteRolePolicyOutput{}, nil)
	iam.On("DeleteRole", mock.Anything, mock.Anything).Return(&iamsdk.DeleteRoleOutput{}, nil)

	res, err := p.Delete(context.Background(), &resource.DeleteRequest{NativeID: encodeNativeID(testRepoURI, "0.1.0")})
	require.NoError(t, err)
	assert.Equal(t, resource.OperationStatusSuccess, res.ProgressResult.OperationStatus)
	cb.AssertCalled(t, "StopBuild", mock.Anything, mock.Anything)
	cb.AssertCalled(t, "DeleteProject", mock.Anything, mock.Anything)
	iam.AssertCalled(t, "DeleteRole", mock.Anything, mock.Anything)
}

// TestDeleteRemovesPushedImage asserts Delete empties the target repository of the
// image this resource pushed, scoped to exactly its own tag, so a co-managed ECR
// repository can be torn down after the build resource is gone.
func TestDeleteRemovesPushedImage(t *testing.T) {
	cb := &mockCodeBuildClient{}
	ecr := &mockECRClient{}
	iam := &mockIAMClient{}
	p := newTestProvisioner(cb, ecr, iam)

	cb.On("ListBuildsForProject", mock.Anything, mock.Anything).Return(&codebuildsdk.ListBuildsForProjectOutput{}, nil)
	cb.On("DeleteProject", mock.Anything, mock.Anything).Return(&codebuildsdk.DeleteProjectOutput{}, nil)
	iam.On("GetRole", mock.Anything, mock.Anything).Return(&iamsdk.GetRoleOutput{
		Role: &iamtypes.Role{Arn: aws.String("arn:aws:iam::123456789012:role/formae-agentimg-x")},
	}, nil)
	iam.On("DeleteRolePolicy", mock.Anything, mock.Anything).Return(&iamsdk.DeleteRolePolicyOutput{}, nil)
	iam.On("DeleteRole", mock.Anything, mock.Anything).Return(&iamsdk.DeleteRoleOutput{}, nil)
	ecr.On("BatchDeleteImage", mock.Anything, mock.MatchedBy(func(input *ecrsdk.BatchDeleteImageInput) bool {
		return aws.ToString(input.RepositoryName) == "formae-agent" &&
			len(input.ImageIds) == 1 &&
			aws.ToString(input.ImageIds[0].ImageTag) == "0.87.0-custom.1"
	})).Return(&ecrsdk.BatchDeleteImageOutput{}, nil)

	res, err := p.Delete(context.Background(), &resource.DeleteRequest{NativeID: encodeNativeID(testRepoURI, "0.87.0-custom.1")})
	require.NoError(t, err)
	assert.Equal(t, resource.OperationStatusSuccess, res.ProgressResult.OperationStatus)
	ecr.AssertCalled(t, "BatchDeleteImage", mock.Anything, mock.Anything)
}

func TestDeleteToleratesMissingRepositoryAndRole(t *testing.T) {
	cb := &mockCodeBuildClient{}
	ecr := &mockECRClient{}
	iam := &mockIAMClient{}
	p := newTestProvisioner(cb, ecr, iam)

	cb.On("ListBuildsForProject", mock.Anything, mock.Anything).Return(&codebuildsdk.ListBuildsForProjectOutput{}, nil)
	cb.On("DeleteProject", mock.Anything, mock.Anything).Return(&codebuildsdk.DeleteProjectOutput{}, nil)
	ecr.On("BatchDeleteImage", mock.Anything, mock.Anything).Return(&ecrsdk.BatchDeleteImageOutput{}, &ecrtypes.RepositoryNotFoundException{})
	// The internal role is already gone: it is not looked up as present, so no
	// deletion is attempted, and teardown still succeeds.
	iam.On("GetRole", mock.Anything, mock.Anything).Return(&iamsdk.GetRoleOutput{}, &iamtypes.NoSuchEntityException{})

	res, err := p.Delete(context.Background(), &resource.DeleteRequest{NativeID: encodeNativeID(testRepoURI, "0.1.0")})
	require.NoError(t, err)
	assert.Equal(t, resource.OperationStatusSuccess, res.ProgressResult.OperationStatus)
	iam.AssertNotCalled(t, "DeleteRole", mock.Anything, mock.Anything)
}

// TestDeleteSkipsInternalRoleForByo asserts that when the internally-named service
// role does not exist — the case for a BYO-role deployment, whose role has a
// caller-owned ARN and which may not grant the agent any IAM access — Delete leaves
// IAM entirely untouched (never deleting a caller-owned role) and still tears down
// the project and pushed image. A lookup that itself fails with AccessDenied is
// treated the same: nothing of ours to remove, teardown proceeds.
func TestDeleteSkipsInternalRoleForByo(t *testing.T) {
	for _, tc := range []struct {
		name       string
		getRoleErr error
	}{
		{"role-absent", &iamtypes.NoSuchEntityException{}},
		{"no-iam-access", &smithyAPIError{code: "AccessDenied", msg: "not authorized to perform: iam:GetRole"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cb := &mockCodeBuildClient{}
			ecr := &mockECRClient{}
			iam := &mockIAMClient{}
			p := newTestProvisioner(cb, ecr, iam)

			cb.On("ListBuildsForProject", mock.Anything, mock.Anything).Return(&codebuildsdk.ListBuildsForProjectOutput{}, nil)
			cb.On("DeleteProject", mock.Anything, mock.Anything).Return(&codebuildsdk.DeleteProjectOutput{}, nil)
			ecr.On("BatchDeleteImage", mock.Anything, mock.Anything).Return(&ecrsdk.BatchDeleteImageOutput{}, nil)
			iam.On("GetRole", mock.Anything, mock.Anything).Return(&iamsdk.GetRoleOutput{}, tc.getRoleErr)

			res, err := p.Delete(context.Background(), &resource.DeleteRequest{NativeID: encodeNativeID(testRepoURI, "0.1.0")})
			require.NoError(t, err)
			assert.Equal(t, resource.OperationStatusSuccess, res.ProgressResult.OperationStatus)
			iam.AssertNotCalled(t, "DeleteRolePolicy", mock.Anything, mock.Anything)
			iam.AssertNotCalled(t, "DeleteRole", mock.Anything, mock.Anything)
			cb.AssertCalled(t, "DeleteProject", mock.Anything, mock.Anything)
			ecr.AssertCalled(t, "BatchDeleteImage", mock.Anything, mock.Anything)
		})
	}
}

func TestNativeIDAndRequestIDRoundTrip(t *testing.T) {
	assert.Equal(t, testRepoURI+"|tag", encodeNativeID(testRepoURI, "tag"))
	repo, tag, err := parseNativeID(encodeNativeID(testRepoURI, "0.1.0"))
	require.NoError(t, err)
	assert.Equal(t, testRepoURI, repo)
	assert.Equal(t, "0.1.0", tag)

	_, _, err = parseNativeID("no-separator")
	assert.Error(t, err)

	state := requestState{Operation: "Create", BuildID: "proj:b-1", RepoURI: testRepoURI, Tag: "0.1.0", Deadline: time.Date(2026, 7, 1, 0, 30, 0, 0, time.UTC), BuildConfigHash: "abc", PriorDigest: "sha256:old"}
	got, err := decodeRequestID(encodeRequestID(state))
	require.NoError(t, err)
	assert.Equal(t, state.BuildID, got.BuildID)
	assert.Equal(t, state.RepoURI, got.RepoURI)
	assert.Equal(t, state.Tag, got.Tag)
	assert.Equal(t, state.BuildConfigHash, got.BuildConfigHash)
	assert.Equal(t, state.PriorDigest, got.PriorDigest)
	assert.True(t, state.Deadline.Equal(got.Deadline))

	_, err = decodeRequestID("too|few")
	assert.Error(t, err)
}

func TestListReturnsEmpty(t *testing.T) {
	p := newTestProvisioner(nil, nil, nil)
	res, err := p.List(context.Background(), &resource.ListRequest{})
	require.NoError(t, err)
	assert.Empty(t, res.NativeIDs)
}

// smithyAPIError is a minimal smithy.APIError for exercising error classification.
type smithyAPIError struct {
	code string
	msg  string
}

func (e *smithyAPIError) Error() string        { return e.code + ": " + e.msg }
func (e *smithyAPIError) ErrorCode() string    { return e.code }
func (e *smithyAPIError) ErrorMessage() string { return e.msg }
func (e *smithyAPIError) ErrorFault() smithy.ErrorFault {
	return smithy.FaultClient
}
