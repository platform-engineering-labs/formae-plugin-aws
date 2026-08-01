// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package codebuild

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	codebuildsdk "github.com/aws/aws-sdk-go-v2/service/codebuild"
	codebuildtypes "github.com/aws/aws-sdk-go-v2/service/codebuild/types"
	ecrsdk "github.com/aws/aws-sdk-go-v2/service/ecr"
	ecrtypes "github.com/aws/aws-sdk-go-v2/service/ecr/types"
	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/config"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
)

const (
	testRepoURI      = "123456789012.dkr.ecr.us-east-1.amazonaws.com/formae-agent"
	testBuildProject = "formae-plugin-sdk-test-image-build"
	testTag          = "0.87.0-custom.1"
)

// testNow is the clock the provisioner under test reads, so a derived deadline is
// an exact expected value rather than a range.
var testNow = time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)

func newTestProvisioner(cb *mockCodeBuildClient, ecr *mockECRClient) *ImageBuild {
	return &ImageBuild{
		cfg:              &config.Config{Region: "us-east-1"},
		codeBuildFactory: func(*config.Config) (codeBuildClientInterface, error) { return cb, nil },
		ecrFactory:       func(*config.Config) (ecrClientInterface, error) { return ecr, nil },
		now:              func() time.Time { return testNow },
	}
}

// validProject is a CodeBuild project configured the way an image build requires:
// a privileged Linux container with no source and no artifacts. Its buildspec is a
// placeholder — the plugin overrides it per build.
func validProject() codebuildtypes.Project {
	return codebuildtypes.Project{
		Name: aws.String(testBuildProject),
		Source: &codebuildtypes.ProjectSource{
			Type:      codebuildtypes.SourceTypeNoSource,
			Buildspec: aws.String("version: 0.2\nphases:\n  build:\n    commands:\n      - true\n"),
		},
		Artifacts: &codebuildtypes.ProjectArtifacts{Type: codebuildtypes.ArtifactsTypeNoArtifacts},
		Environment: &codebuildtypes.ProjectEnvironment{
			Type:           codebuildtypes.EnvironmentTypeLinuxContainer,
			ComputeType:    codebuildtypes.ComputeTypeBuildGeneral1Small,
			Image:          aws.String("aws/codebuild/standard:7.0"),
			PrivilegedMode: aws.Bool(true),
		},
		TimeoutInMinutes: aws.Int32(30),
	}
}

// expectProjectLookup stubs the single pre-flight read of the referenced project.
func expectProjectLookup(cb *mockCodeBuildClient, project codebuildtypes.Project) {
	cb.On("BatchGetProjects", mock.Anything, mock.MatchedBy(func(in *codebuildsdk.BatchGetProjectsInput) bool {
		return len(in.Names) == 1 && in.Names[0] == testBuildProject
	})).Return(&codebuildsdk.BatchGetProjectsOutput{Projects: []codebuildtypes.Project{project}}, nil)
}

func createProps(t *testing.T) json.RawMessage {
	t.Helper()
	js, err := json.Marshal(map[string]any{
		"EcrRepositoryUri": testRepoURI,
		"ImageTag":         testTag,
		"Dockerfile":       "FROM public.ecr.aws/docker/library/alpine:3.20\nRUN true\n",
		"BuildArgs":        map[string]string{"VERSION": "1.2.3"},
		"ProjectName":      testBuildProject,
	})
	require.NoError(t, err)
	return js
}

// TestCodeBuildClientHasNoProjectLifecycle asserts the resource's CodeBuild client
// cannot create, update or delete a project. The project is a declared resource of
// its own; an image build only references it.
func TestCodeBuildClientHasNoProjectLifecycle(t *testing.T) {
	iface := reflect.TypeOf((*codeBuildClientInterface)(nil)).Elem()
	names := make([]string, 0, iface.NumMethod())
	for i := range iface.NumMethod() {
		names = append(names, iface.Method(i).Name)
	}
	assert.ElementsMatch(t,
		[]string{"BatchGetProjects", "StartBuild", "BatchGetBuilds", "ListBuildsForProject", "StopBuild"},
		names)
}

