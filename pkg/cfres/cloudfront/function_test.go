// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package cloudfront

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudfront"
	cftypes "github.com/aws/aws-sdk-go-v2/service/cloudfront/types"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/config"
)

func newFunctionForTest(client CloudFrontClientInterface) *Function {
	return &Function{
		cfg: &config.Config{},
		cloudFrontClientFactory: func(_ *config.Config) (CloudFrontClientInterface, error) {
			return client, nil
		},
	}
}

func functionUpdateRequest(autoPublish bool, code string) *resource.UpdateRequest {
	desired, _ := json.Marshal(map[string]any{
		"Name":         "my-fn",
		"FunctionARN":  "arn:aws:cloudfront::123:function/my-fn",
		"AutoPublish":  autoPublish,
		"FunctionCode": code,
		"FunctionConfig": map[string]any{
			"Comment": "updated",
			"Runtime": "cloudfront-js-2.0",
		},
	})
	return &resource.UpdateRequest{
		NativeID:          "arn:aws:cloudfront::123:function/my-fn",
		ResourceType:      "AWS::CloudFront::Function",
		DesiredProperties: desired,
	}
}

func TestFunction_Update_SendsFunctionCodeWithIfMatchETag(t *testing.T) {
	// The bug: a previous Function.Update reported Success but the live
	// functionCode never changed. The provisioner must (a) include the
	// new FunctionCode in the UpdateFunction payload and (b) forward the
	// ETag captured from DescribeFunction as IfMatch.
	client := &fakeCloudFrontClient{
		describeFunctionOut: &cloudfront.DescribeFunctionOutput{
			ETag: aws.String("ETAG-DEVELOPMENT-1"),
			FunctionSummary: &cftypes.FunctionSummary{
				Name: aws.String("my-fn"),
				FunctionConfig: &cftypes.FunctionConfig{
					Comment: aws.String("old"),
					Runtime: cftypes.FunctionRuntimeCloudfrontJs20,
				},
			},
		},
	}
	f := newFunctionForTest(client)

	_, err := f.Update(context.Background(), functionUpdateRequest(true, "function handler(e) { return e.request; } // v2"))
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}
	if client.describeFunctionInput == nil {
		t.Fatal("expected DescribeFunction to be called")
	}
	if aws.ToString(client.describeFunctionInput.Name) != "my-fn" {
		t.Fatalf("DescribeFunction called with wrong Name: %q", aws.ToString(client.describeFunctionInput.Name))
	}
	if client.updateFunctionInput == nil {
		t.Fatal("expected UpdateFunction to be called")
	}
	if aws.ToString(client.updateFunctionInput.IfMatch) != "ETAG-DEVELOPMENT-1" {
		t.Fatalf("UpdateFunction IfMatch wrong: %q", aws.ToString(client.updateFunctionInput.IfMatch))
	}
	if string(client.updateFunctionInput.FunctionCode) != "function handler(e) { return e.request; } // v2" {
		t.Fatalf("UpdateFunction FunctionCode wrong: %q", string(client.updateFunctionInput.FunctionCode))
	}
	if client.updateFunctionInput.FunctionConfig == nil ||
		aws.ToString(client.updateFunctionInput.FunctionConfig.Comment) != "updated" ||
		client.updateFunctionInput.FunctionConfig.Runtime != cftypes.FunctionRuntimeCloudfrontJs20 {
		t.Fatalf("UpdateFunction FunctionConfig wrong: %+v", client.updateFunctionInput.FunctionConfig)
	}
}

func TestFunction_Update_PublishesWhenAutoPublishTrue(t *testing.T) {
	client := &fakeCloudFrontClient{
		describeFunctionOut: &cloudfront.DescribeFunctionOutput{
			ETag: aws.String("ETAG-DEVELOPMENT-1"),
			FunctionSummary: &cftypes.FunctionSummary{
				Name: aws.String("my-fn"),
				FunctionConfig: &cftypes.FunctionConfig{
					Comment: aws.String("c"),
					Runtime: cftypes.FunctionRuntimeCloudfrontJs20,
				},
			},
		},
	}
	f := newFunctionForTest(client)

	_, err := f.Update(context.Background(), functionUpdateRequest(true, "new code"))
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}
	if client.publishFunctionInput == nil {
		t.Fatal("expected PublishFunction to be called when AutoPublish=true")
	}
	// PublishFunction must use the ETag from UpdateFunction's response, not
	// the original DescribeFunction ETag (Update bumps the version).
	if aws.ToString(client.publishFunctionInput.IfMatch) != "ETAG-AFTER-UPDATE" {
		t.Fatalf("PublishFunction IfMatch wrong: got %q, want ETAG-AFTER-UPDATE", aws.ToString(client.publishFunctionInput.IfMatch))
	}
	if aws.ToString(client.publishFunctionInput.Name) != "my-fn" {
		t.Fatalf("PublishFunction Name wrong: %q", aws.ToString(client.publishFunctionInput.Name))
	}
}

func TestFunction_Update_SkipsPublishWhenAutoPublishFalse(t *testing.T) {
	client := &fakeCloudFrontClient{
		describeFunctionOut: &cloudfront.DescribeFunctionOutput{
			ETag: aws.String("ETAG-DEVELOPMENT-1"),
			FunctionSummary: &cftypes.FunctionSummary{
				Name: aws.String("my-fn"),
				FunctionConfig: &cftypes.FunctionConfig{
					Comment: aws.String("c"),
					Runtime: cftypes.FunctionRuntimeCloudfrontJs20,
				},
			},
		},
	}
	f := newFunctionForTest(client)

	_, err := f.Update(context.Background(), functionUpdateRequest(false, "new code"))
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}
	if client.publishFunctionInput != nil {
		t.Fatal("expected PublishFunction NOT to be called when AutoPublish=false")
	}
}

func TestFunction_Update_ReportsErrorWhenDescribeFails(t *testing.T) {
	client := &fakeCloudFrontClient{
		describeFunctionErr: errors.New("DescribeFunction failed"),
	}
	f := newFunctionForTest(client)
	_, err := f.Update(context.Background(), functionUpdateRequest(true, "code"))
	if err == nil {
		t.Fatal("expected error when DescribeFunction fails")
	}
}

func TestFunction_Update_PreservesNameFromDesiredProperties(t *testing.T) {
	// NativeID is the ARN; the schema's identifier is FunctionARN, but the
	// UpdateFunction SDK call takes Name. The provisioner should extract
	// Name from DesiredProperties.Name (the createOnly field).
	client := &fakeCloudFrontClient{
		describeFunctionOut: &cloudfront.DescribeFunctionOutput{
			ETag: aws.String("E1"),
			FunctionSummary: &cftypes.FunctionSummary{
				Name:           aws.String("my-fn"),
				FunctionConfig: &cftypes.FunctionConfig{Runtime: cftypes.FunctionRuntimeCloudfrontJs20, Comment: aws.String("c")},
			},
		},
	}
	f := newFunctionForTest(client)
	_, err := f.Update(context.Background(), functionUpdateRequest(false, "code"))
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}
	if aws.ToString(client.updateFunctionInput.Name) != "my-fn" {
		t.Fatalf("UpdateFunction Name wrong: %q", aws.ToString(client.updateFunctionInput.Name))
	}
}
