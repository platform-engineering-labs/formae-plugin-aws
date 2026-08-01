// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package codebuild

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	codebuildsdk "github.com/aws/aws-sdk-go-v2/service/codebuild"
	codebuildtypes "github.com/aws/aws-sdk-go-v2/service/codebuild/types"

	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres/prov"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres/registry"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/config"
	"github.com/platform-engineering-labs/formae/pkg/plugin"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
)

const projectResourceType = "AWS::CodeBuild::Project"

const (
	// projectRetryAttempts and projectRetryDelay bound the wait for a freshly
	// created service role to become assumable by CodeBuild.
	projectRetryAttempts = 8
	projectRetryDelay    = 3 * time.Second

	// CodeBuild's own defaults, sent on update for a timeout the caller no longer
	// declares so the project returns to the default rather than keeping the
	// previously declared value.
	defaultProjectTimeoutInMinutes       = 60
	defaultProjectQueuedTimeoutInMinutes = 480

	// CodeBuild removes a project's concurrent build limit when the update sends
	// -1; omitting the field would leave the previous limit in place.
	clearedConcurrentBuildLimit = -1
)

// projectClientInterface is the subset of the CodeBuild API this resource uses.
// *codebuild.Client satisfies it.
type projectClientInterface interface {
	CreateProject(ctx context.Context, params *codebuildsdk.CreateProjectInput, optFns ...func(*codebuildsdk.Options)) (*codebuildsdk.CreateProjectOutput, error)
	UpdateProject(ctx context.Context, params *codebuildsdk.UpdateProjectInput, optFns ...func(*codebuildsdk.Options)) (*codebuildsdk.UpdateProjectOutput, error)
	BatchGetProjects(ctx context.Context, params *codebuildsdk.BatchGetProjectsInput, optFns ...func(*codebuildsdk.Options)) (*codebuildsdk.BatchGetProjectsOutput, error)
	DeleteProject(ctx context.Context, params *codebuildsdk.DeleteProjectInput, optFns ...func(*codebuildsdk.Options)) (*codebuildsdk.DeleteProjectOutput, error)
}

// Project provisions AWS::CodeBuild::Project over the CodeBuild API. The type is
// NON_PROVISIONABLE in CloudControl, so the generic path cannot manage it.
type Project struct {
	cfg *config.Config

	projectFactory func(*config.Config) (projectClientInterface, error)

	retryAttempts int
	retryDelay    time.Duration
}

var _ prov.Provisioner = &Project{}

func init() {
	registry.Register(projectResourceType,
		[]resource.Operation{
			resource.OperationCreate,
			resource.OperationRead,
			resource.OperationUpdate,
			resource.OperationDelete,
			resource.OperationList,
		},
		func(cfg *config.Config) prov.Provisioner {
			return &Project{
				cfg:            cfg,
				projectFactory: defaultProjectFactory,
				retryAttempts:  projectRetryAttempts,
				retryDelay:     projectRetryDelay,
			}
		})
}

func defaultProjectFactory(cfg *config.Config) (projectClientInterface, error) {
	awsCfg, err := cfg.ToAwsConfig(context.Background())
	if err != nil {
		return nil, err
	}
	return codebuildsdk.NewFromConfig(awsCfg), nil
}

// ── property shape ──────────────────────────────────────────────

// projectProperties mirrors the Pkl schema's property names (capitalized by the
// plugin wire format's output-key transformation).
type projectProperties struct {
	Name                   string              `json:"Name,omitempty"`
	Description            string              `json:"Description,omitempty"`
	ServiceRole            string              `json:"ServiceRole,omitempty"`
	Source                 *projectSource      `json:"Source,omitempty"`
	Artifacts              *projectArtifacts   `json:"Artifacts,omitempty"`
	Environment            *projectEnvironment `json:"Environment,omitempty"`
	Cache                  *projectCache       `json:"Cache,omitempty"`
	LogsConfig             *projectLogsConfig  `json:"LogsConfig,omitempty"`
	TimeoutInMinutes       *int32              `json:"TimeoutInMinutes,omitempty"`
	QueuedTimeoutInMinutes *int32              `json:"QueuedTimeoutInMinutes,omitempty"`
	ConcurrentBuildLimit   *int32              `json:"ConcurrentBuildLimit,omitempty"`
	Tags                   []projectTag        `json:"Tags,omitempty"`
	Arn                    string              `json:"Arn,omitempty"`
}

