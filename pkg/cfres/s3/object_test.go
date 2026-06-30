// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package s3

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// mockReadBack stubs the post-write Read (HeadObject + GetObjectTagging) that
// createWithClient/updateWithClient now perform to populate ResourceProperties.
func mockReadBack(client *mockS3ObjectClient, ctx context.Context) {
	client.On("HeadObject", ctx, mock.Anything).Return(&s3.HeadObjectOutput{}, nil).Maybe()
	client.On("GetObjectTagging", ctx, mock.Anything).Return(&s3.GetObjectTaggingOutput{}, nil).Maybe()
}

func TestBuildNativeID(t *testing.T) {
	id := buildNativeID("my-bucket", "path/to/key")
	assert.Equal(t, "my-bucket|path/to/key", id)
}

func TestParseNativeID(t *testing.T) {
	bucket, key, err := parseNativeID("my-bucket|path/to/key")
	assert.NoError(t, err)
	assert.Equal(t, "my-bucket", bucket)
	assert.Equal(t, "path/to/key", key)
}

func TestParseNativeID_KeyContainsPipe(t *testing.T) {
	bucket, key, err := parseNativeID("my-bucket|path/to/key|with|pipes")
	assert.NoError(t, err)
	assert.Equal(t, "my-bucket", bucket)
	assert.Equal(t, "path/to/key|with|pipes", key)
}

func TestParseNativeID_Invalid(t *testing.T) {
	_, _, err := parseNativeID("no-separator")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid NativeID format")
}

func TestResolveBody_InlineContent(t *testing.T) {
	props := map[string]any{
		"Content": "hello world",
	}
	reader, closer, err := resolveBodyWithCloser(props)
	require.NoError(t, err)
	require.NotNil(t, reader)
	defer closer()

	data, err := io.ReadAll(reader)
	require.NoError(t, err)
	assert.Equal(t, "hello world", string(data))
}

func TestResolveBody_Base64Content(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString([]byte("binary data"))
	props := map[string]any{
		"ContentBase64": encoded,
	}
	reader, closer, err := resolveBodyWithCloser(props)
	require.NoError(t, err)
	require.NotNil(t, reader)
	defer closer()

	data, err := io.ReadAll(reader)
	require.NoError(t, err)
	assert.Equal(t, "binary data", string(data))
}

func TestResolveBody_SourceURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("remote content"))
	}))
	defer server.Close()

	props := map[string]any{
		"Source": server.URL,
	}
	reader, closer, err := resolveBodyWithCloser(props)
	require.NoError(t, err)
	require.NotNil(t, reader)
	defer closer()

	data, err := io.ReadAll(reader)
	require.NoError(t, err)
	assert.Equal(t, "remote content", string(data))
}

func TestResolveBody_MutualExclusivity(t *testing.T) {
	props := map[string]any{
		"Content":       "hello",
		"ContentBase64": "aGVsbG8=",
	}
	reader, _, err := resolveBodyWithCloser(props)
	assert.Error(t, err)
	assert.Nil(t, reader)
	assert.Contains(t, err.Error(), "mutually exclusive")
}

func TestResolveBody_NoneProvided(t *testing.T) {
	props := map[string]any{}
	reader, _, err := resolveBodyWithCloser(props)
	assert.NoError(t, err)
	assert.Nil(t, reader)
}

func TestCreate_Success(t *testing.T) {
	ctx := context.Background()
	client := &mockS3ObjectClient{}

	props := map[string]any{
		"Bucket":      "my-bucket",
		"Key":         "path/to/file.txt",
		"Content":     "hello world",
		"ContentType": "text/plain",
	}
	propsBytes, _ := json.Marshal(props)

	client.On("PutObject", ctx, mock.MatchedBy(func(input *s3.PutObjectInput) bool {
		return *input.Bucket == "my-bucket" &&
			*input.Key == "path/to/file.txt" &&
			*input.ContentType == "text/plain"
	})).Return(&s3.PutObjectOutput{}, nil)

	o := &Object{}
	mockReadBack(client, ctx)
	result, err := o.createWithClient(ctx, client, &resource.CreateRequest{
		Properties: propsBytes,
	})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, resource.OperationStatusSuccess, result.ProgressResult.OperationStatus)
	assert.Equal(t, "my-bucket|path/to/file.txt", result.ProgressResult.NativeID)

	client.AssertExpectations(t)
}