func TestCreateStartsBuildOnReferencedProject(t *testing.T) {
	cb := &mockCodeBuildClient{}
	p := newTestProvisioner(cb, nil)

	expectProjectLookup(cb, validProject())
	cb.On("StartBuild", mock.Anything, mock.Anything).Return(&codebuildsdk.StartBuildOutput{
		Build: &codebuildtypes.Build{Id: aws.String("proj:build-123"), TimeoutInMinutes: aws.Int32(30)},
	}, nil)

	res, err := p.Create(context.Background(), &resource.CreateRequest{Properties: createProps(t)})
	require.NoError(t, err)
	pr := res.ProgressResult
	assert.Equal(t, resource.OperationStatusInProgress, pr.OperationStatus)
	assert.Equal(t, encodeNativeID(testRepoURI, testTag, testBuildProject), pr.NativeID)

	state, err := decodeRequestID(pr.RequestID)
	require.NoError(t, err)
	assert.Equal(t, "proj:build-123", state.BuildID)
	assert.Equal(t, string(resource.OperationCreate), state.Operation)
	assert.Equal(t, testBuildProject, state.ProjectName)
	assert.NotEmpty(t, state.BuildConfigHash)

	// The build runs on the referenced project, carries the plugin's generated
	// buildspec, and gets the Dockerfile and push target as environment overrides.
	startInput := cb.Calls[len(cb.Calls)-1].Arguments.Get(1).(*codebuildsdk.StartBuildInput)
	assert.Equal(t, testBuildProject, aws.ToString(startInput.ProjectName))
	assert.Equal(t, generateBuildspec(), aws.ToString(startInput.BuildspecOverride))
	envByName := map[string]string{}
	for _, e := range startInput.EnvironmentVariablesOverride {
		envByName[aws.ToString(e.Name)] = aws.ToString(e.Value)
	}
	assert.NotEmpty(t, envByName[dockerfileEnvVar])
	assert.NotEmpty(t, envByName[buildArgsEnvVar])
	assert.Equal(t, testRepoURI+":"+testTag, envByName[imageURIEnvVar])
	assert.Equal(t, testRepoURI, envByName[ecrRepositoryURIEnvVar])

	cb.AssertExpectations(t)
	cb.AssertNumberOfCalls(t, "BatchGetProjects", 1)
}

// TestCreateDeadlineComesFromStartBuildResponse asserts the poll deadline follows
// the timeout CodeBuild resolved for the build itself, not the timeout read during
// pre-flight, and falls back to the CodeBuild default when the response omits it.
func TestCreateDeadlineComesFromStartBuildResponse(t *testing.T) {
	for _, tc := range []struct {
		name    string
		build   *codebuildtypes.Build
		expment time.Duration
	}{
		{"from-response", &codebuildtypes.Build{Id: aws.String("b"), TimeoutInMinutes: aws.Int32(20)}, 20 * time.Minute},
		{"default-when-absent", &codebuildtypes.Build{Id: aws.String("b")}, defaultBuildTimeoutMinutes * time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cb := &mockCodeBuildClient{}
			p := newTestProvisioner(cb, nil)

			// The pre-flight project declares a different timeout, so a deadline
			// derived from it would be visibly wrong.
			project := validProject()
			project.TimeoutInMinutes = aws.Int32(55)
			expectProjectLookup(cb, project)
			cb.On("StartBuild", mock.Anything, mock.Anything).Return(&codebuildsdk.StartBuildOutput{Build: tc.build}, nil)

			res, err := p.Create(context.Background(), &resource.CreateRequest{Properties: createProps(t)})
			require.NoError(t, err)
			state, err := decodeRequestID(res.ProgressResult.RequestID)
			require.NoError(t, err)
			assert.True(t, testNow.Add(tc.expment+pollDeadlineBuffer).Equal(state.Deadline),
				"expected deadline %s, got %s", testNow.Add(tc.expment+pollDeadlineBuffer), state.Deadline)
		})
	}
}

// TestCreateRejectsCrossRegionRepository asserts a push target whose ECR region
// differs from the target region is rejected up front (the build project, its log
// group, and the ECR clients all run in the target region), before any build starts.
func TestCreateRejectsCrossRegionRepository(t *testing.T) {
	cb := &mockCodeBuildClient{}
	p := newTestProvisioner(cb, nil) // target region us-east-1

	props, _ := json.Marshal(map[string]any{
		"EcrRepositoryUri": "123456789012.dkr.ecr.us-west-2.amazonaws.com/formae-agent",
		"ImageTag":         "0.1.0",
		"Dockerfile":       "FROM public.ecr.aws/docker/library/alpine:3.20\n",
		"ProjectName":      testBuildProject,
	})
	_, err := p.Create(context.Background(), &resource.CreateRequest{Properties: props})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must match the target region")
	cb.AssertNotCalled(t, "StartBuild", mock.Anything, mock.Anything)
}

