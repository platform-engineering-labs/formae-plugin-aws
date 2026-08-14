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
	"maps"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	codebuildtypes "github.com/aws/aws-sdk-go-v2/service/codebuild/types"
)

// generatorVersion is bumped whenever the generated buildspec changes shape, so
// that a generator change forces a rebuild via the build-config hash even when the
// user's inputs are identical.
const generatorVersion = "2"

// buildConfigHashScheme prefixes every hash this version produces. It identifies
// which set of inputs the hash was taken over, so a hash recorded by an earlier
// plugin version is recognisable as belonging to a different scheme instead of
// being compared as though it were comparable.
const buildConfigHashScheme = "v3"

// maxAdditionalTags bounds the pins one build may place. Placement is a serial ECR
// call per pin against a build that has already succeeded, so the bound keeps a
// runaway listing from turning a finished build into a long tail of registry writes.
const maxAdditionalTags = 20

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
	// legacyBuildConfigHashPattern matches a hash recorded before the scheme prefix
	// existed: a bare, unprefixed sha256 hex digest.
	legacyBuildConfigHashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// imageBuildInput mirrors the Pkl schema's input fields (capitalized to match the
// plugin wire format's output-key transformation).
type imageBuildInput struct {
	EcrRepositoryURI string            `json:"EcrRepositoryUri"`
	ImageTag         string            `json:"ImageTag"`
	Dockerfile       string            `json:"Dockerfile"`
	BuildArgs        map[string]string `json:"BuildArgs,omitempty"`
	ProjectName      string            `json:"ProjectName"`
	AdditionalTags   []string          `json:"AdditionalTags,omitempty"`
}

// imageBuildOutputs is the computed read-only state persisted in ResourceProperties
// and surfaced as the resource's resolvable outputs.
// AdditionalTags is echoed rather than computed: it is the declared listing, not an
// output, and it rides here because the caller rebuilds its stored model of a
// list-valued property from the properties the plugin returns.
type imageBuildOutputs struct {
	ImageRef        string   `json:"ImageRef,omitempty"`
	ImageDigest     string   `json:"ImageDigest,omitempty"`
	ImageURI        string   `json:"ImageUri,omitempty"`
	ImageTag        string   `json:"ImageTag,omitempty"`
	BuildConfigHash string   `json:"BuildConfigHash,omitempty"`
	AdditionalTags  []string `json:"AdditionalTags,omitempty"`
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
	if len(in.AdditionalTags) > maxAdditionalTags {
		return fmt.Errorf("at most %d additionalTags are allowed, got %d", maxAdditionalTags, len(in.AdditionalTags))
	}
	seen := make(map[string]struct{}, len(in.AdditionalTags))
	for _, tag := range in.AdditionalTags {
		if !imageTagPattern.MatchString(tag) {
			return fmt.Errorf("invalid additionalTag %q", tag)
		}
		// A pin naming the mutable tag would be moved by the next rebuild, which is
		// the one thing a pin exists not to be.
		if tag == in.ImageTag {
			return fmt.Errorf("invalid additionalTag %q: must not equal imageTag", tag)
		}
		if _, dup := seen[tag]; dup {
			return fmt.Errorf("duplicate additionalTag %q", tag)
		}
		seen[tag] = struct{}{}
	}
	return nil
}

// newPins returns the pins this apply declares for the first time, in declared
// order: those absent from the previously declared listing. A pin already declared
// is carried over and left exactly where it is, so only these are ever placed.
// Every pin is new on a create, where there is no prior.
func newPins(prior, desired imageBuildInput) []string {
	previously := make(map[string]struct{}, len(prior.AdditionalTags))
	for _, tag := range prior.AdditionalTags {
		previously[tag] = struct{}{}
	}
	var fresh []string
	for _, tag := range desired.AdditionalTags {
		if _, carried := previously[tag]; !carried {
			fresh = append(fresh, tag)
		}
	}
	return fresh
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

// computeBuildConfigHash hashes the build-affecting inputs — this resource's own
// inputs, the generator version, and the fingerprint of the project the build runs
// on — and returns them under the current scheme prefix.
func computeBuildConfigHash(in imageBuildInput, project *codebuildtypes.Project) string {
	sum := sha256.Sum256([]byte(buildConfigHashBody(in, project)))
	return buildConfigHashScheme + ":" + hex.EncodeToString(sum[:])
}

// buildConfigHashBody renders the canonical, hashed representation of everything
// that determines the image a build produces. Map- and list-valued inputs are
// emitted in a canonical order, and every value is quoted, so an identical
// configuration always renders identically and two different configurations never
// render alike — a Dockerfile or a build-arg value containing a newline would
// otherwise render as extra lines of the body.
//
// additionalTags is deliberately absent: a pin names a manifest, it does not
// change the one a build produces. Hashing it would make adding a pin force a
// rebuild, and the rebuilt image is precisely not the image the pin was meant to
// name.
func buildConfigHashBody(in imageBuildInput, project *codebuildtypes.Project) string {
	var b strings.Builder
	b.WriteString("v=" + strconv.Quote(generatorVersion) + "\n")
	b.WriteString("dockerfile=" + strconv.Quote(in.Dockerfile) + "\n")
	keys := make([]string, 0, len(in.BuildArgs))
	for k := range in.BuildArgs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		b.WriteString("arg=" + strconv.Quote(k) + "=" + strconv.Quote(in.BuildArgs[k]) + "\n")
	}
	b.WriteString(projectFingerprint(project))
	return b.String()
}