type projectSource struct {
	Type      string `json:"Type,omitempty"`
	BuildSpec string `json:"BuildSpec,omitempty"`
	Location  string `json:"Location,omitempty"`
}

type projectArtifacts struct {
	Type      string `json:"Type,omitempty"`
	Location  string `json:"Location,omitempty"`
	Name      string `json:"Name,omitempty"`
	Packaging string `json:"Packaging,omitempty"`
}

type projectEnvironmentVariable struct {
	Name  string `json:"Name,omitempty"`
	Value string `json:"Value,omitempty"`
	Type  string `json:"Type,omitempty"`
}

type projectEnvironment struct {
	Type                     string                       `json:"Type,omitempty"`
	ComputeType              string                       `json:"ComputeType,omitempty"`
	Image                    string                       `json:"Image,omitempty"`
	PrivilegedMode           *bool                        `json:"PrivilegedMode,omitempty"`
	ImagePullCredentialsType string                       `json:"ImagePullCredentialsType,omitempty"`
	EnvironmentVariables     []projectEnvironmentVariable `json:"EnvironmentVariables,omitempty"`
}

type projectCache struct {
	Type     string   `json:"Type,omitempty"`
	Location string   `json:"Location,omitempty"`
	Modes    []string `json:"Modes,omitempty"`
}

type projectCloudWatchLogs struct {
	Status     string `json:"Status,omitempty"`
	GroupName  string `json:"GroupName,omitempty"`
	StreamName string `json:"StreamName,omitempty"`
}

type projectS3Logs struct {
	Status             string `json:"Status,omitempty"`
	Location           string `json:"Location,omitempty"`
	EncryptionDisabled *bool  `json:"EncryptionDisabled,omitempty"`
}

type projectLogsConfig struct {
	CloudWatchLogs *projectCloudWatchLogs `json:"CloudWatchLogs,omitempty"`
	S3Logs         *projectS3Logs         `json:"S3Logs,omitempty"`
}

type projectTag struct {
	Key   string `json:"Key,omitempty"`
	Value string `json:"Value,omitempty"`
}

// ── Create ──────────────────────────────────────────────────────

func (p *Project) Create(ctx context.Context, request *resource.CreateRequest) (*resource.CreateResult, error) {
	client, err := p.projectFactory(p.cfg)
	if err != nil {
		return nil, err
	}
	return p.createWithClient(ctx, client, request)
}

func (p *Project) createWithClient(ctx context.Context, client projectClientInterface, request *resource.CreateRequest) (*resource.CreateResult, error) {
	var props projectProperties
	if err := json.Unmarshal(request.Properties, &props); err != nil {
		return nil, fmt.Errorf("invalid CodeBuild project properties: %w", err)
	}
	if props.Name == "" {
		return nil, fmt.Errorf("name is required for a CodeBuild project")
	}
	input := createProjectInput(props)

	created, err := p.callWithRolePropagationRetry(ctx, "creating", props.Name, func(attempt int) (*codebuildtypes.Project, error) {
		out, err := client.CreateProject(ctx, input)
		if err == nil {
			return out.Project, nil
		}
		if !isProjectAlreadyExists(err) {
			return nil, err
		}
		if attempt == 1 {
			// The project was there before this apply touched it. formae does
			// not adopt an existing project, so this is a hard conflict.
			return nil, fmt.Errorf("a project with that name already exists and is not managed by this resource")
		}
		// Only a retry of our own loop can see the name taken by us: an earlier
		// attempt succeeded server-side and its response was lost. Read it back
		// rather than failing an apply that in fact created the project.
		existing, readErr := getProject(ctx, client, props.Name)
		if readErr != nil {
			return nil, readErr
		}
		if existing == nil {
			return nil, err
		}
		return existing, nil
	})
	if err != nil {
		return nil, err
	}
	if created == nil {
		return nil, fmt.Errorf("creating CodeBuild project %q: the API returned no project", props.Name)
	}

	js, err := json.Marshal(propertiesFromProject(created))
	if err != nil {
		return nil, err
	}
	return &resource.CreateResult{ProgressResult: &resource.ProgressResult{
		Operation:          resource.OperationCreate,
		OperationStatus:    resource.OperationStatusSuccess,
		NativeID:           props.Name,
		ResourceProperties: js,
	}}, nil
}

