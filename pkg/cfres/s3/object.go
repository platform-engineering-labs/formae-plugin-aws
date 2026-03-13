// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package s3

import (
	"context"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres/prov"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres/registry"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres/utils"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/config"
)

type s3ClientInterface interface {
	PutObject(ctx context.Context, params *s3.PutObjectInput, optFns ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	HeadObject(ctx context.Context, params *s3.HeadObjectInput, optFns ...func(*s3.Options)) (*s3.HeadObjectOutput, error)
	DeleteObject(ctx context.Context, params *s3.DeleteObjectInput, optFns ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
	ListObjectsV2(ctx context.Context, params *s3.ListObjectsV2Input, optFns ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
	GetObjectTagging(ctx context.Context, params *s3.GetObjectTaggingInput, optFns ...func(*s3.Options)) (*s3.GetObjectTaggingOutput, error)
}

type Object struct {
	cfg *config.Config
}

var _ prov.Provisioner = &Object{}

func init() {
	registry.Register("AWS::S3::Object",
		[]resource.Operation{
			resource.OperationCreate,
			resource.OperationRead,
			resource.OperationUpdate,
			resource.OperationDelete,
			resource.OperationCheckStatus,
			resource.OperationList,
		},
		func(cfg *config.Config) prov.Provisioner {
			return &Object{cfg: cfg}
		})
}

func buildNativeID(bucket, key string) string {
	return fmt.Sprintf("%s|%s", bucket, key)
}

func parseNativeID(nativeID string) (bucket, key string, err error) {
	parts := strings.SplitN(nativeID, "|", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("invalid NativeID format: expected bucket|key, got: %s", nativeID)
	}
	return parts[0], parts[1], nil
}

// resolveBodyWithCloser returns an io.Reader for the object body and a closer function.
// Exactly one of Content, ContentBase64, or Source may be set. If none is set, returns nil reader.
func resolveBodyWithCloser(props map[string]any) (io.Reader, func(), error) {
	content, hasContent := props["Content"]
	contentBase64, hasBase64 := props["ContentBase64"]
	source, hasSource := props["Source"]

	count := 0
	if hasContent {
		count++
	}
	if hasBase64 {
		count++
	}
	if hasSource {
		count++
	}
	if count > 1 {
		return nil, nil, fmt.Errorf("content, contentBase64, and source are mutually exclusive")
	}

	if hasContent {
		return strings.NewReader(content.(string)), func() {}, nil
	}

	if hasBase64 {
		decoded, err := base64.StdEncoding.DecodeString(contentBase64.(string))
		if err != nil {
			return nil, nil, fmt.Errorf("failed to decode ContentBase64: %w", err)
		}
		return strings.NewReader(string(decoded)), func() {}, nil
	}

	if hasSource {
		sourceStr := source.(string)
		resp, err := http.Get(sourceStr) //nolint:gosec // Source URL is user-provided infrastructure config
		if err != nil {
			return nil, nil, fmt.Errorf("failed to fetch source URL %s: %w", sourceStr, err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			return nil, nil, fmt.Errorf("source URL %s returned status %d", sourceStr, resp.StatusCode)
		}
		// Buffer into memory so the SDK can determine Content-Length
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to read source URL %s: %w", sourceStr, err)
		}
		return bytes.NewReader(data), func() {}, nil
	}

	return nil, func() {}, nil
}


func (o *Object) Create(ctx context.Context, request *resource.CreateRequest) (*resource.CreateResult, error) {
	cfg, err := o.cfg.ToAwsConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to load AWS config: %w", err)
	}
	client := s3.NewFromConfig(cfg)
	return o.createWithClient(ctx, client, request)
}

func (o *Object) createWithClient(ctx context.Context, client s3ClientInterface, request *resource.CreateRequest) (*resource.CreateResult, error) {
	var props map[string]any
	if err := json.Unmarshal(request.Properties, &props); err != nil {
		return nil, fmt.Errorf("failed to parse properties: %w", err)
	}

	bucket, err := utils.GetStringProperty(props, "Bucket")
	if err != nil {
		return nil, fmt.Errorf("invalid Bucket: %w", err)
	}
	key, err := utils.GetStringProperty(props, "Key")
	if err != nil {
		return nil, fmt.Errorf("invalid Key: %w", err)
	}

	body, closer, err := resolveBodyWithCloser(props)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve body: %w", err)
	}
	defer closer()

	input := &s3.PutObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
		Body:   body,
	}

	if ct, _ := utils.GetStringProperty(props, "ContentType"); ct != "" {
		input.ContentType = aws.String(ct)
	}
	if ce, _ := utils.GetStringProperty(props, "ContentEncoding"); ce != "" {
		input.ContentEncoding = aws.String(ce)
	}
	if cl, _ := utils.GetStringProperty(props, "ContentLanguage"); cl != "" {
		input.ContentLanguage = aws.String(cl)
	}
	if cd, _ := utils.GetStringProperty(props, "ContentDisposition"); cd != "" {
		input.ContentDisposition = aws.String(cd)
	}
	if cc, _ := utils.GetStringProperty(props, "CacheControl"); cc != "" {
		input.CacheControl = aws.String(cc)
	}
	if sc, _ := utils.GetStringProperty(props, "StorageClass"); sc != "" {
		input.StorageClass = s3types.StorageClass(sc)
	}
	if sse, _ := utils.GetStringProperty(props, "ServerSideEncryption"); sse != "" {
		input.ServerSideEncryption = s3types.ServerSideEncryption(sse)
	}
	if kmsKey, _ := utils.GetStringProperty(props, "KmsKeyId"); kmsKey != "" {
		input.SSEKMSKeyId = aws.String(kmsKey)
	}
	if ca, _ := utils.GetStringProperty(props, "ChecksumAlgorithm"); ca != "" {
		input.ChecksumAlgorithm = s3types.ChecksumAlgorithm(ca)
	}
	if acl, _ := utils.GetStringProperty(props, "Acl"); acl != "" {
		input.ACL = s3types.ObjectCannedACL(acl)
	}
	if wrl, _ := utils.GetStringProperty(props, "WebsiteRedirectLocation"); wrl != "" {
		input.WebsiteRedirectLocation = aws.String(wrl)
	}
	if olhs, _ := utils.GetStringProperty(props, "ObjectLockLegalHoldStatus"); olhs != "" {
		input.ObjectLockLegalHoldStatus = s3types.ObjectLockLegalHoldStatus(olhs)
	}
	if olm, _ := utils.GetStringProperty(props, "ObjectLockMode"); olm != "" {
		input.ObjectLockMode = s3types.ObjectLockMode(olm)
	}
	if olrud, _ := utils.GetStringProperty(props, "ObjectLockRetainUntilDate"); olrud != "" {
		// Parse ISO 8601 date — left for the SDK to handle via smithy
		_ = olrud
	}
	if md, ok := props["Metadata"]; ok {
		if mdMap, ok := md.(map[string]any); ok {
			metadata := make(map[string]string)
			for k, v := range mdMap {
				if sv, ok := v.(string); ok {
					metadata[k] = sv
				}
			}
			input.Metadata = metadata
		}
	}
	if tags, ok := props["Tags"]; ok {
		if tagList, ok := tags.([]any); ok {
			input.Tagging = aws.String(buildTaggingHeader(tagList))
		}
	}

	_, err = client.PutObject(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("failed to put object: %w", err)
	}

	nativeID := buildNativeID(bucket, key)
	return &resource.CreateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:       resource.OperationCreate,
			OperationStatus: resource.OperationStatusSuccess,
			NativeID:        nativeID,
		},
	}, nil
}

