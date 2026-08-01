// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package codebuild

import (
	"regexp"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	codebuildtypes "github.com/aws/aws-sdk-go-v2/service/codebuild/types"
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
	project := validProject()
	h1 := computeBuildConfigHash(base, &project)
	// Recomputing is stable.
	assert.Equal(t, h1, computeBuildConfigHash(base, &project))

	// Build-arg ordering does not change the hash (maps are canonicalized).
	a := validInput()
	a.BuildArgs = map[string]string{"A": "1", "B": "2"}
	b := validInput()
	b.BuildArgs = map[string]string{"B": "2", "A": "1"}
	assert.Equal(t, computeBuildConfigHash(a, &project), computeBuildConfigHash(b, &project))

	// Build-affecting changes DO change the hash.
	for _, mutate := range []func(*imageBuildInput){
		func(i *imageBuildInput) { i.Dockerfile = "FROM public.ecr.aws/docker/library/alpine:3.21\n" },
		func(i *imageBuildInput) { i.BuildArgs = map[string]string{"VERSION": "1.2.3"} },
	} {
		in := validInput()
		mutate(&in)
		assert.NotEqual(t, h1, computeBuildConfigHash(in, &project))
	}

	// A build-arg value change changes the hash.
	v1 := validInput()
	v1.BuildArgs = map[string]string{"VERSION": "1.0.0"}
	v2 := validInput()
	v2.BuildArgs = map[string]string{"VERSION": "2.0.0"}
	assert.NotEqual(t, computeBuildConfigHash(v1, &project), computeBuildConfigHash(v2, &project))
}

// TestBuildConfigHashCarriesSchemePrefix asserts the hash is emitted in the
// versioned form, so a hash recorded by an older plugin version is recognisable as
// such rather than being compared as if it had been produced by this scheme.
func TestBuildConfigHashCarriesSchemePrefix(t *testing.T) {
	project := validProject()
	h := computeBuildConfigHash(validInput(), &project)
	assert.Regexp(t, regexp.MustCompile(`^v3:[0-9a-f]{64}$`), h)
}

// TestBuildConfigHashBodyIsCanonical pins the hashed body: the generator version
// leads it, build args and the project's environment variables are ordered
// canonically, and the effective project fingerprint follows the resource's own
// inputs.
func TestBuildConfigHashBodyIsCanonical(t *testing.T) {
	in := validInput()
	in.BuildArgs = map[string]string{"ZED": "1", "ALPHA": "2"}

	project := validProject()
	project.Environment.EnvironmentVariables = []codebuildtypes.EnvironmentVariable{
		{Name: aws.String("ZONE"), Value: aws.String("b"), Type: codebuildtypes.EnvironmentVariableTypePlaintext},
		{Name: aws.String("AREA"), Value: aws.String("a"), Type: codebuildtypes.EnvironmentVariableTypePlaintext},
	}
	project.Cache = &codebuildtypes.ProjectCache{Type: codebuildtypes.CacheTypeLocal}

	want := "v=" + generatorVersion + "\n" +
		"dockerfile=" + in.Dockerfile + "\n" +
		"arg=ALPHA=2\n" +
		"arg=ZED=1\n" +
		"project=" + testBuildProject + "\n" +
		"project.environmentType=LINUX_CONTAINER\n" +
		"project.computeType=BUILD_GENERAL1_SMALL\n" +
		"project.image=aws/codebuild/standard:7.0\n" +
		"project.privilegedMode=true\n" +
		"project.environmentVariable=AREA=PLAINTEXT=a\n" +
		"project.environmentVariable=ZONE=PLAINTEXT=b\n" +
		"project.cacheType=LOCAL\n"
	assert.Equal(t, want, buildConfigHashBody(in, &project))
}