// callWithRolePropagationRetry runs call until it succeeds, until it fails with
// anything other than the transient "CodeBuild cannot assume the service role
// yet" IAM-propagation race, or until the attempt budget is spent. call receives
// the 1-based attempt number so it can tell a first-attempt failure from one that
// follows a retry of this loop.
func (p *Project) callWithRolePropagationRetry(ctx context.Context, action, name string, call func(attempt int) (*codebuildtypes.Project, error)) (*codebuildtypes.Project, error) {
	for attempt := 1; ; attempt++ {
		project, err := call(attempt)
		if err == nil {
			return project, nil
		}
		if attempt >= p.retryAttempts || !isAssumeRolePropagationError(err) {
			return nil, fmt.Errorf("%s CodeBuild project %q: %w", action, name, err)
		}
		plugin.LoggerFromContext(ctx).Info("waiting for the CodeBuild service role to become assumable",
			"project", name, "attempt", attempt)
		if waitErr := p.waitBeforeRetry(ctx); waitErr != nil {
			return nil, waitErr
		}
	}
}

// waitBeforeRetry waits out the retry interval, returning the context's error as
// soon as the apply is cancelled so a cancelled operation does not sit out the
// full wait.
func (p *Project) waitBeforeRetry(ctx context.Context) error {
	timer := time.NewTimer(p.retryDelay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// ── Read ────────────────────────────────────────────────────────

func (p *Project) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	client, err := p.projectFactory(p.cfg)
	if err != nil {
		return nil, err
	}
	return p.readWithClient(ctx, client, request)
}

func (p *Project) readWithClient(ctx context.Context, client projectClientInterface, request *resource.ReadRequest) (*resource.ReadResult, error) {
	project, err := getProject(ctx, client, request.NativeID)
	if err != nil {
		return nil, fmt.Errorf("reading CodeBuild project %q: %w", request.NativeID, err)
	}
	if project == nil {
		return &resource.ReadResult{ResourceType: request.ResourceType, ErrorCode: resource.OperationErrorCodeNotFound}, nil
	}
	js, err := json.Marshal(propertiesFromProject(project))
	if err != nil {
		return nil, err
	}
	return &resource.ReadResult{ResourceType: request.ResourceType, Properties: string(js)}, nil
}

// getProject fetches a single project by name, returning (nil, nil) when it does
// not exist. BatchGetProjects reports a missing name in ProjectsNotFound rather
// than as an error.
func getProject(ctx context.Context, client projectClientInterface, name string) (*codebuildtypes.Project, error) {
	out, err := client.BatchGetProjects(ctx, &codebuildsdk.BatchGetProjectsInput{Names: []string{name}})
	if err != nil {
		return nil, err
	}
	if len(out.Projects) == 0 || slices.Contains(out.ProjectsNotFound, name) {
		return nil, nil
	}
	return &out.Projects[0], nil
}

// ── Update ──────────────────────────────────────────────────────

func (p *Project) Update(ctx context.Context, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	client, err := p.projectFactory(p.cfg)
	if err != nil {
		return nil, err
	}
	return p.updateWithClient(ctx, client, request)
}

func (p *Project) updateWithClient(ctx context.Context, client projectClientInterface, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	var props projectProperties
	if err := json.Unmarshal(request.DesiredProperties, &props); err != nil {
		return nil, fmt.Errorf("invalid CodeBuild project properties: %w", err)
	}
	if props.Name == "" {
		return nil, fmt.Errorf("name is required for a CodeBuild project")
	}
	input := updateProjectInput(props)

	updated, err := p.callWithRolePropagationRetry(ctx, "updating", props.Name, func(int) (*codebuildtypes.Project, error) {
		out, err := client.UpdateProject(ctx, input)
		if err != nil {
			return nil, err
		}
		return out.Project, nil
	})
	if err != nil {
		return nil, err
	}
	if updated == nil {
		return nil, fmt.Errorf("updating CodeBuild project %q: the API returned no project", props.Name)
	}

	js, err := json.Marshal(propertiesFromProject(updated))
	if err != nil {
		return nil, err
	}
	return &resource.UpdateResult{ProgressResult: &resource.ProgressResult{
		Operation:          resource.OperationUpdate,
		OperationStatus:    resource.OperationStatusSuccess,
		NativeID:           request.NativeID,
		ResourceProperties: js,
	}}, nil
}

// ── Delete ──────────────────────────────────────────────────────

func (p *Project) Delete(ctx context.Context, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	client, err := p.projectFactory(p.cfg)
	if err != nil {
		return nil, err
	}
	return p.deleteWithClient(ctx, client, request)
}

func (p *Project) deleteWithClient(ctx context.Context, client projectClientInterface, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	// An already-gone project is success, so a partially-completed delete stays
	// retryable.
	if _, err := client.DeleteProject(ctx, &codebuildsdk.DeleteProjectInput{
		Name: aws.String(request.NativeID),
	}); err != nil && !isCodeBuildNotFound(err) {
		return nil, fmt.Errorf("deleting CodeBuild project %q: %w", request.NativeID, err)
	}
	return &resource.DeleteResult{ProgressResult: &resource.ProgressResult{
		Operation:       resource.OperationDelete,
		OperationStatus: resource.OperationStatusSuccess,
		NativeID:        request.NativeID,
	}}, nil
}

// ── Status / List ───────────────────────────────────────────────

func (p *Project) Status(_ context.Context, _ *resource.StatusRequest) (*resource.StatusResult, error) {
	// Create and Update both complete synchronously, so no status is ever polled.
	return nil, fmt.Errorf("operation not implemented - create and update complete synchronously")
}

func (p *Project) List(_ context.Context, _ *resource.ListRequest) (*resource.ListResult, error) {
	// discoverable = false: the modelled property surface is narrower than the
	// CodeBuild API's, so an externally-created project is never adopted.
	return &resource.ListResult{NativeIDs: []string{}}, nil
}

// ── property → API input ────────────────────────────────────────

func createProjectInput(props projectProperties) *codebuildsdk.CreateProjectInput {
	in := &codebuildsdk.CreateProjectInput{
		Name:                   aws.String(props.Name),
		ServiceRole:            aws.String(props.ServiceRole),
		Source:                 sdkSource(props.Source),
		Artifacts:              sdkArtifacts(props.Artifacts),
		Environment:            sdkEnvironment(props.Environment),
		Cache:                  sdkCache(props.Cache),
		LogsConfig:             sdkLogsConfig(props.LogsConfig),
		TimeoutInMinutes:       props.TimeoutInMinutes,
		QueuedTimeoutInMinutes: props.QueuedTimeoutInMinutes,
		ConcurrentBuildLimit:   props.ConcurrentBuildLimit,
		Tags:                   sdkTags(props.Tags),
	}
	if props.Description != "" {
		in.Description = aws.String(props.Description)
	}
	return in
}

// updateProjectInput sends every supported field on every call. UpdateProject
// leaves an omitted field untouched, so a property the forma no longer declares
// is sent in its clearing representation rather than left out.
func updateProjectInput(props projectProperties) *codebuildsdk.UpdateProjectInput {
	cache := sdkCache(props.Cache)
	if cache == nil {
		cache = &codebuildtypes.ProjectCache{Type: codebuildtypes.CacheTypeNoCache}
	}
	logs := sdkLogsConfig(props.LogsConfig)
	if logs == nil {
		logs = &codebuildtypes.LogsConfig{}
	}
	if logs.CloudWatchLogs == nil {
		logs.CloudWatchLogs = &codebuildtypes.CloudWatchLogsConfig{Status: codebuildtypes.LogsConfigStatusTypeDisabled}
	}
	if logs.S3Logs == nil {
		logs.S3Logs = &codebuildtypes.S3LogsConfig{Status: codebuildtypes.LogsConfigStatusTypeDisabled}
	}
	return &codebuildsdk.UpdateProjectInput{
		Name:                   aws.String(props.Name),
		Description:            aws.String(props.Description),
		ServiceRole:            aws.String(props.ServiceRole),
		Source:                 sdkSource(props.Source),
		Artifacts:              sdkArtifacts(props.Artifacts),
		Environment:            sdkEnvironment(props.Environment),
		Cache:                  cache,
		LogsConfig:             logs,
		TimeoutInMinutes:       int32OrDefault(props.TimeoutInMinutes, defaultProjectTimeoutInMinutes),
		QueuedTimeoutInMinutes: int32OrDefault(props.QueuedTimeoutInMinutes, defaultProjectQueuedTimeoutInMinutes),
		ConcurrentBuildLimit:   int32OrDefault(props.ConcurrentBuildLimit, clearedConcurrentBuildLimit),
		Tags:                   sdkTags(props.Tags),
	}
}

func int32OrDefault(v *int32, fallback int32) *int32 {
	if v != nil {
		return v
	}
	return aws.Int32(fallback)
}

func sdkSource(src *projectSource) *codebuildtypes.ProjectSource {
	if src == nil {
		return nil
	}
	out := &codebuildtypes.ProjectSource{Type: codebuildtypes.SourceType(src.Type)}
	if src.BuildSpec != "" {
		out.Buildspec = aws.String(src.BuildSpec)
	}
	if src.Location != "" {
		out.Location = aws.String(src.Location)
	}
	return out
}

func sdkArtifacts(art *projectArtifacts) *codebuildtypes.ProjectArtifacts {
	if art == nil {
		return nil
	}
	out := &codebuildtypes.ProjectArtifacts{
		Type:      codebuildtypes.ArtifactsType(art.Type),
		Packaging: codebuildtypes.ArtifactPackaging(art.Packaging),
	}
	if art.Location != "" {
		out.Location = aws.String(art.Location)
	}
	if art.Name != "" {
		out.Name = aws.String(art.Name)
	}
	return out
}

// sdkEnvironment always sends a non-nil EnvironmentVariables list, so an update
// that declares none clears the ones a previous revision set.
func sdkEnvironment(env *projectEnvironment) *codebuildtypes.ProjectEnvironment {
	if env == nil {
		return nil
	}
	vars := make([]codebuildtypes.EnvironmentVariable, 0, len(env.EnvironmentVariables))
	for _, v := range env.EnvironmentVariables {
		vars = append(vars, codebuildtypes.EnvironmentVariable{
			Name:  aws.String(v.Name),
			Value: aws.String(v.Value),
			Type:  codebuildtypes.EnvironmentVariableType(v.Type),
		})
	}
	return &codebuildtypes.ProjectEnvironment{
		Type:                     codebuildtypes.EnvironmentType(env.Type),
		ComputeType:              codebuildtypes.ComputeType(env.ComputeType),
		Image:                    aws.String(env.Image),
		PrivilegedMode:           env.PrivilegedMode,
		ImagePullCredentialsType: codebuildtypes.ImagePullCredentialsType(env.ImagePullCredentialsType),
		EnvironmentVariables:     vars,
	}
}

func sdkCache(cache *projectCache) *codebuildtypes.ProjectCache {
	if cache == nil {
		return nil
	}
	out := &codebuildtypes.ProjectCache{Type: codebuildtypes.CacheType(cache.Type)}
	if cache.Location != "" {
		out.Location = aws.String(cache.Location)
	}
	for _, m := range cache.Modes {
		out.Modes = append(out.Modes, codebuildtypes.CacheMode(m))
	}
	return out
}

func sdkLogsConfig(logs *projectLogsConfig) *codebuildtypes.LogsConfig {
	if logs == nil {
		return nil
	}
	out := &codebuildtypes.LogsConfig{}
	if cw := logs.CloudWatchLogs; cw != nil {
		out.CloudWatchLogs = &codebuildtypes.CloudWatchLogsConfig{Status: codebuildtypes.LogsConfigStatusType(cw.Status)}
		if cw.GroupName != "" {
			out.CloudWatchLogs.GroupName = aws.String(cw.GroupName)
		}
		if cw.StreamName != "" {
			out.CloudWatchLogs.StreamName = aws.String(cw.StreamName)
		}
	}
	if s3 := logs.S3Logs; s3 != nil {
		out.S3Logs = &codebuildtypes.S3LogsConfig{
			Status:             codebuildtypes.LogsConfigStatusType(s3.Status),
			EncryptionDisabled: s3.EncryptionDisabled,
		}
		if s3.Location != "" {
			out.S3Logs.Location = aws.String(s3.Location)
		}
	}
	return out
}

// sdkTags always returns a non-nil slice: CodeBuild replaces a project's tag set
// wholesale, so an update that declares no tags must send an empty list to remove
// the ones a previous revision set.
func sdkTags(tags []projectTag) []codebuildtypes.Tag {
	out := make([]codebuildtypes.Tag, 0, len(tags))
	for _, t := range tags {
		out = append(out, codebuildtypes.Tag{Key: aws.String(t.Key), Value: aws.String(t.Value)})
	}
	return out
}

// ── API response → properties ───────────────────────────────────

// propertiesFromProject maps a project as CodeBuild returns it onto the schema's
// property shape. Create and Update both persist their own API response, so the
// provider-populated ARN and defaults land without a read-after-write.
func propertiesFromProject(project *codebuildtypes.Project) projectProperties {
	props := projectProperties{
		Name:                   aws.ToString(project.Name),
		Description:            aws.ToString(project.Description),
		ServiceRole:            aws.ToString(project.ServiceRole),
		TimeoutInMinutes:       project.TimeoutInMinutes,
		QueuedTimeoutInMinutes: project.QueuedTimeoutInMinutes,
		ConcurrentBuildLimit:   project.ConcurrentBuildLimit,
		Arn:                    aws.ToString(project.Arn),
	}
	if src := project.Source; src != nil {
		props.Source = &projectSource{
			Type:      string(src.Type),
			BuildSpec: aws.ToString(src.Buildspec),
			Location:  aws.ToString(src.Location),
		}
	}
	if art := project.Artifacts; art != nil {
		props.Artifacts = &projectArtifacts{
			Type:      string(art.Type),
			Location:  aws.ToString(art.Location),
			Name:      aws.ToString(art.Name),
			Packaging: string(art.Packaging),
		}
	}
	if env := project.Environment; env != nil {
		props.Environment = &projectEnvironment{
			Type:                     string(env.Type),
			ComputeType:              string(env.ComputeType),
			Image:                    aws.ToString(env.Image),
			PrivilegedMode:           env.PrivilegedMode,
			ImagePullCredentialsType: string(env.ImagePullCredentialsType),
		}
		for _, v := range env.EnvironmentVariables {
			props.Environment.EnvironmentVariables = append(props.Environment.EnvironmentVariables, projectEnvironmentVariable{
				Name:  aws.ToString(v.Name),
				Value: aws.ToString(v.Value),
				Type:  string(v.Type),
			})
		}
	}
	if cache := project.Cache; cache != nil {
		props.Cache = &projectCache{
			Type:     string(cache.Type),
			Location: aws.ToString(cache.Location),
		}
		for _, m := range cache.Modes {
			props.Cache.Modes = append(props.Cache.Modes, string(m))
		}
	}
	if logs := project.LogsConfig; logs != nil {
		props.LogsConfig = &projectLogsConfig{}
		if cw := logs.CloudWatchLogs; cw != nil {
			props.LogsConfig.CloudWatchLogs = &projectCloudWatchLogs{
				Status:     string(cw.Status),
				GroupName:  aws.ToString(cw.GroupName),
				StreamName: aws.ToString(cw.StreamName),
			}
		}
		if s3 := logs.S3Logs; s3 != nil {
			props.LogsConfig.S3Logs = &projectS3Logs{
				Status:             string(s3.Status),
				Location:           aws.ToString(s3.Location),
				EncryptionDisabled: s3.EncryptionDisabled,
			}
		}
	}
	for _, t := range project.Tags {
		props.Tags = append(props.Tags, projectTag{Key: aws.ToString(t.Key), Value: aws.ToString(t.Value)})
	}
	return props
}

// ── error classification ────────────────────────────────────────

func isProjectAlreadyExists(err error) bool {
	var exists *codebuildtypes.ResourceAlreadyExistsException
	return errors.As(err, &exists)
}