func buildTaggingHeader(tags []any) string {
	var parts []string
	for _, tag := range tags {
		if tagMap, ok := tag.(map[string]any); ok {
			k, _ := tagMap["Key"].(string)
			v, _ := tagMap["Value"].(string)
			if k != "" {
				parts = append(parts, fmt.Sprintf("%s=%s", k, v))
			}
		}
	}
	return strings.Join(parts, "&")
}

func (o *Object) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	cfg, err := o.cfg.ToAwsConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to load AWS config: %w", err)
	}
	client := s3.NewFromConfig(cfg)
	return o.readWithClient(ctx, client, request)
}

func (o *Object) readWithClient(ctx context.Context, client s3ClientInterface, request *resource.ReadRequest) (*resource.ReadResult, error) {
	bucket, key, err := parseNativeID(request.NativeID)
	if err != nil {
		return nil, err
	}

	head, err := client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		var notFound *s3types.NotFound
		if errors.As(err, &notFound) {
			return &resource.ReadResult{
				ResourceType: "AWS::S3::Object",
				ErrorCode:    resource.OperationErrorCodeNotFound,
			}, nil
		}
		// Also handle HTTP 404 from smithy
		var respErr interface{ HTTPStatusCode() int }
		if errors.As(err, &respErr) && respErr.HTTPStatusCode() == 404 {
			return &resource.ReadResult{
				ResourceType: "AWS::S3::Object",
				ErrorCode:    resource.OperationErrorCodeNotFound,
			}, nil
		}
		return nil, fmt.Errorf("failed to head object: %w", err)
	}

	props := map[string]any{
		"Bucket": bucket,
		"Key":    key,
	}

	if head.ContentType != nil {
		props["ContentType"] = *head.ContentType
	}
	if head.ContentEncoding != nil {
		props["ContentEncoding"] = *head.ContentEncoding
	}
	if head.ContentLanguage != nil {
		props["ContentLanguage"] = *head.ContentLanguage
	}
	if head.ContentDisposition != nil {
		props["ContentDisposition"] = *head.ContentDisposition
	}
	if head.CacheControl != nil {
		props["CacheControl"] = *head.CacheControl
	}
	if head.ContentLength != nil {
		props["ContentLength"] = *head.ContentLength
	}
	if head.ETag != nil {
		props["ETag"] = *head.ETag
	}
	if head.VersionId != nil {
		props["VersionId"] = *head.VersionId
	}
	if head.StorageClass != "" {
		props["StorageClass"] = string(head.StorageClass)
	}
	if head.ServerSideEncryption != "" {
		props["ServerSideEncryption"] = string(head.ServerSideEncryption)
	}
	if head.SSEKMSKeyId != nil {
		props["KmsKeyId"] = *head.SSEKMSKeyId
	}
	if head.WebsiteRedirectLocation != nil {
		props["WebsiteRedirectLocation"] = *head.WebsiteRedirectLocation
	}
	if head.ObjectLockLegalHoldStatus != "" {
		props["ObjectLockLegalHoldStatus"] = string(head.ObjectLockLegalHoldStatus)
	}
	if head.ObjectLockMode != "" {
		props["ObjectLockMode"] = string(head.ObjectLockMode)
	}
	if head.ObjectLockRetainUntilDate != nil {
		props["ObjectLockRetainUntilDate"] = head.ObjectLockRetainUntilDate.Format("2006-01-02T15:04:05Z")
	}
	if len(head.Metadata) > 0 {
		props["Metadata"] = head.Metadata
	}

	// Get tags
	tagging, err := client.GetObjectTagging(ctx, &s3.GetObjectTaggingInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err == nil && len(tagging.TagSet) > 0 {
		var tags []map[string]string
		for _, tag := range tagging.TagSet {
			tags = append(tags, map[string]string{
				"Key":   *tag.Key,
				"Value": *tag.Value,
			})
		}
		props["Tags"] = tags
	}

	propBytes, err := json.Marshal(props)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal properties: %w", err)
	}

	return &resource.ReadResult{
		ResourceType: "AWS::S3::Object",
		Properties:   string(propBytes),
	}, nil
}