// projectFingerprint renders the properties of the referenced build project that
// determine what a build on it produces: the project it runs on and its build
// environment. The compute size and builder image belong to the project rather
// than to this resource, but they genuinely change the built image, so a mutation
// of the project has to invalidate it — otherwise an operator could swap the
// builder image and be told nothing changed. Properties that do not change the
// image a build produces (a description, the project's timeout) are deliberately
// left out, so churn on them does not force a rebuild.
func projectFingerprint(project *codebuildtypes.Project) string {
	var (
		name          string
		envType       codebuildtypes.EnvironmentType
		computeType   codebuildtypes.ComputeType
		image         string
		privileged    bool
		envVars       []codebuildtypes.EnvironmentVariable
		cacheType     codebuildtypes.CacheType
		cacheModes    []codebuildtypes.CacheMode
		cacheLocation string
	)
	if project != nil {
		name = aws.ToString(project.Name)
		if env := project.Environment; env != nil {
			envType = env.Type
			computeType = env.ComputeType
			image = aws.ToString(env.Image)
			privileged = aws.ToBool(env.PrivilegedMode)
			envVars = env.EnvironmentVariables
		}
		if cache := project.Cache; cache != nil {
			cacheType = cache.Type
			cacheModes = cache.Modes
			cacheLocation = aws.ToString(cache.Location)
		}
	}

	// The project's own environment variables are visible to the build (the plugin
	// overrides only the ones it sets), so they are part of the fingerprint. A
	// variable's type is too: the same name and value resolved from Parameter Store
	// is a different build input than a plaintext literal.
	varLines := make([]string, 0, len(envVars))
	for _, v := range envVars {
		varLines = append(varLines, "project.environmentVariable="+
			strconv.Quote(aws.ToString(v.Name))+"="+
			strconv.Quote(string(v.Type))+"="+
			strconv.Quote(aws.ToString(v.Value))+"\n")
	}
	sort.Strings(varLines)

	// The cache mode is what selects Docker layer caching — the thing that lets a
	// build reuse previously built layers — and it changes with the cache type left
	// at LOCAL, so the type alone would not see it. The location is the bucket and
	// prefix an S3 cache reads and writes.
	modeLines := make([]string, 0, len(cacheModes))
	for _, m := range cacheModes {
		modeLines = append(modeLines, "project.cacheMode="+strconv.Quote(string(m))+"\n")
	}
	sort.Strings(modeLines)

	var b strings.Builder
	b.WriteString("project=" + strconv.Quote(name) + "\n")
	b.WriteString("project.environmentType=" + strconv.Quote(string(envType)) + "\n")
	b.WriteString("project.computeType=" + strconv.Quote(string(computeType)) + "\n")
	b.WriteString("project.image=" + strconv.Quote(image) + "\n")
	b.WriteString("project.privilegedMode=" + strconv.FormatBool(privileged) + "\n")
	for _, l := range varLines {
		b.WriteString(l)
	}
	b.WriteString("project.cacheType=" + strconv.Quote(string(cacheType)) + "\n")
	for _, l := range modeLines {
		b.WriteString(l)
	}
	b.WriteString("project.cacheLocation=" + strconv.Quote(cacheLocation) + "\n")
	return b.String()
}

// isLegacyBuildConfigHash reports whether a recorded hash predates the scheme
// prefix. Such a hash was taken over a different set of inputs, so it can neither
// match nor be meaningfully compared against a current one — it only says that
// some earlier build produced the recorded image.
//
// This recognizes exactly the one format that came before the prefix. A future
// scheme bump wants the general form of the same rule — adopt a prior hash under
// any scheme other than the current one, not just a bare-hex one — otherwise that
// bump reopens the same forced re-push of an already pushed tag this predicate
// exists to prevent.
//
// Note that for the one scheme change this currently recognizes, the adoption
// path it feeds is defence-in-depth rather than a live migration path. A row
// carrying a bare-hex hash was written before ImageBuild referenced a project, so
// it also carries a two-segment NativeID that the current parse rejects, and
// adding the now-required projectName re-creates the resource rather than
// updating it. No row any released version wrote satisfies both conditions at
// once. The predicate and its adoption branch are kept because the next scheme
// bump does hit exactly this case, with rows whose NativeID is current.
func isLegacyBuildConfigHash(hash string) bool {
	return legacyBuildConfigHashPattern.MatchString(hash)
}

// priorInputsUnchanged reports whether this resource's own build-affecting inputs
// are unchanged since the recorded build, which is the condition under which a
// hash from an older scheme may be adopted rather than rebuilt. Adoption also
// writes the current hash back, so adopting across a changed Dockerfile or build
// arg would record that the new inputs had been built and leave the declared
// Dockerfile and the deployed image permanently diverged — the following apply
// would compare equal and never build the change.
//
// A row that recorded no Dockerfile at all cannot be compared, and is treated as
// unchanged so it can still migrate: the alternative is a rebuild that re-pushes an
// existing tag, which an immutable repository rejects outright.
func priorInputsUnchanged(prior, desired imageBuildInput) bool {
	if prior.Dockerfile == "" {
		return true
	}
	// Compared as maps rather than as their rendered form, so a value containing a
	// newline cannot render like a different set of args and pass as unchanged.
	return prior.Dockerfile == desired.Dockerfile &&
		maps.Equal(prior.BuildArgs, desired.BuildArgs)
}

// imageURI returns the mutable registry/repo:tag reference.
func imageURI(repoURI, tag string) string { return repoURI + ":" + tag }