func TestCreate_PopulatesResourcePropertiesFromReadBack(t *testing.T) {
	ctx := context.Background()
	client := &mockS3ObjectClient{}

	props := map[string]any{"Bucket": "my-bucket", "Key": "k.txt", "Content": "x"}
	propsBytes, _ := json.Marshal(props)

	client.On("PutObject", ctx, mock.Anything).Return(&s3.PutObjectOutput{}, nil)
	client.On("HeadObject", ctx, mock.Anything).Return(&s3.HeadObjectOutput{
		ContentType: aws.String("text/plain"),
	}, nil)
	client.On("GetObjectTagging", ctx, mock.Anything).Return(&s3.GetObjectTaggingOutput{
		TagSet: []s3types.Tag{{Key: aws.String("Name"), Value: aws.String("v")}},
	}, nil)

	o := &Object{}
	result, err := o.createWithClient(ctx, client, &resource.CreateRequest{Properties: propsBytes})
	require.NoError(t, err)
	require.NotNil(t, result.ProgressResult.ResourceProperties)
	// The persisted create state must carry the read-back Tags, otherwise the
	// agent stores a Tags-less version at create time.
	assert.Contains(t, string(result.ProgressResult.ResourceProperties), `"Tags"`)
	assert.Contains(t, string(result.ProgressResult.ResourceProperties), "Name")

	client.AssertExpectations(t)
}

func TestRead_Success(t *testing.T) {
	ctx := context.Background()
	client := &mockS3ObjectClient{}

	client.On("HeadObject", ctx, mock.MatchedBy(func(input *s3.HeadObjectInput) bool {
		return *input.Bucket == "my-bucket" && *input.Key == "path/to/file.txt"
	})).Return(&s3.HeadObjectOutput{
		ContentType:          aws.String("text/plain"),
		ContentLength:        aws.Int64(11),
		ETag:                 aws.String(`"abc123"`),
		StorageClass:         s3types.StorageClassStandard,
		ServerSideEncryption: s3types.ServerSideEncryptionAes256,
		Metadata:             map[string]string{"env": "test"},
	}, nil)

	client.On("GetObjectTagging", ctx, mock.MatchedBy(func(input *s3.GetObjectTaggingInput) bool {
		return *input.Bucket == "my-bucket" && *input.Key == "path/to/file.txt"
	})).Return(&s3.GetObjectTaggingOutput{
		TagSet: []s3types.Tag{
			{Key: aws.String("Name"), Value: aws.String("test-file")},
		},
	}, nil)

	o := &Object{}
	result, err := o.readWithClient(ctx, client, &resource.ReadRequest{
		NativeID: "my-bucket|path/to/file.txt",
	})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "", string(result.ErrorCode))

	var props map[string]any
	err = json.Unmarshal([]byte(result.Properties), &props)
	require.NoError(t, err)
	assert.Equal(t, "my-bucket", props["Bucket"])
	assert.Equal(t, "path/to/file.txt", props["Key"])
	assert.Equal(t, "text/plain", props["ContentType"])
	assert.Equal(t, `"abc123"`, props["ETag"])
	assert.Equal(t, "STANDARD", props["StorageClass"])
	assert.Equal(t, "AES256", props["ServerSideEncryption"])

	// Verify tags
	tags, ok := props["Tags"].([]any)
	require.True(t, ok)
	require.Len(t, tags, 1)
	tag := tags[0].(map[string]any)
	assert.Equal(t, "Name", tag["Key"])
	assert.Equal(t, "test-file", tag["Value"])

	// Verify metadata
	md, ok := props["Metadata"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "test", md["env"])

	client.AssertExpectations(t)
}