// TestCreateRejectsMissingProject asserts a reference to a project that does not
// exist fails fast, rather than being created as an undeclared side-effect.
func TestCreateRejectsMissingProject(t *testing.T) {
	cb := &mockCodeBuildClient{}
	p := newTestProvisioner(cb, nil)

	cb.On("BatchGetProjects", mock.Anything, mock.Anything).Return(&codebuildsdk.BatchGetProjectsOutput{
		ProjectsNotFound: []string{testBuildProject},
	}, nil)

	_, err := p.Create(context.Background(), &resource.CreateRequest{Properties: createProps(t)})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not exist")
	assert.Contains(t, err.Error(), testBuildProject)
	cb.AssertNotCalled(t, "StartBuild", mock.Anything, mock.Anything)
}

// TestCreateRejectsUnprivilegedProject asserts a project that cannot run Docker is
// rejected before a build is dispatched into a guaranteed failure.
func TestCreateRejectsUnprivilegedProject(t *testing.T) {
	for _, tc := range []struct {
		name           string
		privilegedMode *bool
	}{
		{"absent", nil},
		{"false", aws.Bool(false)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cb := &mockCodeBuildClient{}
			p := newTestProvisioner(cb, nil)

			project := validProject()
			project.Environment.PrivilegedMode = tc.privilegedMode
			expectProjectLookup(cb, project)

			_, err := p.Create(context.Background(), &resource.CreateRequest{Properties: createProps(t)})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "privilegedMode")
			cb.AssertNotCalled(t, "StartBuild", mock.Anything, mock.Anything)
		})
	}
}

// TestCreateRejectsNonLinuxContainerProject asserts the build environment type is
// checked: the generated buildspec is a Linux container shell script.
func TestCreateRejectsNonLinuxContainerProject(t *testing.T) {
	cb := &mockCodeBuildClient{}
	p := newTestProvisioner(cb, nil)

	project := validProject()
	project.Environment.Type = codebuildtypes.EnvironmentTypeArmContainer
	expectProjectLookup(cb, project)

	_, err := p.Create(context.Background(), &resource.CreateRequest{Properties: createProps(t)})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "LINUX_CONTAINER")
	cb.AssertNotCalled(t, "StartBuild", mock.Anything, mock.Anything)
}

// TestCreateRejectsProjectWithSourceOrArtifacts asserts the project must take no
// source and produce no artifacts: the plugin supplies everything per build and
// collects the result from the pushed image, not from an artifact.
func TestCreateRejectsProjectWithSourceOrArtifacts(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mutate  func(*codebuildtypes.Project)
		message string
	}{
		{"source", func(pr *codebuildtypes.Project) { pr.Source.Type = codebuildtypes.SourceTypeS3 }, "NO_SOURCE"},
		{"source-absent", func(pr *codebuildtypes.Project) { pr.Source = nil }, "NO_SOURCE"},
		{"artifacts", func(pr *codebuildtypes.Project) { pr.Artifacts.Type = codebuildtypes.ArtifactsTypeS3 }, "NO_ARTIFACTS"},
		{"artifacts-absent", func(pr *codebuildtypes.Project) { pr.Artifacts = nil }, "NO_ARTIFACTS"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cb := &mockCodeBuildClient{}
			p := newTestProvisioner(cb, nil)

			project := validProject()
			tc.mutate(&project)
			expectProjectLookup(cb, project)

			_, err := p.Create(context.Background(), &resource.CreateRequest{Properties: createProps(t)})
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.message)
			cb.AssertNotCalled(t, "StartBuild", mock.Anything, mock.Anything)
		})
	}
}

