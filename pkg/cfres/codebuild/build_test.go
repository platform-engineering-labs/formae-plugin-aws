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

func TestNormalizeInputDefaultsAndSort(t *testing.T) {
	n := normalizeInput(agentImageInput{
		EcrRepositoryURI: "123456789012.dkr.ecr.us-east-1.amazonaws.com/formae-agent",
		ImageTag:         "0.1.0",
		BaseImage:        "ghcr.io/platform-engineering-labs/formae:0.87.0",
		Plugins: []pluginSpec{
			{Name: "grafana", Version: "0.1.5"},
			{Name: "aws", Version: "0.1.13", Channel: "dev"},
		},
	})
	assert.Equal(t, defaultComputeType, n.ComputeType)
	assert.Equal(t, defaultTimeoutMinutes, n.TimeoutMinutes)
	assert.Equal(t, defaultBuildEnvimage, n.BuildEnvironmentImage)
	// Sorted by name; empty channel defaulted to stable.
	require.Len(t, n.Plugins, 2)
	assert.Equal(t, "aws", n.Plugins[0].Name)
	assert.Equal(t, "dev", n.Plugins[0].Channel)
	assert.Equal(t, "grafana", n.Plugins[1].Name)
	assert.Equal(t, "stable", n.Plugins[1].Channel)
}

func validInput() agentImageInput {
	return agentImageInput{
		EcrRepositoryURI: "123456789012.dkr.ecr.us-east-1.amazonaws.com/formae-agent",
		ImageTag:         "0.87.0-custom.1",
		BaseImage:        "ghcr.io/platform-engineering-labs/formae:0.87.0",
		Plugins:          []pluginSpec{{Name: "aws", Version: "0.1.13-dev.1", Channel: "dev"}},
	}
}

func TestValidateInputAcceptsValid(t *testing.T) {
	require.NoError(t, validateInput(validInput()))
}

func TestValidateInputRejects(t *testing.T) {
	cases := map[string]func(*agentImageInput){
		"missing repo":       func(i *agentImageInput) { i.EcrRepositoryURI = "" },
		"bad repo":           func(i *agentImageInput) { i.EcrRepositoryURI = "not-ecr" },
		"missing tag":        func(i *agentImageInput) { i.ImageTag = "" },
		"bad tag":            func(i *agentImageInput) { i.ImageTag = "bad tag!" },
		"missing base":       func(i *agentImageInput) { i.BaseImage = "" },
		"base with space":    func(i *agentImageInput) { i.BaseImage = "foo bar" },
		"bad compute":        func(i *agentImageInput) { i.ComputeType = "HUGE" },
		"timeout too small":  func(i *agentImageInput) { i.TimeoutMinutes = 1 },
		"timeout too big":    func(i *agentImageInput) { i.TimeoutMinutes = 999 },
		"bad plugin name":    func(i *agentImageInput) { i.Plugins = []pluginSpec{{Name: "Bad_Name", Version: "1.0.0"}} },
		"bad plugin version": func(i *agentImageInput) { i.Plugins = []pluginSpec{{Name: "aws", Version: "latest"}} },
		"bad plugin channel": func(i *agentImageInput) { i.Plugins = []pluginSpec{{Name: "aws", Version: "1.0.0", Channel: "nightly"}} },
		"duplicate plugin":   func(i *agentImageInput) { i.Plugins = []pluginSpec{{Name: "aws", Version: "1.0.0"}, {Name: "aws", Version: "1.0.1"}} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			in := validInput()
			mutate(&in)
			assert.Error(t, validateInput(in))
		})
	}
}

func TestGenerateDockerfileNoPlugins(t *testing.T) {
	df := generateDockerfile("public.ecr.aws/docker/library/alpine:3.20", nil)
	assert.Equal(t, "FROM public.ecr.aws/docker/library/alpine:3.20\n", df)
	// No pel-user dance when there is nothing to install.
	assert.NotContains(t, df, "USER pel")
	assert.NotContains(t, df, "chown")
}

func TestGenerateDockerfileWithPlugins(t *testing.T) {
	df := generateDockerfile("ghcr.io/platform-engineering-labs/formae:0.87.0", []pluginSpec{
		{Name: "aws", Version: "0.1.13-dev.1", Channel: "dev"},
		{Name: "grafana", Version: "0.1.5", Channel: "stable"},
	})
	assert.True(t, strings.HasPrefix(df, "FROM ghcr.io/platform-engineering-labs/formae:0.87.0\n"))
	assert.Contains(t, df, "USER root\n")
	assert.Contains(t, df, "RUN formae plugin install --channel dev aws@0.1.13-dev.1\n")
	assert.Contains(t, df, "RUN formae plugin install --channel stable grafana@0.1.5\n")
	assert.Contains(t, df, "rm -rf /home/pel/.pel/formae/plugins && chown -R pel:pel /opt/pel")
	assert.True(t, strings.HasSuffix(df, "USER pel\n"))
	// root install happens before dropping privileges.
	assert.Less(t, strings.Index(df, "USER root"), strings.Index(df, "USER pel"))
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
}

func TestBuildConfigHashStableAndSensitive(t *testing.T) {
	base := validInput()
	h1 := computeBuildConfigHash(base)
	// Recomputing is stable.
	assert.Equal(t, h1, computeBuildConfigHash(base))

	// Plugin ordering does not change the hash.
	reordered := validInput()
	reordered.Plugins = append([]pluginSpec{{Name: "grafana", Version: "0.1.5"}}, reordered.Plugins...)
	shuffled := validInput()
	shuffled.Plugins = []pluginSpec{{Name: "aws", Version: "0.1.13-dev.1", Channel: "dev"}, {Name: "grafana", Version: "0.1.5"}}
	assert.Equal(t, computeBuildConfigHash(reordered), computeBuildConfigHash(shuffled))

	// Non-build-affecting fields do not change the hash.
	nonBuild := validInput()
	nonBuild.TimeoutMinutes = 45
	nonBuild.ServiceRoleArn = "arn:aws:iam::123456789012:role/custom"
	assert.Equal(t, h1, computeBuildConfigHash(nonBuild))

	// Build-affecting changes DO change the hash.
	for _, mutate := range []func(*agentImageInput){
		func(i *agentImageInput) { i.BaseImage = "ghcr.io/platform-engineering-labs/formae:0.88.0" },
		func(i *agentImageInput) { i.Plugins = []pluginSpec{{Name: "aws", Version: "0.1.14", Channel: "dev"}} },
		func(i *agentImageInput) { i.Plugins[0].Channel = "stable" },
		func(i *agentImageInput) { i.ComputeType = "BUILD_GENERAL1_LARGE" },
	} {
		in := validInput()
		mutate(&in)
		assert.NotEqual(t, h1, computeBuildConfigHash(in))
	}
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
	pol := buildInlinePolicy(ref, "formae-agentimg-abc123")

	var doc map[string]any
	require.NoError(t, json.Unmarshal([]byte(pol), &doc))
	assert.Contains(t, pol, "ecr:GetAuthorizationToken")
	assert.Contains(t, pol, "ecr:PutImage")
	assert.Contains(t, pol, "arn:aws:ecr:us-east-1:123456789012:repository/formae-agent")
	assert.Contains(t, pol, "logs:CreateLogGroup")
	assert.Contains(t, pol, "logs:PutLogEvents")
	assert.Contains(t, pol, "arn:aws:logs:us-east-1:123456789012:log-group:/aws/codebuild/formae-agentimg-abc123")
}

func TestImageURI(t *testing.T) {
	assert.Equal(t, "repo:tag", imageURI("repo", "tag"))
}