func TestRead_NotFound(t *testing.T) {
	ctx := context.Background()
	client := &mockS3ObjectClient{}

	var notFoundErr *s3types.NotFound
	_ = notFoundErr
	client.On("HeadObject", ctx, mock.Anything).Return(
		(*s3.HeadObjectOutput)(nil),
		&s3types.NotFound{Message: aws.String("not found")},
	)

	o := &Object{}
	result, err := o.readWithClient(ctx, client, &resource.ReadRequest{
		NativeID: "my-bucket|nonexistent.txt",
	})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, resource.OperationErrorCodeNotFound, result.ErrorCode)

	client.AssertExpectations(t)
}

func TestRead_TaggingError(t *testing.T) {
	ctx := context.Background()
	client := &mockS3ObjectClient{}

	client.On("HeadObject", ctx, mock.Anything).Return(&s3.HeadObjectOutput{
		ContentType: aws.String("text/plain"),
	}, nil)

	client.On("GetObjectTagging", ctx, mock.MatchedBy(func(input *s3.GetObjectTaggingInput) bool {
		return *input.Bucket == "my-bucket" && *input.Key == "path/to/file.txt"
	})).Return(
		(*s3.GetObjectTaggingOutput)(nil),
		errors.New("AccessDenied: not authorized to perform s3:GetObjectTagging"),
	)

	o := &Object{}
	result, err := o.readWithClient(ctx, client, &resource.ReadRequest{
		NativeID: "my-bucket|path/to/file.txt",
	})

	require.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "my-bucket/path/to/file.txt")

	client.AssertExpectations(t)
}

func TestRead_NoTags(t *testing.T) {
	ctx := context.Background()
	client := &mockS3ObjectClient{}

	client.On("HeadObject", ctx, mock.Anything).Return(&s3.HeadObjectOutput{
		ContentType: aws.String("text/plain"),
	}, nil)

	client.On("GetObjectTagging", ctx, mock.Anything).Return(&s3.GetObjectTaggingOutput{
		TagSet: []s3types.Tag{},
	}, nil)

	o := &Object{}
	result, err := o.readWithClient(ctx, client, &resource.ReadRequest{
		NativeID: "my-bucket|path/to/file.txt",
	})

	require.NoError(t, err)
	require.NotNil(t, result)

	var props map[string]any
	err = json.Unmarshal([]byte(result.Properties), &props)
	require.NoError(t, err)
	_, hasTags := props["Tags"]
	assert.False(t, hasTags, "Tags should be omitted for a genuinely untagged object")

	client.AssertExpectations(t)
}

func TestUpdate_Success(t *testing.T) {
	ctx := context.Background()
	client := &mockS3ObjectClient{}

	desiredProps := map[string]any{
		"Bucket":      "my-bucket",
		"Key":         "path/to/file.txt",
		"Content":     "updated content",
		"ContentType": "text/html",
	}
	desiredBytes, _ := json.Marshal(desiredProps)

	client.On("PutObject", ctx, mock.MatchedBy(func(input *s3.PutObjectInput) bool {
		return *input.Bucket == "my-bucket" &&
			*input.Key == "path/to/file.txt" &&
			*input.ContentType == "text/html"
	})).Return(&s3.PutObjectOutput{}, nil)

	o := &Object{}
	mockReadBack(client, ctx)
	result, err := o.updateWithClient(ctx, client, &resource.UpdateRequest{
		NativeID:          "my-bucket|path/to/file.txt",
		DesiredProperties: desiredBytes,
	})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, resource.OperationStatusSuccess, result.ProgressResult.OperationStatus)
	assert.Equal(t, "my-bucket|path/to/file.txt", result.ProgressResult.NativeID)

	client.AssertExpectations(t)
}