func TestStatusSucceededReturnsOutputs(t *testing.T) {
	cb := &mockCodeBuildClient{}
	p := newTestProvisioner(cb, nil)

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

	state := testRequestState()
	res, err := p.Status(context.Background(), &resource.StatusRequest{RequestID: encodeRequestID(state)})
	require.NoError(t, err)
	assert.Equal(t, resource.OperationStatusSuccess, res.ProgressResult.OperationStatus)
	assert.Equal(t, encodeNativeID(testRepoURI, "0.1.0", testBuildProject), res.ProgressResult.NativeID)

	var out imageBuildOutputs
	require.NoError(t, json.Unmarshal(res.ProgressResult.ResourceProperties, &out))
	assert.Equal(t, "sha256:deadbeef", out.ImageDigest)
	assert.Equal(t, testRepoURI+"@sha256:deadbeef", out.ImageRef)
	assert.Equal(t, testRepoURI+":0.1.0", out.ImageURI)
	assert.Equal(t, "0.1.0", out.ImageTag)
	assert.Equal(t, "hash1", out.BuildConfigHash)
}

// testRequestState is the poll state of an in-flight build of tag 0.1.0.
func testRequestState() requestState {
	return requestState{
		Operation:       string(resource.OperationCreate),
		BuildID:         "proj:build-1",
		RepoURI:         testRepoURI,
		Tag:             "0.1.0",
		ProjectName:     testBuildProject,
		Deadline:        time.Date(2026, 7, 1, 1, 0, 0, 0, time.UTC),
		BuildConfigHash: "hash1",
	}
}

func TestStatusSucceededMissingDigestFails(t *testing.T) {
	cb := &mockCodeBuildClient{}
	p := newTestProvisioner(cb, nil)
	cb.On("BatchGetBuilds", mock.Anything, mock.Anything).Return(&codebuildsdk.BatchGetBuildsOutput{
		Builds: []codebuildtypes.Build{{BuildStatus: codebuildtypes.StatusTypeSucceeded}},
	}, nil)
	res, err := p.Status(context.Background(), &resource.StatusRequest{RequestID: encodeRequestID(testRequestState())})
	require.NoError(t, err)
	assert.Equal(t, resource.OperationStatusFailure, res.ProgressResult.OperationStatus)
}

func TestStatusInProgressAndDeadline(t *testing.T) {
	cb := &mockCodeBuildClient{}
	p := newTestProvisioner(cb, nil)
	cb.On("BatchGetBuilds", mock.Anything, mock.Anything).Return(&codebuildsdk.BatchGetBuildsOutput{
		Builds: []codebuildtypes.Build{{BuildStatus: codebuildtypes.StatusTypeInProgress, CurrentPhase: aws.String("BUILD")}},
	}, nil)

	// Before deadline → InProgress.
	res, err := p.Status(context.Background(), &resource.StatusRequest{RequestID: encodeRequestID(testRequestState())})
	require.NoError(t, err)
	assert.Equal(t, resource.OperationStatusInProgress, res.ProgressResult.OperationStatus)

	// Past deadline → Failure.
	past := testRequestState()
	past.Deadline = time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)
	res, err = p.Status(context.Background(), &resource.StatusRequest{RequestID: encodeRequestID(past)})
	require.NoError(t, err)
	assert.Equal(t, resource.OperationStatusFailure, res.ProgressResult.OperationStatus)
}

func TestStatusFailedBuild(t *testing.T) {
	cb := &mockCodeBuildClient{}
	p := newTestProvisioner(cb, nil)
	cb.On("BatchGetBuilds", mock.Anything, mock.Anything).Return(&codebuildsdk.BatchGetBuildsOutput{
		Builds: []codebuildtypes.Build{{BuildStatus: codebuildtypes.StatusTypeFailed, CurrentPhase: aws.String("BUILD")}},
	}, nil)
	res, err := p.Status(context.Background(), &resource.StatusRequest{RequestID: encodeRequestID(testRequestState())})
	require.NoError(t, err)
	assert.Equal(t, resource.OperationStatusFailure, res.ProgressResult.OperationStatus)
}