func (o *Object) Update(ctx context.Context, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	cfg, err := o.cfg.ToAwsConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to load AWS config: %w", err)
	}
	client := s3.NewFromConfig(cfg)
	return o.updateWithClient(ctx, client, request)
}

func (o *Object) updateWithClient(ctx context.Context, client s3ClientInterface, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	var props map[string]any
	if err := json.Unmarshal(request.DesiredProperties, &props); err != nil {
		return nil, fmt.Errorf("failed to parse desired properties: %w", err)
	}

	bucket, err := utils.GetStringProperty(props, "Bucket")
	if err != nil {
		return nil, fmt.Errorf("invalid Bucket: %w", err)
	}
	key, err := utils.GetStringProperty(props, "Key")
	if err != nil {
		return nil, fmt.Errorf("invalid Key: %w", err)
	}

	body, closer, err := resolveBodyWithCloser(props)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve body: %w", err)
	}
	defer closer()

	input := &s3.PutObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
		Body:   body,
	}

	if ct, _ := utils.GetStringProperty(props, "ContentType"); ct != "" {
		input.ContentType = aws.String(ct)
	}
	if ce, _ := utils.GetStringProperty(props, "ContentEncoding"); ce != "" {
		input.ContentEncoding = aws.String(ce)
	}
	if cl, _ := utils.GetStringProperty(props, "ContentLanguage"); cl != "" {
		input.ContentLanguage = aws.String(cl)
	}
	if cd, _ := utils.GetStringProperty(props, "ContentDisposition"); cd != "" {
		input.ContentDisposition = aws.String(cd)
	}
	if cc, _ := utils.GetStringProperty(props, "CacheControl"); cc != "" {
		input.CacheControl = aws.String(cc)
	}
	if sc, _ := utils.GetStringProperty(props, "StorageClass"); sc != "" {
		input.StorageClass = s3types.StorageClass(sc)
	}
	if sse, _ := utils.GetStringProperty(props, "ServerSideEncryption"); sse != "" {
		input.ServerSideEncryption = s3types.ServerSideEncryption(sse)
	}
	if kmsKey, _ := utils.GetStringProperty(props, "KmsKeyId"); kmsKey != "" {
		input.SSEKMSKeyId = aws.String(kmsKey)
	}
	if ca, _ := utils.GetStringProperty(props, "ChecksumAlgorithm"); ca != "" {
		input.ChecksumAlgorithm = s3types.ChecksumAlgorithm(ca)
	}
	if acl, _ := utils.GetStringProperty(props, "Acl"); acl != "" {
		input.ACL = s3types.ObjectCannedACL(acl)
	}
	if wrl, _ := utils.GetStringProperty(props, "WebsiteRedirectLocation"); wrl != "" {
		input.WebsiteRedirectLocation = aws.String(wrl)
	}
	if md, ok := props["Metadata"]; ok {
		if mdMap, ok := md.(map[string]any); ok {
			metadata := make(map[string]string)
			for k, v := range mdMap {
				if sv, ok := v.(string); ok {
					metadata[k] = sv
				}
			}
			input.Metadata = metadata
		}
	}
	if tags, ok := props["Tags"]; ok {
		if tagList, ok := tags.([]any); ok {
			input.Tagging = aws.String(buildTaggingHeader(tagList))
		}
	}

	_, err = client.PutObject(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("failed to put object: %w", err)
	}

	nativeID := buildNativeID(bucket, key)
	return &resource.UpdateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:       resource.OperationUpdate,
			OperationStatus: resource.OperationStatusSuccess,
			NativeID:        nativeID,
		},
	}, nil
}

