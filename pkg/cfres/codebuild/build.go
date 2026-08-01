// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

// Package codebuild implements the AWS::CodeBuild::Project and
// AWS::CodeBuild::ImageBuild custom provisioners. Project is a declared CodeBuild
// project managed over the CodeBuild API (the type is NON_PROVISIONABLE in
// CloudControl). ImageBuild is a synthetic, imperative-during-apply action that
// runs a build on a declared project to build and push a container image from a
// caller-supplied Dockerfile, and returns the pushed image's immutable digest
// reference as computed outputs. Neither is a CloudControl passthrough.
package codebuild

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// generatorVersion is bumped whenever the generated buildspec changes shape, so
// that a generator change forces a rebuild via the build-config hash even when the
// user's inputs are identical.
const generatorVersion = "2"

const (
	dockerfileEnvVar       = "DOCKERFILE_B64"
	buildArgsEnvVar        = "BUILD_ARGS_B64"
	imageURIEnvVar         = "IMAGE_URI"
	ecrRepositoryURIEnvVar = "ECR_REPOSITORY_URI"
	ecrRegistryEnvVar      = "ECR_REGISTRY"
	exportedDigestVar      = "IMAGE_DIGEST"
	exportedImageRefVar    = "IMAGE_REF"
	exportedImageURIVar    = "IMAGE_URI"
)

var (
	ecrRepositoryURIPattern = regexp.MustCompile(`^([0-9]{12})\.dkr\.ecr\.([a-z0-9-]+)\.amazonaws\.com/(.+)$`)
	imageTagPattern         = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)
	buildArgKeyPattern      = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	// CodeBuild's own project-name rule: 2 to 255 characters of letters, digits,
	// hyphens and underscores, starting with a letter or a digit.
	projectNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{1,254}$`)
)

// imageBuildInput mirrors the Pkl schema's input fields (capitalized to match the
// plugin wire format's output-key transformation).
type imageBuildInput struct {
	EcrRepositoryURI string            `json:"EcrRepositoryUri"`
	ImageTag         string            `json:"ImageTag"`
	Dockerfile       string            `json:"Dockerfile"`
	BuildArgs        map[string]string `json:"BuildArgs,omitempty"`
	ProjectName      string            `json:"ProjectName"`
}

// imageBuildOutputs is the computed read-only state persisted in ResourceProperties
// and surfaced as the resource's resolvable outputs.
type imageBuildOutputs struct {
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

// validateInput rejects malformed inputs before anything is materialized into a
// build. The forma is operator-authored, so this is breakage-prevention, not an
// attacker boundary — but a strict check turns a silently-broken build into an
// immediate, actionable error.
func validateInput(in imageBuildInput) error {
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
	if in.Dockerfile == "" {
		return fmt.Errorf("dockerfile is required")
	}
	if in.ProjectName == "" {
		return fmt.Errorf("projectName is required")
	}
	// The project name is a segment of the NativeID, which is '|'-separated.
	if strings.Contains(in.ProjectName, "|") {
		return fmt.Errorf("invalid projectName %q: must not contain '|'", in.ProjectName)
	}
	if !projectNamePattern.MatchString(in.ProjectName) {
		return fmt.Errorf("invalid projectName %q", in.ProjectName)
	}
	for k := range in.BuildArgs {
		if !buildArgKeyPattern.MatchString(k) {
			return fmt.Errorf("invalid buildArg key %q", k)
		}
	}
	return nil
}

// buildArgsFile renders the build args as a newline-separated KEY=VALUE list,
// sorted by key for a canonical form. It is base64-encoded and passed to the
// buildspec, which decodes it and turns each line into a `--build-arg` flag; this
// keeps operator-supplied values out of any unescaped shell context.
func buildArgsFile(args map[string]string) string {
	if len(args) == 0 {
		return ""
	}
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k + "=" + args[k] + "\n")
	}
	return b.String()
}

// buildspec is the static CodeBuild buildspec. All build-varying values arrive as
// environment variables; the Dockerfile and build args are materialized from
// base64 env vars so operator-supplied values never land in an unescaped shell
// context. Build args are read as KEY=VALUE lines into the shell's positional
// parameters and passed as quoted --build-arg flags, so a value containing spaces
// survives intact. The image is built from an empty build-context directory with the
// Dockerfile supplied via -f, so the generated Dockerfile and build_args.env (which
// hold operator-supplied values) are never part of the build context and cannot be
// captured by a broad COPY in the Dockerfile. The ECR repository is required to be in
// the build project's own region, so $AWS_REGION is the correct login region. The
// computed image reference is exported so CodeBuild's exported-variables collects it
// after post_build.
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
      - printf '%s' "$` + buildArgsEnvVar + `" | base64 -d > build_args.env
      - mkdir -p build-context
      - aws ecr get-login-password --region "$AWS_REGION" | docker login --username AWS --password-stdin "$` + ecrRegistryEnvVar + `"
  build:
    commands:
      - |
        set --
        while IFS= read -r line; do
          [ -n "$line" ] && set -- "$@" --build-arg "$line"
        done < build_args.env
        docker build --platform linux/amd64 "$@" -f Dockerfile -t "$` + imageURIEnvVar + `" build-context
      - docker push "$` + imageURIEnvVar + `"
  post_build:
    commands:
      - export ` + exportedDigestVar + `="$(docker inspect --format='{{index .RepoDigests 0}}' "$` + imageURIEnvVar + `" | cut -d@ -f2)"
      - export ` + exportedImageRefVar + `="$` + ecrRepositoryURIEnvVar + `@$` + exportedDigestVar + `"
      - export ` + exportedImageURIVar + `="$` + imageURIEnvVar + `"
`

// generateBuildspec returns the buildspec. It is static; kept as a function so the
// call sites read uniformly and so tests can assert on its content.
func generateBuildspec() string { return buildspec }

// computeBuildConfigHash hashes exactly the build-affecting inputs plus the
// generator version. The compute size and build-environment image now belong to
// the referenced project, which the build only runs on, so they are not part of
// this resource's own inputs.
func computeBuildConfigHash(in imageBuildInput) string {
	var b strings.Builder
	b.WriteString("v=" + generatorVersion + "\n")
	b.WriteString("dockerfile=" + in.Dockerfile + "\n")
	keys := make([]string, 0, len(in.BuildArgs))
	for k := range in.BuildArgs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		b.WriteString("arg=" + k + "=" + in.BuildArgs[k] + "\n")
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// imageURI returns the mutable registry/repo:tag reference.
func imageURI(repoURI, tag string) string { return repoURI + ":" + tag }