// TestBuildConfigHashSensitiveToProjectFingerprint asserts every fingerprint
// component of the referenced project changes the hash. The project's builder
// image, compute size and environment genuinely change what a build produces, so
// mutating the project must invalidate the built image rather than reporting no
// change.
func TestBuildConfigHashSensitiveToProjectFingerprint(t *testing.T) {
	base := validProject()
	h1 := computeBuildConfigHash(validInput(), &base)

	for name, mutate := range map[string]func(*codebuildtypes.Project){
		"projectName": func(pr *codebuildtypes.Project) { pr.Name = aws.String("other-project") },
		"environmentType": func(pr *codebuildtypes.Project) {
			pr.Environment.Type = codebuildtypes.EnvironmentTypeArmContainer
		},
		"computeType": func(pr *codebuildtypes.Project) {
			pr.Environment.ComputeType = codebuildtypes.ComputeTypeBuildGeneral1Large
		},
		"image": func(pr *codebuildtypes.Project) {
			pr.Environment.Image = aws.String("aws/codebuild/standard:8.0")
		},
		"privilegedMode": func(pr *codebuildtypes.Project) { pr.Environment.PrivilegedMode = aws.Bool(false) },
		"environmentVariables": func(pr *codebuildtypes.Project) {
			pr.Environment.EnvironmentVariables = []codebuildtypes.EnvironmentVariable{
				{Name: aws.String("REGISTRY"), Value: aws.String("a"), Type: codebuildtypes.EnvironmentVariableTypePlaintext},
			}
		},
		"cacheType": func(pr *codebuildtypes.Project) {
			pr.Cache = &codebuildtypes.ProjectCache{Type: codebuildtypes.CacheTypeLocal}
		},
	} {
		t.Run(name, func(t *testing.T) {
			project := validProject()
			mutate(&project)
			assert.NotEqual(t, h1, computeBuildConfigHash(validInput(), &project))
		})
	}

	// A component the build cannot observe does not change the hash.
	unrelated := validProject()
	unrelated.Description = aws.String("a description")
	unrelated.TimeoutInMinutes = aws.Int32(45)
	assert.Equal(t, h1, computeBuildConfigHash(validInput(), &unrelated))
}

// TestBuildConfigHashProjectEnvironmentVariablesCanonicallyOrdered asserts the
// project's environment variables are hashed in a canonical order, so the same
// project read back in a different order is not seen as a change.
func TestBuildConfigHashProjectEnvironmentVariablesCanonicallyOrdered(t *testing.T) {
	vars := []codebuildtypes.EnvironmentVariable{
		{Name: aws.String("ALPHA"), Value: aws.String("1"), Type: codebuildtypes.EnvironmentVariableTypePlaintext},
		{Name: aws.String("BETA"), Value: aws.String("2"), Type: codebuildtypes.EnvironmentVariableTypeParameterStore},
	}
	a := validProject()
	a.Environment.EnvironmentVariables = vars
	b := validProject()
	b.Environment.EnvironmentVariables = []codebuildtypes.EnvironmentVariable{vars[1], vars[0]}
	assert.Equal(t, computeBuildConfigHash(validInput(), &a), computeBuildConfigHash(validInput(), &b))

	// The variable's type is part of the fingerprint: the same name and value read
	// from Parameter Store is a different build input than a plaintext literal.
	c := validProject()
	c.Environment.EnvironmentVariables = []codebuildtypes.EnvironmentVariable{
		{Name: aws.String("ALPHA"), Value: aws.String("1"), Type: codebuildtypes.EnvironmentVariableTypeParameterStore},
		vars[1],
	}
	assert.NotEqual(t, computeBuildConfigHash(validInput(), &a), computeBuildConfigHash(validInput(), &c))
}

// TestIsLegacyBuildConfigHash asserts only a hash in the pre-scheme bare-hex form
// is treated as legacy: a versioned hash of any scheme, an empty value, and
// anything that is not a bare sha256 hex digest are not.
func TestIsLegacyBuildConfigHash(t *testing.T) {
	project := validProject()
	for value, want := range map[string]bool{
		"":                    false,
		legacyBuildConfigHash: true,
		computeBuildConfigHash(validInput(), &project): false,
		"v3:" + legacyBuildConfigHash:                  false,
		"v4:" + legacyBuildConfigHash:                  false,
		"not-a-hash":                                   false,
		legacyBuildConfigHash[:63]:                     false,
		legacyBuildConfigHash + "0":                    false,
	} {
		assert.Equal(t, want, isLegacyBuildConfigHash(value), "value %q", value)
	}
}

func TestImageURI(t *testing.T) {
	assert.Equal(t, "repo:tag", imageURI("repo", "tag"))
}
