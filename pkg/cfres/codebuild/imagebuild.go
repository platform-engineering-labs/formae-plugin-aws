// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package codebuild

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	codebuildsdk "github.com/aws/aws-sdk-go-v2/service/codebuild"
	codebuildtypes "github.com/aws/aws-sdk-go-v2/service/codebuild/types"
	ecrsdk "github.com/aws/aws-sdk-go-v2/service/ecr"
	ecrtypes "github.com/aws/aws-sdk-go-v2/service/ecr/types"

	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres/prov"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres/registry"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/config"
	"github.com/platform-engineering-labs/formae/pkg/plugin"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
)

const resourceType = "AWS::CodeBuild::ImageBuild"

// pollDeadlineBuffer is added to the build timeout to derive the engine's async
// poll deadline, so the plugin gives CodeBuild the full build timeout (plus
// provisioning slack) before declaring failure.
const pollDeadlineBuffer = 10 * time.Minute

// defaultBuildTimeoutMinutes is CodeBuild's own default build timeout, used to
// derive the poll deadline when StartBuild's response does not report the timeout
// it resolved for the build.
const defaultBuildTimeoutMinutes = 60

// codeBuildClientInterface is the subset of the CodeBuild API this resource uses.
// It carries no project lifecycle call: the build project is a declared resource
// of its own (AWS::CodeBuild::Project) that this resource only references.
// *codebuild.Client satisfies it.
type codeBuildClientInterface interface {
	BatchGetProjects(ctx context.Context, params *codebuildsdk.BatchGetProjectsInput, optFns ...func(*codebuildsdk.Options)) (*codebuildsdk.BatchGetProjectsOutput, error)
	StartBuild(ctx context.Context, params *codebuildsdk.StartBuildInput, optFns ...func(*codebuildsdk.Options)) (*codebuildsdk.StartBuildOutput, error)
	BatchGetBuilds(ctx context.Context, params *codebuildsdk.BatchGetBuildsInput, optFns ...func(*codebuildsdk.Options)) (*codebuildsdk.BatchGetBuildsOutput, error)
	ListBuildsForProject(ctx context.Context, params *codebuildsdk.ListBuildsForProjectInput, optFns ...func(*codebuildsdk.Options)) (*codebuildsdk.ListBuildsForProjectOutput, error)
	StopBuild(ctx context.Context, params *codebuildsdk.StopBuildInput, optFns ...func(*codebuildsdk.Options)) (*codebuildsdk.StopBuildOutput, error)
}

// ecrClientInterface is the subset of the ECR API this resource uses.
type ecrClientInterface interface {
	DescribeImages(ctx context.Context, params *ecrsdk.DescribeImagesInput, optFns ...func(*ecrsdk.Options)) (*ecrsdk.DescribeImagesOutput, error)
	BatchDeleteImage(ctx context.Context, params *ecrsdk.BatchDeleteImageInput, optFns ...func(*ecrsdk.Options)) (*ecrsdk.BatchDeleteImageOutput, error)
}

// ImageBuild is the synthetic build-during-apply provisioner that builds and
// pushes a container image from a caller-supplied Dockerfile. It runs the build on
// a declared AWS::CodeBuild::Project and creates no AWS resource of its own beyond
// the pushed image.
type ImageBuild struct {
	cfg *config.Config

	codeBuildFactory func(*config.Config) (codeBuildClientInterface, error)
	ecrFactory       func(*config.Config) (ecrClientInterface, error)

	now func() time.Time
}

var _ prov.Provisioner = &ImageBuild{}

func init() {
	registry.Register(resourceType,
		[]resource.Operation{
			resource.OperationCreate,
			resource.OperationUpdate,
			resource.OperationDelete,
			resource.OperationCheckStatus,
			resource.OperationRead,
			resource.OperationList,
		},
		func(cfg *config.Config) prov.Provisioner {
			return &ImageBuild{
				cfg:              cfg,
				codeBuildFactory: defaultCodeBuildFactory,
				ecrFactory:       defaultEcrFactory,
				now:              func() time.Time { return time.Now().UTC() },
			}
		})
}