func TestReadFoundAndNotFound(t *testing.T) {
	ecr := &mockECRClient{}
	p := newTestProvisioner(nil, ecr)
	ecr.On("DescribeImages", mock.Anything, mock.Anything).Return(&ecrsdk.DescribeImagesOutput{
		ImageDetails: []ecrtypes.ImageDetail{{ImageDigest: aws.String("sha256:cafe")}},
	}, nil).Once()

	res, err := p.Read(context.Background(), &resource.ReadRequest{
		NativeID: encodeNativeID(testRepoURI, "0.1.0", testBuildProject), ResourceType: resourceType,
	})
	require.NoError(t, err)
	assert.Empty(t, res.ErrorCode)
	var out imageBuildOutputs
	require.NoError(t, json.Unmarshal([]byte(res.Properties), &out))
	assert.Equal(t, "sha256:cafe", out.ImageDigest)
	assert.Equal(t, testRepoURI+"@sha256:cafe", out.ImageRef)

	ecr.On("DescribeImages", mock.Anything, mock.Anything).Return(&ecrsdk.DescribeImagesOutput{}, &ecrtypes.ImageNotFoundException{}).Once()
	res, err = p.Read(context.Background(), &resource.ReadRequest{
		NativeID: encodeNativeID(testRepoURI, "missing", testBuildProject), ResourceType: resourceType,
	})
	require.NoError(t, err)
	assert.Equal(t, resource.OperationErrorCodeNotFound, res.ErrorCode)
}

func updateProps(t *testing.T, in imageBuildInput) json.RawMessage {
	t.Helper()
	js, err := json.Marshal(map[string]any{
		"EcrRepositoryUri": in.EcrRepositoryURI,
		"ImageTag":         in.ImageTag,
		"Dockerfile":       in.Dockerfile,
		"ProjectName":      in.ProjectName,
	})
	require.NoError(t, err)
	return js
}

func TestUpdateNoopWhenHashUnchangedAndDigestPresent(t *testing.T) {
	cb := &mockCodeBuildClient{}
	ecr := &mockECRClient{}
	p := newTestProvisioner(cb, ecr)

	desired := validInput()
	prior := imageBuildOutputs{
		BuildConfigHash: computeBuildConfigHash(desired),
		ImageDigest:     "sha256:cafe",
		ImageRef:        desired.EcrRepositoryURI + "@sha256:cafe",
	}
	priorJSON, _ := json.Marshal(prior)

	expectProjectLookup(cb, validProject())
	ecr.On("DescribeImages", mock.Anything, mock.Anything).Return(&ecrsdk.DescribeImagesOutput{
		ImageDetails: []ecrtypes.ImageDetail{{ImageDigest: aws.String("sha256:cafe")}},
	}, nil)

	res, err := p.Update(context.Background(), &resource.UpdateRequest{
		NativeID:          encodeNativeID(desired.EcrRepositoryURI, desired.ImageTag, desired.ProjectName),
		PriorProperties:   priorJSON,
		DesiredProperties: updateProps(t, desired),
	})
	require.NoError(t, err)
	assert.Equal(t, resource.OperationStatusSuccess, res.ProgressResult.OperationStatus)
	cb.AssertNotCalled(t, "StartBuild", mock.Anything, mock.Anything)
	// The referenced project is validated even on the path that does not rebuild,
	// and it is read exactly once.
	cb.AssertNumberOfCalls(t, "BatchGetProjects", 1)
}

func TestUpdateRebuildsWhenHashChanges(t *testing.T) {
	cb := &mockCodeBuildClient{}
	ecr := &mockECRClient{}
	p := newTestProvisioner(cb, ecr)

	expectProjectLookup(cb, validProject())
	cb.On("StartBuild", mock.Anything, mock.Anything).Return(&codebuildsdk.StartBuildOutput{
		Build: &codebuildtypes.Build{Id: aws.String("proj:build-2"), TimeoutInMinutes: aws.Int32(30)},
	}, nil)

	desired := validInput()
	desired.Dockerfile = "FROM public.ecr.aws/docker/library/alpine:3.21\n"
	priorJSON, _ := json.Marshal(imageBuildOutputs{BuildConfigHash: "stale-hash", ImageDigest: "sha256:old"})

	res, err := p.Update(context.Background(), &resource.UpdateRequest{
		NativeID:          encodeNativeID(desired.EcrRepositoryURI, desired.ImageTag, desired.ProjectName),
		PriorProperties:   priorJSON,
		DesiredProperties: updateProps(t, desired),
	})
	require.NoError(t, err)
	assert.Equal(t, resource.OperationStatusInProgress, res.ProgressResult.OperationStatus)

	// The rebuild carries the prior digest forward so Status can prune it.
	state, err := decodeRequestID(res.ProgressResult.RequestID)
	require.NoError(t, err)
	assert.Equal(t, "sha256:old", state.PriorDigest)
	// A rebuild reuses the pre-flight read rather than fetching the project again.
	cb.AssertNumberOfCalls(t, "BatchGetProjects", 1)
}

