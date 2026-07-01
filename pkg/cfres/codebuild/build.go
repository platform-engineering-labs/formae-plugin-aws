// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

// Package codebuild implements the AWS::CodeBuild::AgentImage custom provisioner:
// a synthetic, imperative-during-apply resource that builds and pushes a custom
// formae agent image via AWS CodeBuild and returns the pushed image's immutable
// digest reference as computed outputs. It is not a CloudControl passthrough.
package codebuild

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// generatorVersion is bumped whenever the generated Dockerfile or buildspec
// changes shape, so that a generator change forces a rebuild via the build-config
// hash even when the user's inputs are identical.
const generatorVersion = "1"

const (
	defaultComputeType      = "BUILD_GENERAL1_SMALL"
	defaultTimeoutMinutes   = 30
	defaultBuildEnvimage    = "aws/codebuild/standard:7.0"
	resourceNamePrefix      = "formae-agentimg-"
	dockerfileEnvVar        = "DOCKERFILE_B64"
	imageURIEnvVar          = "IMAGE_URI"
	ecrRepositoryURIEnvVar  = "ECR_REPOSITORY_URI"
	ecrRegistryEnvVar       = "ECR_REGISTRY"
	exportedDigestVar       = "IMAGE_DIGEST"
	exportedImageRefVar     = "IMAGE_REF"
	exportedImageURIVar     = "IMAGE_URI"
	inlinePolicyName        = "formae-agentimage-build"
)