func defaultCodeBuildFactory(cfg *config.Config) (codeBuildClientInterface, error) {
	awsCfg, err := cfg.ToAwsConfig(context.Background())
	if err != nil {
		return nil, err
	}
	return codebuildsdk.NewFromConfig(awsCfg), nil
}

func defaultEcrFactory(cfg *config.Config) (ecrClientInterface, error) {
	awsCfg, err := cfg.ToAwsConfig(context.Background())
	if err != nil {
		return nil, err
	}
	return ecrsdk.NewFromConfig(awsCfg), nil
}

// ── NativeID / RequestID codecs ─────────────────────────────────

// encodeNativeID joins the push target and the build project into the composite
// identity. None of the repository URI, the tag, or the project name can contain
// '|', so SplitN round-trips cleanly.
func encodeNativeID(repoURI, tag, projectName string) string {
	return repoURI + "|" + tag + "|" + projectName
}

func parseNativeID(nativeID string) (repoURI, tag, projectName string, err error) {
	parts := strings.SplitN(nativeID, "|", 3)
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", "", "", fmt.Errorf("invalid NativeID %q: expected <repositoryUri>|<tag>|<projectName>", nativeID)
	}
	return parts[0], parts[1], parts[2], nil
}

// requestState is what a build's RequestID carries so Status can poll the exact
// build and reconstruct the outputs without any other persisted state. PriorDigest
// is the digest this resource previously had under the same tag (empty on Create);
// once the new build succeeds Status prunes it so an in-place rebuild does not leave
// the old manifest behind as an untagged image. None of the fields can contain '|'
// (a repository URI, a tag, a project name, an RFC3339 time, a hex hash, and a
// sha256: digest).
type requestState struct {
	Operation       string
	BuildID         string
	RepoURI         string
	Tag             string
	ProjectName     string
	Deadline        time.Time
	BuildConfigHash string
	PriorDigest     string
}

func encodeRequestID(s requestState) string {
	return strings.Join([]string{
		s.Operation,
		s.BuildID,
		s.RepoURI,
		s.Tag,
		s.ProjectName,
		s.Deadline.UTC().Format(time.RFC3339),
		s.BuildConfigHash,
		s.PriorDigest,
	}, "|")
}

func decodeRequestID(requestID string) (requestState, error) {
	parts := strings.SplitN(requestID, "|", 8)
	if len(parts) != 8 {
		return requestState{}, fmt.Errorf("invalid RequestID %q", requestID)
	}
	deadline, err := time.Parse(time.RFC3339, parts[5])
	if err != nil {
		return requestState{}, fmt.Errorf("invalid deadline in RequestID: %w", err)
	}
	return requestState{
		Operation:       parts[0],
		BuildID:         parts[1],
		RepoURI:         parts[2],
		Tag:             parts[3],
		ProjectName:     parts[4],
		Deadline:        deadline,
		BuildConfigHash: parts[6],
		PriorDigest:     parts[7],
	}, nil
}

// ── Create ──────────────────────────────────────────────────────

func (a *ImageBuild) Create(ctx context.Context, request *resource.CreateRequest) (*resource.CreateResult, error) {
	var in imageBuildInput
	if err := json.Unmarshal(request.Properties, &in); err != nil {
		return nil, fmt.Errorf("ImageBuild: invalid Properties: %w", err)
	}
	if err := validateInput(in); err != nil {
		return nil, fmt.Errorf("ImageBuild: %w", err)
	}
	client, ref, project, err := a.preflight(ctx, in)
	if err != nil {
		return nil, err
	}
	pr, err := a.startBuild(ctx, client, in, ref, project, resource.OperationCreate, "")
	if err != nil {
		return nil, err
	}
	return &resource.CreateResult{ProgressResult: pr}, nil
}