// TestUpdateRebuildsWhenTagDrifted asserts the no-op skip only fires when the
// declared tag still resolves to the recorded digest. If the tag was moved to a
// different image out of band, an Update rebuilds rather than reporting success
// against a stale reference.
func TestUpdateRebuildsWhenTagDrifted(t *testing.T) {
	cb := &mockCodeBuildClient{}
	ecr := &mockECRClient{}
	p := newTestProvisioner(cb, ecr)

	desired := validInput()
	prior := imageBuildOutputs{BuildConfigHash: computeBuildConfigHash(desired), ImageDigest: "sha256:original"}
	priorJSON, _ := json.Marshal(prior)

	// The tag now resolves to a different image than the one we built.
	ecr.On("DescribeImages", mock.Anything, mock.Anything).Return(&ecrsdk.DescribeImagesOutput{
		ImageDetails: []ecrtypes.ImageDetail{{ImageDigest: aws.String("sha256:drifted")}},
	}, nil)
	expectProjectLookup(cb, validProject())
	cb.On("StartBuild", mock.Anything, mock.Anything).Return(&codebuildsdk.StartBuildOutput{
		Build: &codebuildtypes.Build{Id: aws.String("proj:build-3"), TimeoutInMinutes: aws.Int32(30)},
	}, nil)

	res, err := p.Update(context.Background(), &resource.UpdateRequest{
		NativeID:          encodeNativeID(desired.EcrRepositoryURI, desired.ImageTag, desired.ProjectName),
		PriorProperties:   priorJSON,
		DesiredProperties: updateProps(t, desired),
	})
	require.NoError(t, err)
	assert.Equal(t, resource.OperationStatusInProgress, res.ProgressResult.OperationStatus)
	cb.AssertCalled(t, "StartBuild", mock.Anything, mock.Anything)
}

// TestStatusSucceededPrunesPriorDigestOnRebuild asserts that once an in-place
// rebuild succeeds, the now-untagged prior manifest is pruned so the repository
// stays empty enough to tear down.
func TestStatusSucceededPrunesPriorDigestOnRebuild(t *testing.T) {
	cb := &mockCodeBuildClient{}
	ecr := &mockECRClient{}
	p := newTestProvisioner(cb, ecr)

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

	state := testRequestState()
	state.Operation = string(resource.OperationUpdate)
	state.BuildID = "proj:build-9"
	state.PriorDigest = "sha256:old"
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
	p := newTestProvisioner(cb, ecr)

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

	state := testRequestState()
	state.Operation = string(resource.OperationUpdate)
	state.BuildID = "proj:build-9"
	state.PriorDigest = "sha256:old"
	res, err := p.Status(context.Background(), &resource.StatusRequest{RequestID: encodeRequestID(state)})
	require.NoError(t, err)
	assert.Equal(t, resource.OperationStatusSuccess, res.ProgressResult.OperationStatus)
	ecr.AssertNotCalled(t, "BatchDeleteImage", mock.Anything, mock.Anything)
}

// buildWithImageURI is a build on the shared project, tagged with the push target
// it was started for exactly as the plugin sets it.
func buildWithImageURI(id, uri string, status codebuildtypes.StatusType) codebuildtypes.Build {
	return codebuildtypes.Build{
		Id:          aws.String(id),
		BuildStatus: status,
		Environment: &codebuildtypes.ProjectEnvironment{
			EnvironmentVariables: []codebuildtypes.EnvironmentVariable{
				{Name: aws.String(imageURIEnvVar), Value: aws.String(uri)},
			},
		},
	}
}