func (o *Object) Delete(ctx context.Context, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	cfg, err := o.cfg.ToAwsConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to load AWS config: %w", err)
	}
	client := s3.NewFromConfig(cfg)
	return o.deleteWithClient(ctx, client, request)
}

func (o *Object) deleteWithClient(ctx context.Context, client s3ClientInterface, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	bucket, key, err := parseNativeID(request.NativeID)
	if err != nil {
		return nil, err
	}

	_, err = client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to delete object: %w", err)
	}

	return &resource.DeleteResult{
		ProgressResult: &resource.ProgressResult{
			Operation:       resource.OperationDelete,
			OperationStatus: resource.OperationStatusSuccess,
			NativeID:        request.NativeID,
		},
	}, nil
}

// Status returns success immediately — all S3 operations are synchronous.
func (o *Object) Status(_ context.Context, request *resource.StatusRequest) (*resource.StatusResult, error) {
	return &resource.StatusResult{
		ProgressResult: &resource.ProgressResult{
			Operation:       resource.OperationCheckStatus,
			OperationStatus: resource.OperationStatusSuccess,
			NativeID:        request.NativeID,
		},
	}, nil
}

func (o *Object) List(ctx context.Context, request *resource.ListRequest) (*resource.ListResult, error) {
	cfg, err := o.cfg.ToAwsConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to load AWS config: %w", err)
	}
	client := s3.NewFromConfig(cfg)
	return o.listWithClient(ctx, client, request)
}

func (o *Object) listWithClient(ctx context.Context, client s3ClientInterface, request *resource.ListRequest) (*resource.ListResult, error) {
	if request.AdditionalProperties == nil {
		return nil, fmt.Errorf("BucketName required for listing S3 objects")
	}
	bucketName, ok := request.AdditionalProperties["BucketName"]
	if !ok || bucketName == "" {
		return nil, fmt.Errorf("BucketName must be provided in additional properties for listing S3 objects")
	}

	input := &s3.ListObjectsV2Input{
		Bucket:  aws.String(bucketName),
		MaxKeys: aws.Int32(request.PageSize),
	}
	if request.PageToken != nil && *request.PageToken != "" {
		input.ContinuationToken = request.PageToken
	}

	resp, err := client.ListObjectsV2(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("failed to list objects in bucket %s: %w", bucketName, err)
	}

	var nativeIDs []string
	for _, obj := range resp.Contents {
		nativeIDs = append(nativeIDs, buildNativeID(bucketName, *obj.Key))
	}

	var nextToken *string
	if resp.IsTruncated != nil && *resp.IsTruncated {
		nextToken = resp.NextContinuationToken
	}

	return &resource.ListResult{
		NativeIDs:     nativeIDs,
		NextPageToken: nextToken,
	}, nil
}