// preflight resolves the push target and the referenced build project, rejecting
// anything the build could not possibly succeed against. It performs the single
// read of the project; every path that needs the project takes it from here rather
// than fetching it again.
//
// Preconditions that cannot be checked cheaply are deliberately left to the build:
// whether the project's VPC configuration has egress to ECR, STS and CloudWatch
// Logs, and whether its service role actually grants the ECR pushes, surface as
// real AWS errors from the build itself.
func (a *ImageBuild) preflight(ctx context.Context, in imageBuildInput) (codeBuildClientInterface, ecrRepositoryRef, *codebuildtypes.Project, error) {
	ref, err := parseEcrRepositoryURI(in.EcrRepositoryURI)
	if err != nil {
		return nil, ecrRepositoryRef{}, nil, fmt.Errorf("ImageBuild: %w", err)
	}
	// The build project, its log group, and the ECR API clients all run in the
	// target's region. The push target must therefore be an ECR repository in that
	// same region (v1 also assumes the same account); a cross-region repository
	// would build but then fail to be read or deleted.
	if a.cfg.Region != "" && ref.Region != a.cfg.Region {
		return nil, ecrRepositoryRef{}, nil, fmt.Errorf("ImageBuild: ecrRepositoryUri region %q must match the target region %q", ref.Region, a.cfg.Region)
	}

	client, err := a.codeBuildFactory(a.cfg)
	if err != nil {
		return nil, ecrRepositoryRef{}, nil, err
	}
	project, err := preflightProject(ctx, client, in.ProjectName)
	if err != nil {
		return nil, ecrRepositoryRef{}, nil, err
	}
	return client, ref, project, nil
}

// preflightProject reads the referenced project and rejects a configuration the
// generated build cannot run under, so a misconfigured project fails immediately
// with an actionable message rather than after a full build attempt.
func preflightProject(ctx context.Context, client codeBuildClientInterface, projectName string) (*codebuildtypes.Project, error) {
	project, err := getProject(ctx, client, projectName)
	if err != nil {
		return nil, fmt.Errorf("ImageBuild: looking up build project %q: %w", projectName, err)
	}
	if project == nil {
		return nil, fmt.Errorf("ImageBuild: build project %q does not exist; declare it as an AWS::CodeBuild::Project", projectName)
	}
	env := project.Environment
	// The build runs docker itself, so the project's environment must be privileged.
	if env == nil || !aws.ToBool(env.PrivilegedMode) {
		return nil, fmt.Errorf("ImageBuild: build project %q must set environment.privilegedMode to true to run a docker build", projectName)
	}
	// The generated buildspec is a Linux container shell script.
	if env.Type != codebuildtypes.EnvironmentTypeLinuxContainer {
		return nil, fmt.Errorf("ImageBuild: build project %q must use environment.type %q, got %q",
			projectName, codebuildtypes.EnvironmentTypeLinuxContainer, env.Type)
	}
	// Everything the build needs arrives per build, and its result is the pushed
	// image rather than an artifact.
	if project.Source == nil || project.Source.Type != codebuildtypes.SourceTypeNoSource {
		return nil, fmt.Errorf("ImageBuild: build project %q must use source.type %q", projectName, codebuildtypes.SourceTypeNoSource)
	}
	if project.Artifacts == nil || project.Artifacts.Type != codebuildtypes.ArtifactsTypeNoArtifacts {
		return nil, fmt.Errorf("ImageBuild: build project %q must use artifacts.type %q", projectName, codebuildtypes.ArtifactsTypeNoArtifacts)
	}
	return project, nil
}