// TestDeleteStopsOnlyItsOwnBuilds asserts teardown stops the in-flight builds this
// resource started — identified by the push target they carry — and leaves the
// shared project's other builds, the project itself, and IAM untouched.
func TestDeleteStopsOnlyItsOwnBuilds(t *testing.T) {
	cb := &mockCodeBuildClient{}
	ecr := &mockECRClient{}
	p := newTestProvisioner(cb, ecr)

	cb.On("ListBuildsForProject", mock.Anything, mock.MatchedBy(func(in *codebuildsdk.ListBuildsForProjectInput) bool {
		return aws.ToString(in.ProjectName) == testBuildProject
	})).Return(&codebuildsdk.ListBuildsForProjectOutput{
		Ids: []string{"proj:mine", "proj:someone-else", "proj:mine-done", "proj:unlabelled"},
	}, nil)
	cb.On("BatchGetBuilds", mock.Anything, mock.Anything).Return(&codebuildsdk.BatchGetBuildsOutput{
		Builds: []codebuildtypes.Build{
			buildWithImageURI("proj:mine", testRepoURI+":"+testTag, codebuildtypes.StatusTypeInProgress),
			buildWithImageURI("proj:someone-else", testRepoURI+":other-tag", codebuildtypes.StatusTypeInProgress),
			buildWithImageURI("proj:mine-done", testRepoURI+":"+testTag, codebuildtypes.StatusTypeSucceeded),
			{Id: aws.String("proj:unlabelled"), BuildStatus: codebuildtypes.StatusTypeInProgress},
		},
	}, nil)
	cb.On("StopBuild", mock.Anything, mock.Anything).Return(&codebuildsdk.StopBuildOutput{}, nil)
	ecr.On("BatchDeleteImage", mock.Anything, mock.Anything).Return(&ecrsdk.BatchDeleteImageOutput{}, nil)

	res, err := p.Delete(context.Background(), &resource.DeleteRequest{
		NativeID: encodeNativeID(testRepoURI, testTag, testBuildProject),
	})
	require.NoError(t, err)
	assert.Equal(t, resource.OperationStatusSuccess, res.ProgressResult.OperationStatus)

	cb.AssertNumberOfCalls(t, "StopBuild", 1)
	stopped := cb.Calls[len(cb.Calls)-1].Arguments.Get(1).(*codebuildsdk.StopBuildInput)
	assert.Equal(t, "proj:mine", aws.ToString(stopped.Id))
	cb.AssertExpectations(t)
}

// TestDeleteRemovesPushedImage asserts Delete empties the target repository of the
// image this resource pushed, scoped to exactly its own tag, so a co-managed ECR
// repository can be torn down after the build resource is gone.
func TestDeleteRemovesPushedImage(t *testing.T) {
	cb := &mockCodeBuildClient{}
	ecr := &mockECRClient{}
	p := newTestProvisioner(cb, ecr)

	cb.On("ListBuildsForProject", mock.Anything, mock.Anything).Return(&codebuildsdk.ListBuildsForProjectOutput{}, nil)
	ecr.On("BatchDeleteImage", mock.Anything, mock.MatchedBy(func(input *ecrsdk.BatchDeleteImageInput) bool {
		return aws.ToString(input.RepositoryName) == "formae-agent" &&
			len(input.ImageIds) == 1 &&
			aws.ToString(input.ImageIds[0].ImageTag) == testTag
	})).Return(&ecrsdk.BatchDeleteImageOutput{}, nil)

	res, err := p.Delete(context.Background(), &resource.DeleteRequest{
		NativeID: encodeNativeID(testRepoURI, testTag, testBuildProject),
	})
	require.NoError(t, err)
	assert.Equal(t, resource.OperationStatusSuccess, res.ProgressResult.OperationStatus)
	ecr.AssertExpectations(t)
}