func TestCreate_SetsObjectLockRetainUntilDate(t *testing.T) {
	ctx := context.Background()
	client := &mockS3ObjectClient{}

	retainUntil := "2030-01-01T00:00:00Z"
	props := map[string]any{
		"Bucket":                    "my-bucket",
		"Key":                       "locked.txt",
		"Content":                   "data",
		"ObjectLockMode":            "GOVERNANCE",
		"ObjectLockRetainUntilDate": retainUntil,
	}
	propsBytes, _ := json.Marshal(props)

	expected, _ := time.Parse(time.RFC3339, retainUntil)
	client.On("PutObject", ctx, mock.MatchedBy(func(input *s3.PutObjectInput) bool {
		return input.ObjectLockMode == s3types.ObjectLockModeGovernance &&
			input.ObjectLockRetainUntilDate != nil &&
			input.ObjectLockRetainUntilDate.Equal(expected)
	})).Return(&s3.PutObjectOutput{}, nil)

	o := &Object{}
	mockReadBack(client, ctx)
	_, err := o.createWithClient(ctx, client, &resource.CreateRequest{Properties: propsBytes})
	require.NoError(t, err)
	client.AssertExpectations(t)
}

func TestUpdate_SetsObjectLockFields(t *testing.T) {
	ctx := context.Background()
	client := &mockS3ObjectClient{}

	retainUntil := "2030-06-15T12:00:00Z"
	desired := map[string]any{
		"Bucket":                    "my-bucket",
		"Key":                       "locked.txt",
		"Content":                   "updated",
		"ObjectLockMode":            "COMPLIANCE",
		"ObjectLockRetainUntilDate": retainUntil,
	}
	desiredBytes, _ := json.Marshal(desired)

	expected, _ := time.Parse(time.RFC3339, retainUntil)
	client.On("PutObject", ctx, mock.MatchedBy(func(input *s3.PutObjectInput) bool {
		return input.ObjectLockMode == s3types.ObjectLockModeCompliance &&
			input.ObjectLockRetainUntilDate != nil &&
			input.ObjectLockRetainUntilDate.Equal(expected)
	})).Return(&s3.PutObjectOutput{}, nil)

	o := &Object{}
	mockReadBack(client, ctx)
	_, err := o.updateWithClient(ctx, client, &resource.UpdateRequest{
		NativeID:          "my-bucket|locked.txt",
		DesiredProperties: desiredBytes,
	})
	require.NoError(t, err)
	client.AssertExpectations(t)
}

func TestCreate_InvalidObjectLockRetainUntilDate_Errors(t *testing.T) {
	ctx := context.Background()
	client := &mockS3ObjectClient{}

	props := map[string]any{
		"Bucket":                    "my-bucket",
		"Key":                       "locked.txt",
		"ObjectLockRetainUntilDate": "not-a-date",
	}
	propsBytes, _ := json.Marshal(props)

	o := &Object{}
	_, err := o.createWithClient(ctx, client, &resource.CreateRequest{Properties: propsBytes})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ObjectLockRetainUntilDate")
	client.AssertExpectations(t) // PutObject must not be called
}

func TestDelete_Success(t *testing.T) {
	ctx := context.Background()
	client := &mockS3ObjectClient{}

	client.On("DeleteObject", ctx, mock.MatchedBy(func(input *s3.DeleteObjectInput) bool {
		return *input.Bucket == "my-bucket" && *input.Key == "path/to/file.txt"
	})).Return(&s3.DeleteObjectOutput{}, nil)

	o := &Object{}
	nativeID := "my-bucket|path/to/file.txt"
	result, err := o.deleteWithClient(ctx, client, &resource.DeleteRequest{
		NativeID: nativeID,
	})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, resource.OperationStatusSuccess, result.ProgressResult.OperationStatus)
	assert.Equal(t, "my-bucket|path/to/file.txt", result.ProgressResult.NativeID)

	client.AssertExpectations(t)
}

func TestDelete_NotFound_IsIdempotent(t *testing.T) {
	ctx := context.Background()
	client := &mockS3ObjectClient{}

	// S3 DeleteObject returns success even for non-existent keys
	client.On("DeleteObject", ctx, mock.Anything).Return(&s3.DeleteObjectOutput{}, nil)

	o := &Object{}
	nativeID := "my-bucket|nonexistent.txt"
	result, err := o.deleteWithClient(ctx, client, &resource.DeleteRequest{
		NativeID: nativeID,
	})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, resource.OperationStatusSuccess, result.ProgressResult.OperationStatus)

	client.AssertExpectations(t)
}