// startBuild dispatches a build on the pre-flight-validated project and returns an
// InProgress ProgressResult carrying the poll state. priorDigest is the digest
// currently recorded under the tag (empty on Create); it is carried through so
// Status can prune it once the new build succeeds.
func (a *ImageBuild) startBuild(ctx context.Context, client codeBuildClientInterface, in imageBuildInput, ref ecrRepositoryRef, project *codebuildtypes.Project, op resource.Operation, priorDigest string) (*resource.ProgressResult, error) {
	projectName := aws.ToString(project.Name)

	buildID, timeoutMinutes, err := a.dispatchBuild(ctx, client, projectName, ref, in.ImageTag, in.Dockerfile, in.BuildArgs)
	if err != nil {
		return nil, err
	}
	plugin.LoggerFromContext(ctx).Info("ImageBuild: build started",
		"project", projectName, "buildId", buildID, "imageUri", imageURI(ref.URI, in.ImageTag))

	deadline := a.now().Add(time.Duration(timeoutMinutes)*time.Minute + pollDeadlineBuffer)
	state := requestState{
		Operation:       string(op),
		BuildID:         buildID,
		RepoURI:         ref.URI,
		Tag:             in.ImageTag,
		ProjectName:     projectName,
		Deadline:        deadline,
		BuildConfigHash: computeBuildConfigHash(in),
		PriorDigest:     priorDigest,
	}
	return &resource.ProgressResult{
		Operation:       op,
		OperationStatus: resource.OperationStatusInProgress,
		NativeID:        encodeNativeID(ref.URI, in.ImageTag, projectName),
		RequestID:       encodeRequestID(state),
	}, nil
}

// dispatchBuild starts the build with the generated buildspec and the per-build
// environment overrides, and returns the build id along with the timeout CodeBuild
// resolved for it. The project's own buildspec is never read or written: the build
// this resource runs is defined entirely by the override.
func (a *ImageBuild) dispatchBuild(ctx context.Context, client codeBuildClientInterface, projectName string, ref ecrRepositoryRef, tag, dockerfile string, buildArgs map[string]string) (string, int32, error) {
	out, err := client.StartBuild(ctx, &codebuildsdk.StartBuildInput{
		ProjectName:       aws.String(projectName),
		BuildspecOverride: aws.String(generateBuildspec()),
		EnvironmentVariablesOverride: []codebuildtypes.EnvironmentVariable{
			{Name: aws.String(dockerfileEnvVar), Value: aws.String(base64.StdEncoding.EncodeToString([]byte(dockerfile))), Type: codebuildtypes.EnvironmentVariableTypePlaintext},
			{Name: aws.String(buildArgsEnvVar), Value: aws.String(base64.StdEncoding.EncodeToString([]byte(buildArgsFile(buildArgs)))), Type: codebuildtypes.EnvironmentVariableTypePlaintext},
			{Name: aws.String(imageURIEnvVar), Value: aws.String(imageURI(ref.URI, tag)), Type: codebuildtypes.EnvironmentVariableTypePlaintext},
			{Name: aws.String(ecrRepositoryURIEnvVar), Value: aws.String(ref.URI), Type: codebuildtypes.EnvironmentVariableTypePlaintext},
			{Name: aws.String(ecrRegistryEnvVar), Value: aws.String(ref.Registry), Type: codebuildtypes.EnvironmentVariableTypePlaintext},
		},
	})
	if err != nil {
		return "", 0, fmt.Errorf("ImageBuild: starting build: %w", err)
	}
	if out.Build == nil || out.Build.Id == nil {
		return "", 0, fmt.Errorf("ImageBuild: StartBuild did not return a build id")
	}
	// The deadline follows the timeout CodeBuild resolved for this build, which
	// takes the project's timeout and any override into account.
	timeoutMinutes := int32(defaultBuildTimeoutMinutes)
	if t := aws.ToInt32(out.Build.TimeoutInMinutes); t > 0 {
		timeoutMinutes = t
	}
	return aws.ToString(out.Build.Id), timeoutMinutes, nil
}

// ── Status ──────────────────────────────────────────────────────

