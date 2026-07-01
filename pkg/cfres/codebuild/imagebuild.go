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
	iamsdk "github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/aws/smithy-go"

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

// codeBuildClientInterface is the subset of the CodeBuild API this resource uses.
// *codebuild.Client satisfies it.
type codeBuildClientInterface interface {
	BatchGetProjects(ctx context.Context, params *codebuildsdk.BatchGetProjectsInput, optFns ...func(*codebuildsdk.Options)) (*codebuildsdk.BatchGetProjectsOutput, error)
	CreateProject(ctx context.Context, params *codebuildsdk.CreateProjectInput, optFns ...func(*codebuildsdk.Options)) (*codebuildsdk.CreateProjectOutput, error)
	UpdateProject(ctx context.Context, params *codebuildsdk.UpdateProjectInput, optFns ...func(*codebuildsdk.Options)) (*codebuildsdk.UpdateProjectOutput, error)
	DeleteProject(ctx context.Context, params *codebuildsdk.DeleteProjectInput, optFns ...func(*codebuildsdk.Options)) (*codebuildsdk.DeleteProjectOutput, error)
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

// iamClientInterface is the subset of the IAM API this resource uses to manage the
// internal CodeBuild service role.
type iamClientInterface interface {
	GetRole(ctx context.Context, params *iamsdk.GetRoleInput, optFns ...func(*iamsdk.Options)) (*iamsdk.GetRoleOutput, error)
	CreateRole(ctx context.Context, params *iamsdk.CreateRoleInput, optFns ...func(*iamsdk.Options)) (*iamsdk.CreateRoleOutput, error)
	PutRolePolicy(ctx context.Context, params *iamsdk.PutRolePolicyInput, optFns ...func(*iamsdk.Options)) (*iamsdk.PutRolePolicyOutput, error)
	DeleteRolePolicy(ctx context.Context, params *iamsdk.DeleteRolePolicyInput, optFns ...func(*iamsdk.Options)) (*iamsdk.DeleteRolePolicyOutput, error)
	DeleteRole(ctx context.Context, params *iamsdk.DeleteRoleInput, optFns ...func(*iamsdk.Options)) (*iamsdk.DeleteRoleOutput, error)
}

// ImageBuild is the synthetic build-during-apply provisioner that builds and
// pushes a container image from a caller-supplied Dockerfile.
type ImageBuild struct {
	cfg *config.Config

	codeBuildFactory func(*config.Config) (codeBuildClientInterface, error)
	ecrFactory       func(*config.Config) (ecrClientInterface, error)
	iamFactory       func(*config.Config) (iamClientInterface, error)

	now   func() time.Time
	sleep func(time.Duration)
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
				iamFactory:       defaultIamFactory,
				now:              func() time.Time { return time.Now().UTC() },
				sleep:            time.Sleep,
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

func defaultIamFactory(cfg *config.Config) (iamClientInterface, error) {
	awsCfg, err := cfg.ToAwsConfig(context.Background())
	if err != nil {
		return nil, err
	}
	return iamsdk.NewFromConfig(awsCfg), nil
}

// ── NativeID / RequestID codecs ─────────────────────────────────

// encodeNativeID joins the push target into the composite identity. Neither the
// repository URI nor the tag can contain '|', so SplitN round-trips cleanly.
func encodeNativeID(repoURI, tag string) string { return repoURI + "|" + tag }

func parseNativeID(nativeID string) (repoURI, tag string, err error) {
	parts := strings.SplitN(nativeID, "|", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid NativeID %q: expected <repositoryUri>|<tag>", nativeID)
	}
	return parts[0], parts[1], nil
}

// requestState is what a build's RequestID carries so Status can poll the exact
// build and reconstruct the outputs without any other persisted state.
type requestState struct {
	Operation       string
	BuildID         string
	RepoURI         string
	Tag             string
	Deadline        time.Time
	BuildConfigHash string
}

func encodeRequestID(s requestState) string {
	return strings.Join([]string{
		s.Operation,
		s.BuildID,
		s.RepoURI,
		s.Tag,
		s.Deadline.UTC().Format(time.RFC3339),
		s.BuildConfigHash,
	}, "|")
}

func decodeRequestID(requestID string) (requestState, error) {
	parts := strings.SplitN(requestID, "|", 6)
	if len(parts) != 6 {
		return requestState{}, fmt.Errorf("invalid RequestID %q", requestID)
	}
	deadline, err := time.Parse(time.RFC3339, parts[4])
	if err != nil {
		return requestState{}, fmt.Errorf("invalid deadline in RequestID: %w", err)
	}
	return requestState{
		Operation:       parts[0],
		BuildID:         parts[1],
		RepoURI:         parts[2],
		Tag:             parts[3],
		Deadline:        deadline,
		BuildConfigHash: parts[5],
	}, nil
}

// ── Create ──────────────────────────────────────────────────────

func (a *ImageBuild) Create(ctx context.Context, request *resource.CreateRequest) (*resource.CreateResult, error) {
	var in imageBuildInput
	if err := json.Unmarshal(request.Properties, &in); err != nil {
		return nil, fmt.Errorf("ImageBuild: invalid Properties: %w", err)
	}
	pr, err := a.startBuild(ctx, in, resource.OperationCreate)
	if err != nil {
		return nil, err
	}
	return &resource.CreateResult{ProgressResult: pr}, nil
}

// startBuild validates inputs, ensures the internal role and project exist, kicks
// off a build, and returns an InProgress ProgressResult carrying the poll state.
func (a *ImageBuild) startBuild(ctx context.Context, in imageBuildInput, op resource.Operation) (*resource.ProgressResult, error) {
	if err := validateInput(in); err != nil {
		return nil, fmt.Errorf("ImageBuild: %w", err)
	}
	n := normalizeInput(in)
	ref, err := parseEcrRepositoryURI(n.EcrRepositoryURI)
	if err != nil {
		return nil, fmt.Errorf("ImageBuild: %w", err)
	}
	// The CodeBuild project, its IAM role, its log group, and the ECR API clients
	// all run in the target's region, and the internal role's inline policy is
	// scoped to the target account. The push target must therefore be an ECR
	// repository in that same region (v1 also assumes the same account); a
	// cross-region repository would build but then fail to log or to be read/deleted.
	if a.cfg.Region != "" && ref.Region != a.cfg.Region {
		return nil, fmt.Errorf("ImageBuild: ecrRepositoryUri region %q must match the target region %q", ref.Region, a.cfg.Region)
	}
	projectName, roleName := resourceNames(ref.URI, n.ImageTag)

	cbClient, err := a.codeBuildFactory(a.cfg)
	if err != nil {
		return nil, err
	}
	iamClient, err := a.iamFactory(a.cfg)
	if err != nil {
		return nil, err
	}

	roleArn, err := a.ensureRole(ctx, iamClient, n, ref, roleName, projectName)
	if err != nil {
		return nil, err
	}
	if err := a.ensureProject(ctx, cbClient, n, projectName, roleArn); err != nil {
		return nil, err
	}

	buildID, err := a.dispatchBuild(ctx, cbClient, projectName, ref, n.ImageTag, n.Dockerfile, n.BuildArgs)
	if err != nil {
		return nil, err
	}
	plugin.LoggerFromContext(ctx).Info("ImageBuild: build started",
		"project", projectName, "buildId", buildID, "imageUri", imageURI(ref.URI, n.ImageTag))

	deadline := a.now().Add(time.Duration(n.TimeoutMinutes)*time.Minute + pollDeadlineBuffer)
	state := requestState{
		Operation:       string(op),
		BuildID:         buildID,
		RepoURI:         ref.URI,
		Tag:             n.ImageTag,
		Deadline:        deadline,
		BuildConfigHash: computeBuildConfigHash(n),
	}
	return &resource.ProgressResult{
		Operation:       op,
		OperationStatus: resource.OperationStatusInProgress,
		NativeID:        encodeNativeID(ref.URI, n.ImageTag),
		RequestID:       encodeRequestID(state),
	}, nil
}

// ensureRole adopts a BYO role when serviceRoleArn is set, otherwise idempotently
// creates the internal role and (re)writes its inline policy. It returns the role
// ARN CodeBuild should assume.
func (a *ImageBuild) ensureRole(ctx context.Context, client iamClientInterface, in imageBuildInput, ref ecrRepositoryRef, roleName, projectName string) (string, error) {
	if in.ServiceRoleArn != "" {
		// BYO role: the plugin never creates, mutates, or deletes it.
		return in.ServiceRoleArn, nil
	}

	var roleArn string
	getOut, err := client.GetRole(ctx, &iamsdk.GetRoleInput{RoleName: aws.String(roleName)})
	switch {
	case err == nil:
		roleArn = aws.ToString(getOut.Role.Arn)
	case isIAMNotFound(err):
		createOut, cerr := client.CreateRole(ctx, &iamsdk.CreateRoleInput{
			RoleName:                 aws.String(roleName),
			AssumeRolePolicyDocument: aws.String(buildTrustPolicy()),
			Description:              aws.String("formae-managed CodeBuild service role for building a container image"),
		})
		if cerr != nil {
			return "", fmt.Errorf("ImageBuild: creating service role: %w", cerr)
		}
		roleArn = aws.ToString(createOut.Role.Arn)
	default:
		return "", fmt.Errorf("ImageBuild: getting service role: %w", err)
	}

	if _, err := client.PutRolePolicy(ctx, &iamsdk.PutRolePolicyInput{
		RoleName:       aws.String(roleName),
		PolicyName:     aws.String(inlinePolicyName),
		PolicyDocument: aws.String(buildInlinePolicy(ref, projectName)),
	}); err != nil {
		return "", fmt.Errorf("ImageBuild: putting role policy: %w", err)
	}
	return roleArn, nil
}

// ensureProject creates or updates the internal CodeBuild project. A freshly
// created role can lag IAM propagation, so project creation retries briefly on the
// CodeBuild "cannot assume role" error.
func (a *ImageBuild) ensureProject(ctx context.Context, client codeBuildClientInterface, in imageBuildInput, projectName, roleArn string) error {
	getOut, err := client.BatchGetProjects(ctx, &codebuildsdk.BatchGetProjectsInput{Names: []string{projectName}})
	if err != nil {
		return fmt.Errorf("ImageBuild: looking up build project: %w", err)
	}
	exists := len(getOut.Projects) > 0

	env := &codebuildtypes.ProjectEnvironment{
		Type:                     codebuildtypes.EnvironmentTypeLinuxContainer,
		ComputeType:              codebuildtypes.ComputeType(in.ComputeType),
		Image:                    aws.String(in.BuildEnvironmentImage),
		PrivilegedMode:           aws.Bool(true),
		ImagePullCredentialsType: codebuildtypes.ImagePullCredentialsTypeCodebuild,
	}
	source := &codebuildtypes.ProjectSource{
		Type:      codebuildtypes.SourceTypeNoSource,
		Buildspec: aws.String(generateBuildspec()),
	}
	artifacts := &codebuildtypes.ProjectArtifacts{Type: codebuildtypes.ArtifactsTypeNoArtifacts}
	timeout := aws.Int32(int32(in.TimeoutMinutes))

	if exists {
		_, err := client.UpdateProject(ctx, &codebuildsdk.UpdateProjectInput{
			Name:             aws.String(projectName),
			Source:           source,
			Artifacts:        artifacts,
			Environment:      env,
			ServiceRole:      aws.String(roleArn),
			TimeoutInMinutes: timeout,
		})
		if err != nil {
			return fmt.Errorf("ImageBuild: updating build project: %w", err)
		}
		return nil
	}

	create := func() error {
		_, err := client.CreateProject(ctx, &codebuildsdk.CreateProjectInput{
			Name:             aws.String(projectName),
			Source:           source,
			Artifacts:        artifacts,
			Environment:      env,
			ServiceRole:      aws.String(roleArn),
			TimeoutInMinutes: timeout,
		})
		return err
	}
	const maxAttempts = 8
	for attempt := 1; ; attempt++ {
		err := create()
		if err == nil {
			return nil
		}
		if attempt >= maxAttempts || !isAssumeRolePropagationError(err) {
			return fmt.Errorf("ImageBuild: creating build project: %w", err)
		}
		a.sleep(3 * time.Second)
	}
}

// dispatchBuild starts the build with the per-build environment overrides and
// returns the build id.
func (a *ImageBuild) dispatchBuild(ctx context.Context, client codeBuildClientInterface, projectName string, ref ecrRepositoryRef, tag, dockerfile string, buildArgs map[string]string) (string, error) {
	out, err := client.StartBuild(ctx, &codebuildsdk.StartBuildInput{
		ProjectName: aws.String(projectName),
		EnvironmentVariablesOverride: []codebuildtypes.EnvironmentVariable{
			{Name: aws.String(dockerfileEnvVar), Value: aws.String(base64.StdEncoding.EncodeToString([]byte(dockerfile))), Type: codebuildtypes.EnvironmentVariableTypePlaintext},
			{Name: aws.String(buildArgsEnvVar), Value: aws.String(base64.StdEncoding.EncodeToString([]byte(buildArgsFile(buildArgs)))), Type: codebuildtypes.EnvironmentVariableTypePlaintext},
			{Name: aws.String(imageURIEnvVar), Value: aws.String(imageURI(ref.URI, tag)), Type: codebuildtypes.EnvironmentVariableTypePlaintext},
			{Name: aws.String(ecrRepositoryURIEnvVar), Value: aws.String(ref.URI), Type: codebuildtypes.EnvironmentVariableTypePlaintext},
			{Name: aws.String(ecrRegistryEnvVar), Value: aws.String(ref.Registry), Type: codebuildtypes.EnvironmentVariableTypePlaintext},
		},
	})
	if err != nil {
		return "", fmt.Errorf("ImageBuild: starting build: %w", err)
	}
	if out.Build == nil || out.Build.Id == nil {
		return "", fmt.Errorf("ImageBuild: StartBuild did not return a build id")
	}
	return aws.ToString(out.Build.Id), nil
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
		NativeID:  encodeNativeID(state.RepoURI, state.Tag),
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
	repoURI, tag, err := parseNativeID(request.NativeID)
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

	pr, err := a.startBuild(ctx, desired, resource.OperationUpdate)
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
	repoURI, tag, err := parseNativeID(nativeID)
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
	repoURI, tag, err := parseNativeID(request.NativeID)
	if err != nil {
		return nil, err
	}
	projectName, roleName := resourceNames(repoURI, tag)

	cbClient, err := a.codeBuildFactory(a.cfg)
	if err != nil {
		return nil, err
	}
	iamClient, err := a.iamFactory(a.cfg)
	if err != nil {
		return nil, err
	}

	// Stop any in-flight build for the project before deleting it.
	a.stopInFlightBuilds(ctx, cbClient, projectName)

	// An already-gone project is success: a partially-completed delete must be
	// retryable through to cleaning up the pushed image and role below.
	if _, err := cbClient.DeleteProject(ctx, &codebuildsdk.DeleteProjectInput{Name: aws.String(projectName)}); err != nil && !isCodeBuildNotFound(err) {
		return nil, fmt.Errorf("ImageBuild: deleting build project: %w", err)
	}

	// Remove the image this resource pushed so the push-target repository is left
	// empty and can itself be torn down. Deletion is scoped to exactly the tag this
	// resource created, so it never touches images pushed by anything else. An
	// already-gone image or repository is success.
	if err := a.deletePushedImage(ctx, repoURI, tag); err != nil {
		return nil, err
	}

	// Only remove the service role when it exists under this plugin's deterministic
	// internal name. A BYO-role deployment never creates that role (its role has a
	// caller-owned ARN), so the lookup misses and the caller's role is left
	// untouched per the schema contract — and a BYO deployment that does not grant
	// the agent IAM access does not fail teardown on an AccessDenied delete.
	if a.internalRoleExists(ctx, iamClient, roleName) {
		if _, err := iamClient.DeleteRolePolicy(ctx, &iamsdk.DeleteRolePolicyInput{
			RoleName:   aws.String(roleName),
			PolicyName: aws.String(inlinePolicyName),
		}); err != nil && !isIAMNotFound(err) {
			return nil, fmt.Errorf("ImageBuild: deleting role policy: %w", err)
		}
		if _, err := iamClient.DeleteRole(ctx, &iamsdk.DeleteRoleInput{RoleName: aws.String(roleName)}); err != nil && !isIAMNotFound(err) {
			return nil, fmt.Errorf("ImageBuild: deleting role: %w", err)
		}
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
// This removes only the image currently under the tag. Rebuilding to the same tag
// (an in-place update) leaves the prior digest behind as an untagged image; those
// orphaned digests are not pruned here, so a co-managed repository that has been
// rebuilt in place may still hold untagged images at teardown.
func (a *ImageBuild) deletePushedImage(ctx context.Context, repoURI, tag string) error {
	ref, err := parseEcrRepositoryURI(repoURI)
	if err != nil {
		return fmt.Errorf("ImageBuild: %w", err)
	}
	client, err := a.ecrFactory(a.cfg)
	if err != nil {
		return err
	}
	if _, err := client.BatchDeleteImage(ctx, &ecrsdk.BatchDeleteImageInput{
		RepositoryName: aws.String(ref.RepoName),
		ImageIds:       []ecrtypes.ImageIdentifier{{ImageTag: aws.String(tag)}},
	}); err != nil && !isECRImageNotFound(err) {
		return fmt.Errorf("ImageBuild: deleting pushed image: %w", err)
	}
	return nil
}

// internalRoleExists reports whether the internally-managed service role is present
// under its deterministic name. A NotFound (a BYO deployment that never created it,
// or an already-completed delete) means there is nothing of ours to remove. Any
// other lookup error (e.g. a BYO deployment that grants the agent no IAM access) is
// also treated as "not ours to delete" so teardown is never blocked, but is logged
// so an orphaned internally-managed role stays observable.
func (a *ImageBuild) internalRoleExists(ctx context.Context, client iamClientInterface, roleName string) bool {
	if _, err := client.GetRole(ctx, &iamsdk.GetRoleInput{RoleName: aws.String(roleName)}); err != nil {
		if !isIAMNotFound(err) {
			plugin.LoggerFromContext(ctx).Warn("ImageBuild: skipping internal role cleanup; role lookup failed",
				"role", roleName, "error", err.Error())
		}
		return false
	}
	return true
}

// stopInFlightBuilds best-effort stops any running build for the project so the
// project can be deleted. Any error here is non-fatal to the delete.
func (a *ImageBuild) stopInFlightBuilds(ctx context.Context, client codeBuildClientInterface, projectName string) {
	listOut, err := client.ListBuildsForProject(ctx, &codebuildsdk.ListBuildsForProjectInput{ProjectName: aws.String(projectName)})
	if err != nil || len(listOut.Ids) == 0 {
		return
	}
	buildsOut, err := client.BatchGetBuilds(ctx, &codebuildsdk.BatchGetBuildsInput{Ids: listOut.Ids})
	if err != nil {
		return
	}
	for _, b := range buildsOut.Builds {
		if b.BuildStatus == codebuildtypes.StatusTypeInProgress {
			_, _ = client.StopBuild(ctx, &codebuildsdk.StopBuildInput{Id: b.Id})
		}
	}
}

func (a *ImageBuild) List(_ context.Context, _ *resource.ListRequest) (*resource.ListResult, error) {
	// discoverable = false: the build resource has no listable inventory.
	return &resource.ListResult{NativeIDs: []string{}}, nil
}

// ── error classification ────────────────────────────────────────

func isIAMNotFound(err error) bool {
	var nse *iamtypes.NoSuchEntityException
	return errors.As(err, &nse)
}

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

// isAssumeRolePropagationError reports whether a CreateProject error is the
// transient "CodeBuild cannot assume the freshly-created role yet" IAM-propagation
// race, which clears on retry.
func isAssumeRolePropagationError(err error) bool {
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	if apiErr.ErrorCode() != "InvalidInputException" {
		return false
	}
	msg := strings.ToLower(apiErr.ErrorMessage())
	return strings.Contains(msg, "cannot be assumed") || strings.Contains(msg, "not authorized to perform: sts:assumerole") || strings.Contains(msg, "service role")
}