func TestStatus_ReturnsSuccess(t *testing.T) {
	o := &Object{}
	result, err := o.Status(context.Background(), &resource.StatusRequest{
		NativeID: "my-bucket|path/to/file.txt",
	})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, resource.OperationStatusSuccess, result.ProgressResult.OperationStatus)
}

func TestList_Success(t *testing.T) {
	ctx := context.Background()
	client := &mockS3ObjectClient{}

	client.On("ListObjectsV2", ctx, mock.MatchedBy(func(input *s3.ListObjectsV2Input) bool {
		return *input.Bucket == "my-bucket" && input.ContinuationToken == nil
	})).Return(&s3.ListObjectsV2Output{
		Contents: []s3types.Object{
			{Key: aws.String("file1.txt")},
			{Key: aws.String("dir/file2.txt")},
		},
		IsTruncated:           aws.Bool(false),
		NextContinuationToken: nil,
	}, nil)

	o := &Object{}
	result, err := o.listWithClient(ctx, client, &resource.ListRequest{
		ResourceType: "AWS::S3::Object",
		PageSize:     100,
		AdditionalProperties: map[string]string{
			"BucketName": "my-bucket",
		},
	})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Len(t, result.NativeIDs, 2)
	assert.Equal(t, "my-bucket|file1.txt", result.NativeIDs[0])
	assert.Equal(t, "my-bucket|dir/file2.txt", result.NativeIDs[1])
	assert.Nil(t, result.NextPageToken)

	client.AssertExpectations(t)
}

func TestList_WithPagination(t *testing.T) {
	ctx := context.Background()
	client := &mockS3ObjectClient{}

	nextToken := "next-page"
	client.On("ListObjectsV2", ctx, mock.MatchedBy(func(input *s3.ListObjectsV2Input) bool {
		return *input.Bucket == "my-bucket" && input.ContinuationToken == nil
	})).Return(&s3.ListObjectsV2Output{
		Contents: []s3types.Object{
			{Key: aws.String("file1.txt")},
		},
		IsTruncated:           aws.Bool(true),
		NextContinuationToken: &nextToken,
	}, nil)

	o := &Object{}
	result, err := o.listWithClient(ctx, client, &resource.ListRequest{
		ResourceType: "AWS::S3::Object",
		PageSize:     1,
		AdditionalProperties: map[string]string{
			"BucketName": "my-bucket",
		},
	})

	require.NoError(t, err)
	require.Len(t, result.NativeIDs, 1)
	require.NotNil(t, result.NextPageToken)
	assert.Equal(t, "next-page", *result.NextPageToken)

	client.AssertExpectations(t)
}

func TestList_MissingBucketName(t *testing.T) {
	o := &Object{}
	result, err := o.listWithClient(context.Background(), nil, &resource.ListRequest{
		ResourceType: "AWS::S3::Object",
		PageSize:     100,
	})

	assert.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "BucketName")
}

func TestResolveBody_HttpSource_HeadersAndExtract(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Write(zipWith(t, map[string]string{"carys-cars.jar": "JAR"})) // reuse Task 4 helper
	}))
	defer srv.Close()
	// Override guardURLFn so the http:// test server is not rejected by the https-only check.
	origGuard := guardURLFn
	guardURLFn = func(raw string) error { return nil }
	defer func() { guardURLFn = origGuard }()
	// Override dialIPGuard so the loopback httptest server is not blocked at dial time.
	origDial := dialIPGuard
	dialIPGuard = func(ip net.IP) error { return nil }
	defer func() { dialIPGuard = origDial }()
	props := map[string]any{"Source": map[string]any{
		"Url":     srv.URL,
		"Headers": map[string]any{"Authorization": "Bearer tok"},
		"Extract": "carys-cars.jar",
	}}
	reader, closer, err := resolveBodyWithCloser(props)
	if err != nil {
		t.Fatal(err)
	}
	defer closer()
	if gotAuth != "Bearer tok" {
		t.Fatalf("auth header not sent: %q", gotAuth)
	}
	b, _ := io.ReadAll(reader)
	if string(b) != "JAR" {
		t.Fatalf("got %q", b)
	}
}