func (a *ImageBuild) Status(ctx context.Context, request *resource.StatusRequest) (*resource.StatusResult, error) {
	state, err := decodeRequestID(request.RequestID)
	if err != nil {
		return nil, err
	}
	op := resource.Operation(state.Operation)
	if op != resource.OperationUpdate {
		op = resource.OperationCreate
	}

	client, err := a.codeBuildFactory(a.cfg)
	if err != nil {
		return nil, err
	}
	out, err := client.BatchGetBuilds(ctx, &codebuildsdk.BatchGetBuildsInput{Ids: []string{state.BuildID}})
	if err != nil {
		return nil, fmt.Errorf("ImageBuild: getting build status: %w", err)
	}
	if len(out.Builds) == 0 {
		return nil, fmt.Errorf("ImageBuild: build %q not found", state.BuildID)
	}
	build := out.Builds[0]

	pr := &resource.ProgressResult{
		Operation: op,
		NativeID:  encodeNativeID(state.RepoURI, state.Tag, state.ProjectName),
		RequestID: request.RequestID,
	}

	switch build.BuildStatus {
	case codebuildtypes.StatusTypeSucceeded:
		outputs, err := buildOutputsFromExports(build.ExportedEnvironmentVariables, state)
		if err != nil {
			pr.OperationStatus = resource.OperationStatusFailure
			pr.StatusMessage = err.Error()
			return &resource.StatusResult{ProgressResult: pr}, nil
		}
		// An in-place rebuild moved the tag to a new digest and left the prior
		// manifest untagged; prune it so a co-managed repository stays empty enough
		// to tear down. Best-effort: the build already succeeded, so a prune failure
		// is logged, not surfaced.
		if state.PriorDigest != "" && state.PriorDigest != outputs.ImageDigest {
			a.prunePriorDigest(ctx, state.RepoURI, state.PriorDigest)
		}
		js, _ := json.Marshal(outputs)
		pr.OperationStatus = resource.OperationStatusSuccess
		pr.ResourceProperties = js
		return &resource.StatusResult{ProgressResult: pr}, nil

	case codebuildtypes.StatusTypeInProgress:
		if a.now().After(state.Deadline) {
			pr.OperationStatus = resource.OperationStatusFailure
			pr.StatusMessage = fmt.Sprintf("timeout waiting for build %q to complete (deadline %s)", state.BuildID, state.Deadline.Format(time.RFC3339))
			return &resource.StatusResult{ProgressResult: pr}, nil
		}
		pr.OperationStatus = resource.OperationStatusInProgress
		pr.StatusMessage = fmt.Sprintf("build in progress (phase %s)", aws.ToString(build.CurrentPhase))
		return &resource.StatusResult{ProgressResult: pr}, nil

	default: // FAILED / FAULT / TIMED_OUT / STOPPED
		pr.OperationStatus = resource.OperationStatusFailure
		pr.StatusMessage = fmt.Sprintf("build %q %s (phase %s)", state.BuildID, string(build.BuildStatus), aws.ToString(build.CurrentPhase))
		return &resource.StatusResult{ProgressResult: pr}, nil
	}
}

// buildOutputsFromExports reads the digest reference exported by the buildspec and
// assembles the resource outputs. The digest is authoritative for this specific
// push, so it does not depend on a later tag lookup.
func buildOutputsFromExports(exports []codebuildtypes.ExportedEnvironmentVariable, state requestState) (imageBuildOutputs, error) {
	values := make(map[string]string, len(exports))
	for _, e := range exports {
		values[aws.ToString(e.Name)] = aws.ToString(e.Value)
	}
	digest := values[exportedDigestVar]
	if digest == "" {
		return imageBuildOutputs{}, fmt.Errorf("build succeeded but did not export %s", exportedDigestVar)
	}
	ref := values[exportedImageRefVar]
	if ref == "" {
		ref = state.RepoURI + "@" + digest
	}
	uri := values[exportedImageURIVar]
	if uri == "" {
		uri = imageURI(state.RepoURI, state.Tag)
	}
	return imageBuildOutputs{
		ImageRef:        ref,
		ImageDigest:     digest,
		ImageURI:        uri,
		ImageTag:        state.Tag,
		BuildConfigHash: state.BuildConfigHash,
	}, nil
}

// ── Read ────────────────────────────────────────────────────────