var (
	computeTypes = map[string]struct{}{
		"BUILD_GENERAL1_SMALL":  {},
		"BUILD_GENERAL1_MEDIUM": {},
		"BUILD_GENERAL1_LARGE":  {},
	}

	ecrRepositoryURIPattern = regexp.MustCompile(`^([0-9]{12})\.dkr\.ecr\.([a-z0-9-]+)\.amazonaws\.com/(.+)$`)
	imageTagPattern         = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)
	baseImagePattern        = regexp.MustCompile(`^[A-Za-z0-9._:/@-]+$`)
	pluginNamePattern       = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	pluginVersionPattern    = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.]+)?$`)
)

// pluginSpec is one plugin to install into the image.
type pluginSpec struct {
	Name    string `json:"Name"`
	Version string `json:"Version"`
	Channel string `json:"Channel,omitempty"`
}

// agentImageInput mirrors the Pkl schema's input fields (capitalized to match the
// plugin wire format's output-key transformation).
type agentImageInput struct {
	EcrRepositoryURI      string       `json:"EcrRepositoryUri"`
	ImageTag              string       `json:"ImageTag"`
	BaseImage             string       `json:"BaseImage"`
	Plugins               []pluginSpec `json:"Plugins,omitempty"`
	ComputeType           string       `json:"ComputeType,omitempty"`
	TimeoutMinutes        int          `json:"TimeoutMinutes,omitempty"`
	ServiceRoleArn        string       `json:"ServiceRoleArn,omitempty"`
	BuildEnvironmentImage string       `json:"BuildEnvironmentImage,omitempty"`
}

// agentImageOutputs is the computed read-only state persisted in ResourceProperties
// and surfaced as the resource's resolvable outputs.
type agentImageOutputs struct {
	ImageRef        string `json:"ImageRef,omitempty"`
	ImageDigest     string `json:"ImageDigest,omitempty"`
	ImageURI        string `json:"ImageUri,omitempty"`
	ImageTag        string `json:"ImageTag,omitempty"`
	BuildConfigHash string `json:"BuildConfigHash,omitempty"`
}

// ecrRepositoryRef is the parsed form of an ECR repository URI.
type ecrRepositoryRef struct {
	AccountID string
	Region    string
	RepoName  string
	Registry  string // <account>.dkr.ecr.<region>.amazonaws.com
	URI       string // registry/repoName
}

// parseEcrRepositoryURI splits an ECR repository URI into its parts. It accepts
// the canonical form <account>.dkr.ecr.<region>.amazonaws.com/<repoName> and
// rejects anything else (including a URI that carries a :tag suffix).
func parseEcrRepositoryURI(uri string) (ecrRepositoryRef, error) {
	m := ecrRepositoryURIPattern.FindStringSubmatch(uri)
	if m == nil {
		return ecrRepositoryRef{}, fmt.Errorf("invalid ecrRepositoryUri %q: expected <account>.dkr.ecr.<region>.amazonaws.com/<repository>", uri)
	}
	repoName := m[3]
	if strings.ContainsAny(repoName, ":@") {
		return ecrRepositoryRef{}, fmt.Errorf("invalid ecrRepositoryUri %q: repository must not include a tag or digest", uri)
	}
	return ecrRepositoryRef{
		AccountID: m[1],
		Region:    m[2],
		RepoName:  repoName,
		Registry:  fmt.Sprintf("%s.dkr.ecr.%s.amazonaws.com", m[1], m[2]),
		URI:       uri,
	}, nil
}

// normalizeInput fills in defaults and sorts plugins by name so that build-affecting
// inputs have a canonical form (the build-config hash and Dockerfile are
// order-insensitive over plugins).
func normalizeInput(in agentImageInput) agentImageInput {
	out := in
	if out.ComputeType == "" {
		out.ComputeType = defaultComputeType
	}
	if out.TimeoutMinutes == 0 {
		out.TimeoutMinutes = defaultTimeoutMinutes
	}
	if out.BuildEnvironmentImage == "" {
		out.BuildEnvironmentImage = defaultBuildEnvimage
	}
	plugins := make([]pluginSpec, len(in.Plugins))
	copy(plugins, in.Plugins)
	for i := range plugins {
		if plugins[i].Channel == "" {
			plugins[i].Channel = "stable"
		}
	}
	sort.SliceStable(plugins, func(i, j int) bool { return plugins[i].Name < plugins[j].Name })
	out.Plugins = plugins
	return out
}

// validateInput rejects malformed inputs before anything is materialized into a
// build. The forma is operator-authored, so this is breakage-prevention, not an
// attacker boundary — but a strict check turns a silently-broken build into an
// immediate, actionable error.
func validateInput(in agentImageInput) error {
	if in.EcrRepositoryURI == "" {
		return fmt.Errorf("ecrRepositoryUri is required")
	}
	if _, err := parseEcrRepositoryURI(in.EcrRepositoryURI); err != nil {
		return err
	}
	if in.ImageTag == "" {
		return fmt.Errorf("imageTag is required")
	}
	if !imageTagPattern.MatchString(in.ImageTag) {
		return fmt.Errorf("invalid imageTag %q", in.ImageTag)
	}
	if in.BaseImage == "" {
		return fmt.Errorf("baseImage is required")
	}
	if !baseImagePattern.MatchString(in.BaseImage) {
		return fmt.Errorf("invalid baseImage %q", in.BaseImage)
	}
	if in.ComputeType != "" {
		if _, ok := computeTypes[in.ComputeType]; !ok {
			return fmt.Errorf("invalid computeType %q", in.ComputeType)
		}
	}
	if in.TimeoutMinutes != 0 && (in.TimeoutMinutes < 5 || in.TimeoutMinutes > 60) {
		return fmt.Errorf("timeoutMinutes must be between 5 and 60, got %d", in.TimeoutMinutes)
	}
	seen := make(map[string]struct{}, len(in.Plugins))
	for _, p := range in.Plugins {
		if !pluginNamePattern.MatchString(p.Name) {
			return fmt.Errorf("invalid plugin name %q", p.Name)
		}
		if !pluginVersionPattern.MatchString(p.Version) {
			return fmt.Errorf("invalid plugin version %q for %q", p.Version, p.Name)
		}
		if p.Channel != "" && p.Channel != "stable" && p.Channel != "dev" {
			return fmt.Errorf("invalid plugin channel %q for %q", p.Channel, p.Name)
		}
		if _, dup := seen[p.Name]; dup {
			return fmt.Errorf("duplicate plugin %q", p.Name)
		}
		seen[p.Name] = struct{}{}
	}
	return nil
}

// generateDockerfile synthesizes the Dockerfile from the base image plus plugins,
// mirroring the prod formae-agent image build: plugins install as root, then the
// per-user plugin cache is cleared and /opt/pel is chowned back to the pel user
// before dropping privileges. With no plugins the base image is rebuilt/retagged
// unchanged, so the root/ownership dance (which is specific to a formae base) is
// omitted.
func generateDockerfile(baseImage string, plugins []pluginSpec) string {
	var b strings.Builder
	fmt.Fprintf(&b, "FROM %s\n", baseImage)
	if len(plugins) > 0 {
		b.WriteString("USER root\n")
		for _, p := range plugins {
			channel := p.Channel
			if channel == "" {
				channel = "stable"
			}
			fmt.Fprintf(&b, "RUN formae plugin install --channel %s %s@%s\n", channel, p.Name, p.Version)
		}
		b.WriteString("RUN rm -rf /home/pel/.pel/formae/plugins && chown -R pel:pel /opt/pel\n")
		b.WriteString("USER pel\n")
	}
	return b.String()
}

// buildspec is the static CodeBuild buildspec. All build-varying values arrive as
// environment variables; the Dockerfile is materialized from a base64 env var so
// operator-supplied values never land in an unescaped shell context.
const buildspec = `version: 0.2
env:
  exported-variables:
    - ` + exportedDigestVar + `
    - ` + exportedImageRefVar + `
    - ` + exportedImageURIVar + `
phases:
  pre_build:
    commands:
      - printf '%s' "$` + dockerfileEnvVar + `" | base64 -d > Dockerfile
      - aws ecr get-login-password --region "$AWS_REGION" | docker login --username AWS --password-stdin "$` + ecrRegistryEnvVar + `"
  build:
    commands:
      - docker build --platform linux/amd64 -t "$` + imageURIEnvVar + `" .
      - docker push "$` + imageURIEnvVar + `"
  post_build:
    commands:
      - ` + exportedDigestVar + `="$(docker inspect --format='{{index .RepoDigests 0}}' "$` + imageURIEnvVar + `" | cut -d@ -f2)"
      - ` + exportedImageRefVar + `="$` + ecrRepositoryURIEnvVar + `@$` + exportedDigestVar + `"
      - ` + exportedImageURIVar + `="$` + imageURIEnvVar + `"
