// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package codebuild

import (
	"encoding/json"
	"strings"
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

func TestNormalizeInputDefaults(t *testing.T) {
	n := normalizeInput(imageBuildInput{
		EcrRepositoryURI: "123456789012.dkr.ecr.us-east-1.amazonaws.com/formae-agent",
		ImageTag:         "0.1.0",
		Dockerfile:       "FROM alpine:3.20\n",
	})
	assert.Equal(t, defaultComputeType, n.ComputeType)
	assert.Equal(t, defaultTimeoutMinutes, n.TimeoutMinutes)
	assert.Equal(t, defaultBuildEnvimage, n.BuildEnvironmentImage)
}

func validInput() imageBuildInput {
	return imageBuildInput{
		EcrRepositoryURI: "123456789012.dkr.ecr.us-east-1.amazonaws.com/formae-agent",
		ImageTag:         "0.87.0-custom.1",
		Dockerfile:       "FROM public.ecr.aws/docker/library/alpine:3.20\nRUN true\n",
	}
}

func TestValidateInputAcceptsValid(t *testing.T) {
	require.NoError(t, validateInput(validInput()))
}

func TestValidateInputRejects(t *testing.T) {
	cases := map[string]func(*imageBuildInput){
		"missing repo":      func(i *imageBuildInput) { i.EcrRepositoryURI = "" },
		"bad repo":          func(i *imageBuildInput) { i.EcrRepositoryURI = "not-ecr" },
		"missing tag":       func(i *imageBuildInput) { i.ImageTag = "" },
		"bad tag":           func(i *imageBuildInput) { i.ImageTag = "bad tag!" },
		"missing dockerfile": func(i *imageBuildInput) { i.Dockerfile = "" },
		"bad compute":       func(i *imageBuildInput) { i.ComputeType = "HUGE" },
		"timeout too small": func(i *imageBuildInput) { i.TimeoutMinutes = 1 },
		"timeout too big":   func(i *imageBuildInput) { i.TimeoutMinutes = 999 },
		"bad buildArg key":  func(i *imageBuildInput) { i.BuildArgs = map[string]string{"bad key": "v"} },
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
	// Build args are decoded and threaded in as --build-arg flags.
	assert.Contains(t, bs, "base64 -d > build_args.env")
	assert.Contains(t, bs, "--build-arg")
	assert.Contains(t, bs, "$BUILD_ARG_FLAGS")
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

	// Non-build-affecting fields do not change the hash.
	nonBuild := validInput()
	nonBuild.TimeoutMinutes = 45
	nonBuild.ServiceRoleArn = "arn:aws:iam::123456789012:role/custom"
	assert.Equal(t, h1, computeBuildConfigHash(nonBuild))

	// Build-affecting changes DO change the hash.
	for _, mutate := range []func(*imageBuildInput){
		func(i *imageBuildInput) { i.Dockerfile = "FROM public.ecr.aws/docker/library/alpine:3.21\n" },
		func(i *imageBuildInput) { i.BuildArgs = map[string]string{"VERSION": "1.2.3"} },
		func(i *imageBuildInput) { i.ComputeType = "BUILD_GENERAL1_LARGE" },
		func(i *imageBuildInput) { i.BuildEnvironmentImage = "aws/codebuild/standard:8.0" },
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

func TestResourceNamesDeterministicAndBounded(t *testing.T) {
	p1, r1 := resourceNames("123456789012.dkr.ecr.us-east-1.amazonaws.com/formae-agent", "0.1.0")
	p2, r2 := resourceNames("123456789012.dkr.ecr.us-east-1.amazonaws.com/formae-agent", "0.1.0")
	assert.Equal(t, p1, p2)
	assert.Equal(t, r1, r2)

	// Different target → different names.
	p3, _ := resourceNames("123456789012.dkr.ecr.us-east-1.amazonaws.com/formae-agent", "0.2.0")
	assert.NotEqual(t, p1, p3)

	// CodeBuild project name limit is 255, IAM role name limit is 64.
	assert.LessOrEqual(t, len(p1), 255)
	assert.LessOrEqual(t, len(r1), 64)
	assert.Regexp(t, `^[A-Za-z0-9_-]+$`, p1)
	assert.Regexp(t, `^[A-Za-z0-9_-]+$`, r1)
	assert.True(t, strings.HasSuffix(r1, "-role"))
}

func TestTrustPolicyIsValidJSON(t *testing.T) {
	var doc map[string]any
	require.NoError(t, json.Unmarshal([]byte(buildTrustPolicy()), &doc))
	assert.Contains(t, buildTrustPolicy(), "codebuild.amazonaws.com")
	assert.Contains(t, buildTrustPolicy(), "sts:AssumeRole")
}

func TestInlinePolicyScopedToTargets(t *testing.T) {
	ref, err := parseEcrRepositoryURI("123456789012.dkr.ecr.us-east-1.amazonaws.com/formae-agent")
	require.NoError(t, err)
	pol := buildInlinePolicy(ref, "formae-imgbuild-abc123")

	var doc map[string]any
	require.NoError(t, json.Unmarshal([]byte(pol), &doc))
	assert.Contains(t, pol, "ecr:GetAuthorizationToken")
	assert.Contains(t, pol, "ecr:PutImage")
	assert.Contains(t, pol, "arn:aws:ecr:us-east-1:123456789012:repository/formae-agent")
	assert.Contains(t, pol, "logs:CreateLogGroup")
	assert.Contains(t, pol, "logs:PutLogEvents")
	assert.Contains(t, pol, "arn:aws:logs:us-east-1:123456789012:log-group:/aws/codebuild/formae-imgbuild-abc123")
}

func TestImageURI(t *testing.T) {
	assert.Equal(t, "repo:tag", imageURI("repo", "tag"))
}