func (a *ImageBuild) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	repoURI, tag, _, err := parseNativeID(request.NativeID)
	if err != nil {
		return nil, err
	}
	ref, err := parseEcrRepositoryURI(repoURI)
	if err != nil {
		return nil, err
	}
	client, err := a.ecrFactory(a.cfg)
	if err != nil {
		return nil, err
	}
	out, err := client.DescribeImages(ctx, &ecrsdk.DescribeImagesInput{
		RepositoryName: aws.String(ref.RepoName),
		ImageIds:       []ecrtypes.ImageIdentifier{{ImageTag: aws.String(tag)}},
	})
	if err != nil {
		if isECRImageNotFound(err) {
			return &resource.ReadResult{ResourceType: request.ResourceType, ErrorCode: resource.OperationErrorCodeNotFound}, nil
		}
		return nil, fmt.Errorf("ImageBuild: describing image: %w", err)
	}
	if len(out.ImageDetails) == 0 || aws.ToString(out.ImageDetails[0].ImageDigest) == "" {
		return &resource.ReadResult{ResourceType: request.ResourceType, ErrorCode: resource.OperationErrorCodeNotFound}, nil
	}
	digest := aws.ToString(out.ImageDetails[0].ImageDigest)
	outputs := imageBuildOutputs{
		ImageRef:    ref.URI + "@" + digest,
		ImageDigest: digest,
		ImageURI:    imageURI(ref.URI, tag),
		ImageTag:    tag,
	}
	js, err := json.Marshal(outputs)
	if err != nil {
		return nil, err
	}
	return &resource.ReadResult{ResourceType: request.ResourceType, Properties: string(js)}, nil
}

// ── Update ──────────────────────────────────────────────────────

func (a *ImageBuild) Update(ctx context.Context, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	var desired imageBuildInput
	if err := json.Unmarshal(request.DesiredProperties, &desired); err != nil {
		return nil, fmt.Errorf("ImageBuild: invalid DesiredProperties: %w", err)
	}
	var prior imageBuildOutputs
	if len(request.PriorProperties) > 0 {
		_ = json.Unmarshal(request.PriorProperties, &prior)
	}

	if err := validateInput(desired); err != nil {
		return nil, fmt.Errorf("ImageBuild: %w", err)
	}
	client, ref, project, err := a.preflight(ctx, desired)
	if err != nil {
		return nil, err
	}
	newHash := computeBuildConfigHash(desired)

	// Rebuild only when the build-affecting inputs changed, or the declared tag no
	// longer resolves to the recorded digest in ECR (missing, or moved out of band).
	if prior.BuildConfigHash != "" && prior.BuildConfigHash == newHash {
		matches, err := a.tagMatchesDigest(ctx, request.NativeID, prior.ImageDigest)
		if err != nil {
			return nil, err
		}
		if matches {
			outputs := prior
			outputs.ImageTag = desired.ImageTag
			js, _ := json.Marshal(outputs)
			return &resource.UpdateResult{ProgressResult: &resource.ProgressResult{
				Operation:          resource.OperationUpdate,
				OperationStatus:    resource.OperationStatusSuccess,
				NativeID:           request.NativeID,
				ResourceProperties: js,
			}}, nil
		}
	}

	pr, err := a.startBuild(ctx, client, desired, ref, project, resource.OperationUpdate, prior.ImageDigest)
	if err != nil {
		return nil, err
	}
	return &resource.UpdateResult{ProgressResult: pr}, nil
}

// tagMatchesDigest reports whether the declared tag in the target repository still
// resolves to the recorded digest. It is false when the tag is missing or has been
// moved to a different image out of band (either forces a rebuild), or when the
// recorded digest is empty.
func (a *ImageBuild) tagMatchesDigest(ctx context.Context, nativeID, digest string) (bool, error) {
	if digest == "" {
		return false, nil
	}
	repoURI, tag, _, err := parseNativeID(nativeID)
	if err != nil {
		return false, err
	}
	ref, err := parseEcrRepositoryURI(repoURI)
	if err != nil {
		return false, err
	}
	client, err := a.ecrFactory(a.cfg)
	if err != nil {
		return false, err
	}
	out, err := client.DescribeImages(ctx, &ecrsdk.DescribeImagesInput{
		RepositoryName: aws.String(ref.RepoName),
		ImageIds:       []ecrtypes.ImageIdentifier{{ImageTag: aws.String(tag)}},
	})
	if err != nil {
		if isECRImageNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("ImageBuild: checking pushed tag: %w", err)
	}
	if len(out.ImageDetails) == 0 {
		return false, nil
	}
	return aws.ToString(out.ImageDetails[0].ImageDigest) == digest, nil
}

