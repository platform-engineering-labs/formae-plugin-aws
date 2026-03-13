// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package s3

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

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
	client := &mockS3Client{}

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
	result, err := o.createWithClient(ctx, client, &resource.CreateRequest{
		Properties: propsBytes,
	})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, resource.OperationStatusSuccess, result.ProgressResult.OperationStatus)
	assert.Equal(t, "my-bucket|path/to/file.txt", result.ProgressResult.NativeID)

	client.AssertExpectations(t)
}

func TestRead_Success(t *testing.T) {
	ctx := context.Background()
	client := &mockS3Client{}

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
	client := &mockS3Client{}

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

func TestUpdate_Success(t *testing.T) {
	ctx := context.Background()
	client := &mockS3Client{}

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

func TestDelete_Success(t *testing.T) {
	ctx := context.Background()
	client := &mockS3Client{}

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
	client := &mockS3Client{}

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
	client := &mockS3Client{}

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
	client := &mockS3Client{}

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
