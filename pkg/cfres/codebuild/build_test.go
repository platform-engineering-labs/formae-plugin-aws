// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package codebuild

import (
	"regexp"
	"strconv"
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
		"bad pin":            func(i *imageBuildInput) { i.AdditionalTags = []string{"bad pin!"} },
		"duplicate pin":      func(i *imageBuildInput) { i.AdditionalTags = []string{"release-1", "release-1"} },
		"pin equal to tag":   func(i *imageBuildInput) { i.AdditionalTags = []string{validInput().ImageTag} },
		"too many pins":      func(i *imageBuildInput) { i.AdditionalTags = manyPins(maxAdditionalTags + 1) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			in := validInput()
			mutate(&in)
			assert.Error(t, validateInput(in))
		})
	}
}

// manyPins renders n distinct, well-formed pins.
func manyPins(n int) []string {
	pins := make([]string, 0, n)
	for i := range n {
		pins = append(pins, "release-"+strconv.Itoa(i))
	}
	return pins
}

func TestValidateInputAcceptsPins(t *testing.T) {
	in := validInput()
	in.AdditionalTags = manyPins(maxAdditionalTags)
	require.NoError(t, validateInput(in))

	// An absent and an empty listing are both "no pins".
	in.AdditionalTags = nil
	require.NoError(t, validateInput(in))
	in.AdditionalTags = []string{}
	require.NoError(t, validateInput(in))
}

// TestImageTagPatternExcludesEncodingSeparators asserts the tag pattern admits
// neither separator a pin is joined on, so a pin can never split its own field in
// the RequestID or the NativeID.
func TestImageTagPatternExcludesEncodingSeparators(t *testing.T) {
	for _, bad := range []string{"a,b", "a|b"} {
		assert.False(t, imageTagPattern.MatchString(bad), "expected %q to be rejected", bad)
	}
}

func TestNewPins(t *testing.T) {
	prior := imageBuildInput{AdditionalTags: []string{"release-1", "release-2"}}

	// Declared-minus-previously-declared, in declared order.
	got := newPins(prior, imageBuildInput{AdditionalTags: []string{"release-2", "release-3", "release-1", "release-4"}})
	assert.Equal(t, []string{"release-3", "release-4"}, got)

	// Nothing new when the listing is carried over unchanged.
	assert.Empty(t, newPins(prior, imageBuildInput{AdditionalTags: []string{"release-1", "release-2"}}))

	// Dropping a pin declares nothing new — the plugin never deletes a pin.
	assert.Empty(t, newPins(prior, imageBuildInput{AdditionalTags: []string{"release-1"}}))

	// On a create there is no prior, so every declared pin is new.
	assert.Equal(t, []string{"release-1"}, newPins(imageBuildInput{}, imageBuildInput{AdditionalTags: []string{"release-1"}}))

	// A pin dropped and re-added in a later apply counts as new again.
	dropped := imageBuildInput{AdditionalTags: []string{"release-1"}}
	assert.Equal(t, []string{"release-2"}, newPins(dropped, imageBuildInput{AdditionalTags: []string{"release-1", "release-2"}}))

	assert.Empty(t, newPins(imageBuildInput{}, imageBuildInput{}))
}

// TestBuildConfigHashIgnoresAdditionalTags asserts a pin is not a build-affecting
// input: adding one must place a tag on the manifest that already exists, never
// force a rebuild that would produce a different one.
func TestBuildConfigHashIgnoresAdditionalTags(t *testing.T) {
	project := &codebuildtypes.Project{Name: aws.String(testBuildProject)}
	base := validInput()
	pinned := validInput()
	pinned.AdditionalTags = []string{"release-1", "release-2"}
	assert.Equal(t, computeBuildConfigHash(base, project), computeBuildConfigHash(pinned, project))
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
	project.Cache = &codebuildtypes.ProjectCache{
		Type: codebuildtypes.CacheTypeLocal,
		Modes: []codebuildtypes.CacheMode{
			codebuildtypes.CacheModeLocalSourceCache,
			codebuildtypes.CacheModeLocalDockerLayerCache,
		},
	}

	// Every value is quoted, so a value containing a newline or an '=' cannot render
	// as extra lines and make two different configurations hash alike.
	want := "v=\"" + generatorVersion + "\"\n" +
		"dockerfile=" + strconv.Quote(in.Dockerfile) + "\n" +
		"arg=\"ALPHA\"=\"2\"\n" +
		"arg=\"ZED\"=\"1\"\n" +
		"project=\"" + testBuildProject + "\"\n" +
		"project.environmentType=\"LINUX_CONTAINER\"\n" +
		"project.computeType=\"BUILD_GENERAL1_SMALL\"\n" +
		"project.image=\"aws/codebuild/standard:7.0\"\n" +
		"project.privilegedMode=true\n" +
		"project.environmentVariable=\"AREA\"=\"PLAINTEXT\"=\"a\"\n" +
		"project.environmentVariable=\"ZONE\"=\"PLAINTEXT\"=\"b\"\n" +
		"project.cacheType=\"LOCAL\"\n" +
		"project.cacheMode=\"LOCAL_DOCKER_LAYER_CACHE\"\n" +
		"project.cacheMode=\"LOCAL_SOURCE_CACHE\"\n" +
		"project.cacheLocation=\"\"\n"
	assert.Equal(t, want, buildConfigHashBody(in, &project))
}