func TestResolveBody_HttpSource_RejectsHTTP(t *testing.T) {
	props := map[string]any{"Source": map[string]any{"Url": "http://example.com/x"}}
	if _, _, err := resolveBodyWithCloser(props); err == nil {
		t.Fatal("expected http:// rejection")
	}
}

func TestResolveBody_HttpSource_ErrorRedactsHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	// Override guardURLFn so the http:// test server is not rejected by the https-only check.
	origGuard := guardURLFn
	guardURLFn = func(raw string) error { return nil }
	defer func() { guardURLFn = origGuard }()
	// Override dialIPGuard so the loopback httptest server is not blocked at dial time.
	origDial := dialIPGuard
	dialIPGuard = func(ip net.IP) error { return nil }
	defer func() { dialIPGuard = origDial }()
	props := map[string]any{"Source": map[string]any{
		"Url": srv.URL, "Headers": map[string]any{"Authorization": "Bearer SUPERSECRET"},
	}}
	_, _, err := resolveBodyWithCloser(props)
	if err == nil {
		t.Fatal("expected 403 error")
	}
	if strings.Contains(err.Error(), "SUPERSECRET") {
		t.Fatal("error leaked the auth token")
	}
}

// TestResolveBody_MutualExclusivity_HttpSource asserts that providing both a
// Content string and a structured HttpSource (Source map) is rejected. The
// count>1 guard in resolveBodyWithCloser must treat a Source map as one source,
// so that mixing it with Content triggers the mutual-exclusion error.
func TestResolveBody_MutualExclusivity_HttpSource(t *testing.T) {
	props := map[string]any{
		"Content": "x",
		"Source":  map[string]any{"Url": "https://example.com/x"},
	}
	if _, _, err := resolveBodyWithCloser(props); err == nil {
		t.Fatal("expected mutual-exclusion error with Content + Source")
	}
}

func objectPKLPath() string {
	_, filename, _, _ := runtime.Caller(0)
	// pkg/cfres/s3/ -> (repo root)/schema/pkl/s3/object.pkl
	root := filepath.Join(filepath.Dir(filename), "..", "..", "..", "schema", "pkl", "s3")
	return filepath.Join(root, "object.pkl")
}

// TestSchema_SourceIsWriteOnly asserts that the `source` field in object.pkl
// carries @aws.FieldHint { writeOnly = true } and does NOT carry
// requiredOnUpdate = true. The writeOnly annotation tells the agent the field
// is never returned by Read, preventing phantom-redeploy loops on every sync.
// requiredOnUpdate must stay absent: S3 does not REDACT the source on read
// (it simply never returns it), so the agent must not treat an absent source
// field after a Read as a reason to trigger an update.
//
// This is a textual test against the PKL source — the same approach used for
// ECS attachesTo annotations — because running ExtractSchema requires a live
// pkl CLI subprocess and network access, making it unsuitable for make test-unit.
func TestSchema_SourceIsWriteOnly(t *testing.T) {
	content, err := os.ReadFile(objectPKLPath())
	if err != nil {
		t.Fatalf("could not read object.pkl: %v", err)
	}
	src := string(content)

	// Find the `source` field declaration and its preceding annotation block.
	lines := strings.Split(src, "\n")
	for i, line := range lines {
		if !strings.Contains(line, "source:") {
			continue
		}
		// Collect annotation lines immediately preceding this field declaration.
		// They form a block starting with @aws.FieldHint.
		annotationBlock := ""
		for j := i - 1; j >= 0; j-- {
			trimmed := strings.TrimSpace(lines[j])
			if trimmed == "" {
				break
			}
			annotationBlock = trimmed + "\n" + annotationBlock
		}
		if !strings.Contains(annotationBlock, "writeOnly = true") {
			t.Errorf("source field: expected @aws.FieldHint { writeOnly = true } before source: declaration, got annotation block:\n%s", annotationBlock)
		}
		if strings.Contains(annotationBlock, "requiredOnUpdate") {
			t.Errorf("source field: requiredOnUpdate must not be set on source — it causes phantom-redeploy loops because S3 never returns the source on Read; annotation block:\n%s", annotationBlock)
		}
		return
	}
	t.Fatal("source: field declaration not found in object.pkl")
}