// TestDeleteSurfacesImageDeleteFailure asserts a per-image BatchDeleteImage failure
// (returned in Failures with an HTTP success, not as an error) is surfaced rather
// than reported as a successful teardown.
func TestDeleteSurfacesImageDeleteFailure(t *testing.T) {
	cb := &mockCodeBuildClient{}
	ecr := &mockECRClient{}
	p := newTestProvisioner(cb, ecr)

	cb.On("ListBuildsForProject", mock.Anything, mock.Anything).Return(&codebuildsdk.ListBuildsForProjectOutput{}, nil)
	ecr.On("BatchDeleteImage", mock.Anything, mock.Anything).Return(&ecrsdk.BatchDeleteImageOutput{
		Failures: []ecrtypes.ImageFailure{{
			FailureCode:   ecrtypes.ImageFailureCodeImageReferencedByManifestList,
			FailureReason: aws.String("image referenced by manifest list"),
		}},
	}, nil)

	_, err := p.Delete(context.Background(), &resource.DeleteRequest{
		NativeID: encodeNativeID(testRepoURI, "0.1.0", testBuildProject),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "deleting pushed image")
}

func TestDeleteToleratesMissingRepository(t *testing.T) {
	cb := &mockCodeBuildClient{}
	ecr := &mockECRClient{}
	p := newTestProvisioner(cb, ecr)

	cb.On("ListBuildsForProject", mock.Anything, mock.Anything).Return(&codebuildsdk.ListBuildsForProjectOutput{}, nil)
	ecr.On("BatchDeleteImage", mock.Anything, mock.Anything).Return(&ecrsdk.BatchDeleteImageOutput{}, &ecrtypes.RepositoryNotFoundException{})

	res, err := p.Delete(context.Background(), &resource.DeleteRequest{
		NativeID: encodeNativeID(testRepoURI, "0.1.0", testBuildProject),
	})
	require.NoError(t, err)
	assert.Equal(t, resource.OperationStatusSuccess, res.ProgressResult.OperationStatus)
}

func TestNativeIDAndRequestIDRoundTrip(t *testing.T) {
	assert.Equal(t, testRepoURI+"|tag|"+testBuildProject, encodeNativeID(testRepoURI, "tag", testBuildProject))
	repo, tag, project, err := parseNativeID(encodeNativeID(testRepoURI, "0.1.0", testBuildProject))
	require.NoError(t, err)
	assert.Equal(t, testRepoURI, repo)
	assert.Equal(t, "0.1.0", tag)
	assert.Equal(t, testBuildProject, project)

	for _, bad := range []string{"no-separator", "repo|tag", "repo|tag|", "|tag|project", "repo||project"} {
		_, _, _, err := parseNativeID(bad)
		assert.Error(t, err, "expected %q to be rejected", bad)
	}

	state := requestState{
		Operation: "Create", BuildID: "proj:b-1", RepoURI: testRepoURI, Tag: "0.1.0",
		ProjectName:     testBuildProject,
		Deadline:        time.Date(2026, 7, 1, 0, 30, 0, 0, time.UTC),
		BuildConfigHash: "abc",
		PriorDigest:     "sha256:old",
	}
	got, err := decodeRequestID(encodeRequestID(state))
	require.NoError(t, err)
	assert.Equal(t, state.BuildID, got.BuildID)
	assert.Equal(t, state.RepoURI, got.RepoURI)
	assert.Equal(t, state.Tag, got.Tag)
	assert.Equal(t, state.ProjectName, got.ProjectName)
	assert.Equal(t, state.BuildConfigHash, got.BuildConfigHash)
	assert.Equal(t, state.PriorDigest, got.PriorDigest)
	assert.True(t, state.Deadline.Equal(got.Deadline))

	_, err = decodeRequestID("too|few")
	assert.Error(t, err)
}

func TestListReturnsEmpty(t *testing.T) {
	p := newTestProvisioner(nil, nil)
	res, err := p.List(context.Background(), &resource.ListRequest{})
	require.NoError(t, err)
	assert.Empty(t, res.NativeIDs)
}

// TestIsAssumeRolePropagationError asserts the classifier still recognises the
// transient race that a retry clears, and does not burn the retry budget on a
// permanent input error that merely mentions the service role.
func TestIsAssumeRolePropagationError(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"not-authorized-to-assume", &smithyAPIError{code: "InvalidInputException", msg: "CodeBuild is not authorized to perform: sts:AssumeRole on arn:aws:iam::123456789012:role/r"}, true},
		{"cannot-be-assumed", &smithyAPIError{code: "InvalidInputException", msg: "CodeBuild is experiencing an issue: the role cannot be assumed"}, true},
		{"service-role-not-assumable", &smithyAPIError{code: "InvalidInputException", msg: "Invalid service role: the service role could not be assumed"}, true},
		{"service-role-permanent", &smithyAPIError{code: "InvalidInputException", msg: "Invalid service role: service role arn:aws:iam::123456789012:role/typo does not exist"}, false},
		{"other-code", &smithyAPIError{code: "AccessDeniedException", msg: "the role cannot be assumed"}, false},
		{"not-api-error", assert.AnError, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, isAssumeRolePropagationError(tc.err))
		})
	}
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