// TestBuildConfigHashEscapesValues asserts values are escaped rather than
// concatenated raw: a build-arg value containing a newline must not be able to
// render as an additional line and collide with a genuinely different set of args.
func TestBuildConfigHashEscapesValues(t *testing.T) {
	project := validProject()

	forged := validInput()
	forged.BuildArgs = map[string]string{"A": "1\narg=B=2"}
	genuine := validInput()
	genuine.BuildArgs = map[string]string{"A": "1", "B": "2"}
	assert.NotEqual(t, computeBuildConfigHash(forged, &project), computeBuildConfigHash(genuine, &project))

	// The same for a project environment variable, whose value the plugin does not
	// author at all.
	a := validProject()
	a.Environment.EnvironmentVariables = []codebuildtypes.EnvironmentVariable{
		{Name: aws.String("A"), Value: aws.String("1\nproject.environmentVariable=\"B\"=\"PLAINTEXT\"=\"2\""), Type: codebuildtypes.EnvironmentVariableTypePlaintext},
	}
	b := validProject()
	b.Environment.EnvironmentVariables = []codebuildtypes.EnvironmentVariable{
		{Name: aws.String("A"), Value: aws.String("1"), Type: codebuildtypes.EnvironmentVariableTypePlaintext},
		{Name: aws.String("B"), Value: aws.String("2"), Type: codebuildtypes.EnvironmentVariableTypePlaintext},
	}
	assert.NotEqual(t, computeBuildConfigHash(validInput(), &a), computeBuildConfigHash(validInput(), &b))
}

// TestBuildConfigHashSensitiveToCacheModesAndLocation asserts the cache fingerprint
// covers more than the cache type. Docker layer caching is what lets a build reuse
// previously built layers, and it is selected by the cache mode with the type
// unchanged at LOCAL — so a mode flip has to invalidate the built image.
func TestBuildConfigHashSensitiveToCacheModesAndLocation(t *testing.T) {
	sourceCache := validProject()
	sourceCache.Cache = &codebuildtypes.ProjectCache{
		Type:  codebuildtypes.CacheTypeLocal,
		Modes: []codebuildtypes.CacheMode{codebuildtypes.CacheModeLocalSourceCache},
	}
	layerCache := validProject()
	layerCache.Cache = &codebuildtypes.ProjectCache{
		Type:  codebuildtypes.CacheTypeLocal,
		Modes: []codebuildtypes.CacheMode{codebuildtypes.CacheModeLocalDockerLayerCache},
	}
	assert.NotEqual(t,
		computeBuildConfigHash(validInput(), &sourceCache),
		computeBuildConfigHash(validInput(), &layerCache))

	// Mode ordering is not significant.
	both := []codebuildtypes.CacheMode{codebuildtypes.CacheModeLocalSourceCache, codebuildtypes.CacheModeLocalDockerLayerCache}
	forward := validProject()
	forward.Cache = &codebuildtypes.ProjectCache{Type: codebuildtypes.CacheTypeLocal, Modes: both}
	reversed := validProject()
	reversed.Cache = &codebuildtypes.ProjectCache{
		Type:  codebuildtypes.CacheTypeLocal,
		Modes: []codebuildtypes.CacheMode{both[1], both[0]},
	}
	assert.Equal(t,
		computeBuildConfigHash(validInput(), &forward),
		computeBuildConfigHash(validInput(), &reversed))

	// An S3 cache reads and writes a specific bucket/prefix, so the location counts.
	here := validProject()
	here.Cache = &codebuildtypes.ProjectCache{Type: codebuildtypes.CacheTypeS3, Location: aws.String("bucket/a")}
	there := validProject()
	there.Cache = &codebuildtypes.ProjectCache{Type: codebuildtypes.CacheTypeS3, Location: aws.String("bucket/b")}
	assert.NotEqual(t,
		computeBuildConfigHash(validInput(), &here),
		computeBuildConfigHash(validInput(), &there))
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