// ── Delete ──────────────────────────────────────────────────────

func (a *ImageBuild) Delete(ctx context.Context, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	repoURI, tag, projectName, err := parseNativeID(request.NativeID)
	if err != nil {
		return nil, err
	}

	cbClient, err := a.codeBuildFactory(a.cfg)
	if err != nil {
		return nil, err
	}

	// Stop this resource's own in-flight builds. The project is declared and torn
	// down separately, and may be shared with other image builds, so neither the
	// project nor anyone else's builds are touched here.
	a.stopInFlightBuilds(ctx, cbClient, projectName, imageURI(repoURI, tag))

	// Remove the image this resource pushed so the push-target repository is left
	// empty and can itself be torn down. Deletion is scoped to exactly the tag this
	// resource created, so it never touches images pushed by anything else. An
	// already-gone image or repository is success.
	if err := a.deletePushedImage(ctx, repoURI, tag); err != nil {
		return nil, err
	}

	return &resource.DeleteResult{ProgressResult: &resource.ProgressResult{
		Operation:       resource.OperationDelete,
		OperationStatus: resource.OperationStatusSuccess,
		NativeID:        request.NativeID,
	}}, nil
}

// deletePushedImage removes the image this resource pushed, referenced by its tag,
// from the target repository. A missing image or repository is treated as success
// (nothing left to remove); any other ECR error is surfaced so a repository that
// cannot be emptied does not silently block its own teardown.
//
// This removes the image currently under the tag. In-place rebuilds prune their own
// prior digest as the new build succeeds (see prunePriorDigest), so a normal
// build/rebuild/delete cycle leaves the repository empty. A tag moved out of band
// would delete whatever it now points at (the recorded push digest is not available
// at delete time — the request carries only the repository URI, tag and project).
func (a *ImageBuild) deletePushedImage(ctx context.Context, repoURI, tag string) error {
	ref, err := parseEcrRepositoryURI(repoURI)
	if err != nil {
		return fmt.Errorf("ImageBuild: %w", err)
	}
	client, err := a.ecrFactory(a.cfg)
	if err != nil {
		return err
	}
	out, err := client.BatchDeleteImage(ctx, &ecrsdk.BatchDeleteImageInput{
		RepositoryName: aws.String(ref.RepoName),
		ImageIds:       []ecrtypes.ImageIdentifier{{ImageTag: aws.String(tag)}},
	})
	if err != nil && !isECRImageNotFound(err) {
		return fmt.Errorf("ImageBuild: deleting pushed image: %w", err)
	}
	// BatchDeleteImage reports per-image problems in Failures with an HTTP success,
	// so an unchecked call would report a still-present image as deleted.
	if f := firstImageFailure(out); f != nil {
		return fmt.Errorf("ImageBuild: deleting pushed image: %s (%s)", aws.ToString(f.FailureReason), f.FailureCode)
	}
	return nil
}

// firstImageFailure returns the first per-image failure that is not an already-gone
// (ImageNotFound) result, or nil. ECR's BatchDeleteImage returns HTTP success with
// per-image problems (e.g. ImageReferencedByManifestList) reported in Failures
// rather than as a request error.
func firstImageFailure(out *ecrsdk.BatchDeleteImageOutput) *ecrtypes.ImageFailure {
	if out == nil {
		return nil
	}
	for i := range out.Failures {
		if out.Failures[i].FailureCode != ecrtypes.ImageFailureCodeImageNotFound {
			return &out.Failures[i]
		}
	}
	return nil
}