`

// generateBuildspec returns the buildspec. It is static; kept as a function so the
// call sites read uniformly and so tests can assert on its content.
func generateBuildspec() string { return buildspec }

// computeBuildConfigHash hashes exactly the build-affecting inputs plus the
// generator version. timeoutMinutes and serviceRoleArn affect whether a build
// succeeds but not what is built, so they are excluded.
func computeBuildConfigHash(in agentImageInput) string {
	n := normalizeInput(in)
	var b strings.Builder
	b.WriteString("v=" + generatorVersion + "\n")
	b.WriteString("base=" + n.BaseImage + "\n")
	b.WriteString("compute=" + n.ComputeType + "\n")
	b.WriteString("env=" + n.BuildEnvironmentImage + "\n")
	for _, p := range n.Plugins {
		b.WriteString("plugin=" + p.Name + "@" + p.Version + "#" + p.Channel + "\n")
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// resourceNames returns the deterministic, bounded names of the internally-managed
// CodeBuild project and IAM role for a given push target. The names are derived
// from the repository URI and tag alone (both available from the NativeID), so
// Create, Status, and Delete all reconstruct the same names without needing the
// resource label or stack.
func resourceNames(repoURI, tag string) (projectName, roleName string) {
	sum := sha256.Sum256([]byte(repoURI + "|" + tag))
	short := hex.EncodeToString(sum[:])[:12]
	projectName = resourceNamePrefix + short
	roleName = resourceNamePrefix + short + "-role"
	return projectName, roleName
}

// imageURI returns the mutable registry/repo:tag reference.
func imageURI(repoURI, tag string) string { return repoURI + ":" + tag }

// policyDocument, policyStatement model an IAM policy for JSON marshaling.
type policyDocument struct {
	Version   string            `json:"Version"`
	Statement []policyStatement `json:"Statement"`
}

type policyStatement struct {
	Effect    string          `json:"Effect"`
	Action    []string        `json:"Action,omitempty"`
	Resource  any             `json:"Resource,omitempty"`
	Principal *policyPrincipal `json:"Principal,omitempty"`
}

type policyPrincipal struct {
	Service string `json:"Service"`
}

// buildTrustPolicy returns the assume-role trust policy that lets CodeBuild assume
// the internal service role.
func buildTrustPolicy() string {
	doc := policyDocument{
		Version: "2012-10-17",
		Statement: []policyStatement{{
			Effect:    "Allow",
			Principal: &policyPrincipal{Service: "codebuild.amazonaws.com"},
			Action:    []string{"sts:AssumeRole"},
		}},
	}
	return mustMarshalPolicy(doc)
}

// buildInlinePolicy derives the service role's inline policy from what the
// generated buildspec actually does: pull/push the target ECR repository and write
// to the CodeBuild log group. It is scoped to the target repository and log group;
// this covers a public base image (no auth needed) or a same-account ECR base image
// (pulled through the same repository-scoped grant).
func buildInlinePolicy(ref ecrRepositoryRef, projectName string) string {
	ecrRepoArn := fmt.Sprintf("arn:aws:ecr:%s:%s:repository/%s", ref.Region, ref.AccountID, ref.RepoName)
	logGroupArn := fmt.Sprintf("arn:aws:logs:%s:%s:log-group:/aws/codebuild/%s", ref.Region, ref.AccountID, projectName)
	doc := policyDocument{
		Version: "2012-10-17",
		Statement: []policyStatement{
			{
				Effect:   "Allow",
				Action:   []string{"ecr:GetAuthorizationToken"},
				Resource: "*",
			},
			{
				Effect: "Allow",
				Action: []string{
					"ecr:BatchCheckLayerAvailability",
					"ecr:GetDownloadUrlForLayer",
					"ecr:BatchGetImage",
					"ecr:DescribeImages",
					"ecr:ListImages",
					"ecr:InitiateLayerUpload",
					"ecr:UploadLayerPart",
					"ecr:CompleteLayerUpload",
					"ecr:PutImage",
				},
				Resource: ecrRepoArn,
			},
			{
				Effect: "Allow",
				Action: []string{
					"logs:CreateLogGroup",
					"logs:CreateLogStream",
					"logs:PutLogEvents",
				},
				Resource: []string{logGroupArn, logGroupArn + ":*"},
			},
		},
	}
	return mustMarshalPolicy(doc)
}

func mustMarshalPolicy(doc policyDocument) string {
	b, err := json.Marshal(doc)
	if err != nil {
		// The policy documents are built from constants and validated inputs, so
		// marshaling cannot realistically fail; panic surfaces a programming error.
		panic(fmt.Sprintf("marshaling IAM policy: %v", err))
	}
	return string(b)
}
