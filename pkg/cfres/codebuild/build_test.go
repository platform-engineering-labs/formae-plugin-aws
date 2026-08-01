// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package codebuild

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseEcrRepositoryURI(t *testing.T) {
	ref, err := parseEcrRepositoryURI("123456789012.dkr.ecr.us-east-1.amazonaws.com/formae-agent")
	require.NoError(t, err)
	assert.Equal(t, "123456789012", ref.AccountID)
	assert.Equal(t, "us-east-1", ref.Region)
	assert.Equal(t, "formae-agent", ref.RepoName)
	assert.Equal(t, "123456789012.dkr.ecr.us-east-1.amazonaws.com", ref.Registry)

	// Nested repository path is allowed.
	ref, err = parseEcrRepositoryURI("123456789012.dkr.ecr.eu-west-1.amazonaws.com/team/formae-agent")
	require.NoError(t, err)
	assert.Equal(t, "team/formae-agent", ref.RepoName)

	for _, bad := range []string{
		"",
		"formae-agent",
		"ghcr.io/platform-engineering-labs/formae",
		"123456789012.dkr.ecr.us-east-1.amazonaws.com/formae-agent:0.1.0", // carries a tag
		"123456789012.dkr.ecr.us-east-1.amazonaws.com/formae-agent@sha256:abc",
	} {
		_, err := parseEcrRepositoryURI(bad)
		assert.Error(t, err, "expected %q to be rejected", bad)
	}
}

func validInput() imageBuildInput {
	return imageBuildInput{
		EcrRepositoryURI: "123456789012.dkr.ecr.us-east-1.amazonaws.com/formae-agent",
		ImageTag:         "0.87.0-custom.1",
		Dockerfile:       "FROM public.ecr.aws/docker/library/alpine:3.20\nRUN true\n",
		ProjectName:      testBuildProject,
	}
}

func TestValidateInputAcceptsValid(t *testing.T) {
	require.NoError(t, validateInput(validInput()))
}

func TestValidateInputRejects(t *testing.T) {
	cases := map[string]func(*imageBuildInput){
		"missing repo":       func(i *imageBuildInput) { i.EcrRepositoryURI = "" },
		"bad repo":           func(i *imageBuildInput) { i.EcrRepositoryURI = "not-ecr" },
		"missing tag":        func(i *imageBuildInput) { i.ImageTag = "" },
		"bad tag":            func(i *imageBuildInput) { i.ImageTag = "bad tag!" },
		"missing dockerfile": func(i *imageBuildInput) { i.Dockerfile = "" },
		"missing project":    func(i *imageBuildInput) { i.ProjectName = "" },
		"bad project name":   func(i *imageBuildInput) { i.ProjectName = "not a project name" },
		"project with pipe":  func(i *imageBuildInput) { i.ProjectName = "left|right" },
		"bad buildArg key":   func(i *imageBuildInput) { i.BuildArgs = map[string]string{"bad key": "v"} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			in := validInput()
			mutate(&in)
			assert.Error(t, validateInput(in))
		})
	}
}

func TestBuildArgsFileSortedAndCanonical(t *testing.T) {
	assert.Equal(t, "", buildArgsFile(nil))
	assert.Equal(t, "", buildArgsFile(map[string]string{}))

	got := buildArgsFile(map[string]string{"ZED": "1", "ALPHA": "2", "BETA": "3"})
	assert.Equal(t, "ALPHA=2\nBETA=3\nZED=1\n", got)
}

func TestGenerateBuildspecShape(t *testing.T) {
	bs := generateBuildspec()
	assert.Contains(t, bs, "version: 0.2")
	assert.Contains(t, bs, "exported-variables")
	assert.Contains(t, bs, "IMAGE_DIGEST")
	assert.Contains(t, bs, "get-login-password")
	assert.Contains(t, bs, "docker build --platform linux/amd64")
	assert.Contains(t, bs, "docker push")
	assert.Contains(t, bs, "base64 -d > Dockerfile")
	// Build args are decoded and threaded in as quoted --build-arg flags via the
	// shell's positional parameters, so a value with spaces survives intact.
	assert.Contains(t, bs, "base64 -d > build_args.env")
	assert.Contains(t, bs, "--build-arg")
	assert.Contains(t, bs, `set -- "$@" --build-arg "$line"`)
	// The image is built from an isolated empty context with the Dockerfile passed
	// via -f, so neither the Dockerfile nor build_args.env is in the build context.
	assert.Contains(t, bs, "mkdir -p build-context")
	assert.Contains(t, bs, `docker build --platform linux/amd64 "$@" -f Dockerfile -t "$IMAGE_URI" build-context`)
	// Computed outputs are exported so CodeBuild's exported-variables collects them
	// after post_build.
	assert.Contains(t, bs, "export IMAGE_DIGEST=")
	assert.Contains(t, bs, "export IMAGE_REF=")
	// No formae-agent coupling in the generated build.
	assert.NotContains(t, bs, "formae plugin install")
	assert.NotContains(t, bs, "USER pel")
}

func TestBuildConfigHashStableAndSensitive(t *testing.T) {
	base := validInput()
	h1 := computeBuildConfigHash(base)
	// Recomputing is stable.
	assert.Equal(t, h1, computeBuildConfigHash(base))

	// Build-arg ordering does not change the hash (maps are canonicalized).
	a := validInput()
	a.BuildArgs = map[string]string{"A": "1", "B": "2"}
	b := validInput()
	b.BuildArgs = map[string]string{"B": "2", "A": "1"}
	assert.Equal(t, computeBuildConfigHash(a), computeBuildConfigHash(b))

	// Build-affecting changes DO change the hash.
	for _, mutate := range []func(*imageBuildInput){
		func(i *imageBuildInput) { i.Dockerfile = "FROM public.ecr.aws/docker/library/alpine:3.21\n" },
		func(i *imageBuildInput) { i.BuildArgs = map[string]string{"VERSION": "1.2.3"} },
	} {
		in := validInput()
		mutate(&in)
		assert.NotEqual(t, h1, computeBuildConfigHash(in))
	}

	// A build-arg value change changes the hash.
	v1 := validInput()
	v1.BuildArgs = map[string]string{"VERSION": "1.0.0"}
	v2 := validInput()
	v2.BuildArgs = map[string]string{"VERSION": "2.0.0"}
	assert.NotEqual(t, computeBuildConfigHash(v1), computeBuildConfigHash(v2))
}

func TestImageURI(t *testing.T) {
	assert.Equal(t, "repo:tag", imageURI("repo", "tag"))
}