// prunePriorDigest best-effort removes the manifest a prior build pushed under the
// same tag, which an in-place rebuild left untagged. It deletes the manifest only
// when it currently carries no tags, so a digest still referenced by another tag
// (e.g. an identical image pushed elsewhere in the repository) is left untouched.
// The rebuild already succeeded, so any failure here is logged, not surfaced.
func (a *ImageBuild) prunePriorDigest(ctx context.Context, repoURI, digest string) {
	log := plugin.LoggerFromContext(ctx)
	ref, err := parseEcrRepositoryURI(repoURI)
	if err != nil {
		log.Warn("ImageBuild: skipping prior-digest prune; invalid repository uri", "error", err.Error())
		return
	}
	client, err := a.ecrFactory(a.cfg)
	if err != nil {
		log.Warn("ImageBuild: skipping prior-digest prune; ecr client unavailable", "error", err.Error())
		return
	}
	out, err := client.DescribeImages(ctx, &ecrsdk.DescribeImagesInput{
		RepositoryName: aws.String(ref.RepoName),
		ImageIds:       []ecrtypes.ImageIdentifier{{ImageDigest: aws.String(digest)}},
	})
	if err != nil {
		if !isECRImageNotFound(err) {
			log.Warn("ImageBuild: skipping prior-digest prune; describe failed", "digest", digest, "error", err.Error())
		}
		return
	}
	// Leave the manifest if it is still referenced by any tag.
	if len(out.ImageDetails) > 0 && len(out.ImageDetails[0].ImageTags) > 0 {
		return
	}
	delOut, err := client.BatchDeleteImage(ctx, &ecrsdk.BatchDeleteImageInput{
		RepositoryName: aws.String(ref.RepoName),
		ImageIds:       []ecrtypes.ImageIdentifier{{ImageDigest: aws.String(digest)}},
	})
	if err != nil && !isECRImageNotFound(err) {
		log.Warn("ImageBuild: pruning prior digest failed", "digest", digest, "error", err.Error())
		return
	}
	if f := firstImageFailure(delOut); f != nil {
		log.Warn("ImageBuild: pruning prior digest failed", "digest", digest, "reason", aws.ToString(f.FailureReason), "code", string(f.FailureCode))
	}
}

// stopInFlightBuilds best-effort stops the running builds this resource started on
// the referenced project, identified by the push target they carry. The project can
// be shared with other image builds, so a build for any other push target — or one
// started by something else entirely — is left running. Any error here is non-fatal
// to the delete.
func (a *ImageBuild) stopInFlightBuilds(ctx context.Context, client codeBuildClientInterface, projectName, targetImageURI string) {
	listOut, err := client.ListBuildsForProject(ctx, &codebuildsdk.ListBuildsForProjectInput{ProjectName: aws.String(projectName)})
	if err != nil || len(listOut.Ids) == 0 {
		return
	}
	buildsOut, err := client.BatchGetBuilds(ctx, &codebuildsdk.BatchGetBuildsInput{Ids: listOut.Ids})
	if err != nil {
		return
	}
	for _, b := range buildsOut.Builds {
		if b.BuildStatus != codebuildtypes.StatusTypeInProgress {
			continue
		}
		if buildImageURI(b) != targetImageURI {
			continue
		}
		_, _ = client.StopBuild(ctx, &codebuildsdk.StopBuildInput{Id: b.Id})
	}
}

// buildImageURI returns the push target a build was started for, taken from the
// IMAGE_URI environment variable this resource sets on every build it dispatches.
// A build started by anything else has no such variable and returns "".
func buildImageURI(build codebuildtypes.Build) string {
	if build.Environment == nil {
		return ""
	}
	for _, v := range build.Environment.EnvironmentVariables {
		if aws.ToString(v.Name) == imageURIEnvVar {
			return aws.ToString(v.Value)
		}
	}
	return ""
}

func (a *ImageBuild) List(_ context.Context, _ *resource.ListRequest) (*resource.ListResult, error) {
	// discoverable = false: the build resource has no listable inventory.
	return &resource.ListResult{NativeIDs: []string{}}, nil
}

// ── error classification ────────────────────────────────────────

func isCodeBuildNotFound(err error) bool {
	var rnf *codebuildtypes.ResourceNotFoundException
	return errors.As(err, &rnf)
}

func isECRImageNotFound(err error) bool {
	var inf *ecrtypes.ImageNotFoundException
	if errors.As(err, &inf) {
		return true
	}
	var rnf *ecrtypes.RepositoryNotFoundException
	return errors.As(err, &rnf)
}
