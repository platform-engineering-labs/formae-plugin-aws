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

// ----- fake CloudFront client -----

type fakeCloudFrontClient struct {
	getDistributionConfigInput  *cloudfront.GetDistributionConfigInput
	getDistributionConfigOut    *cloudfront.GetDistributionConfigOutput
	getDistributionConfigErr    error
	updateDistributionInput     *cloudfront.UpdateDistributionInput
	updateDistributionOut       *cloudfront.UpdateDistributionOutput
	updateDistributionErr       error
	getDistributionInput        *cloudfront.GetDistributionInput
	getDistributionOut          *cloudfront.GetDistributionOutput
	listTagsForResourceInput    *cloudfront.ListTagsForResourceInput
	listTagsForResourceOut      *cloudfront.ListTagsForResourceOutput
	tagResourceInput            *cloudfront.TagResourceInput
	untagResourceInput          *cloudfront.UntagResourceInput

	describeFunctionInput  *cloudfront.DescribeFunctionInput
	describeFunctionOut    *cloudfront.DescribeFunctionOutput
	describeFunctionErr    error
	updateFunctionInput    *cloudfront.UpdateFunctionInput
	updateFunctionOut      *cloudfront.UpdateFunctionOutput
	updateFunctionErr      error
	publishFunctionInput   *cloudfront.PublishFunctionInput
	publishFunctionOut     *cloudfront.PublishFunctionOutput
	publishFunctionErr     error
	getFunctionInput       *cloudfront.GetFunctionInput
	getFunctionOut         *cloudfront.GetFunctionOutput
}

func (f *fakeCloudFrontClient) GetDistributionConfig(_ context.Context, in *cloudfront.GetDistributionConfigInput, _ ...func(*cloudfront.Options)) (*cloudfront.GetDistributionConfigOutput, error) {
	f.getDistributionConfigInput = in
	if f.getDistributionConfigErr != nil {
		return nil, f.getDistributionConfigErr
	}
	return f.getDistributionConfigOut, nil
}

func (f *fakeCloudFrontClient) UpdateDistribution(_ context.Context, in *cloudfront.UpdateDistributionInput, _ ...func(*cloudfront.Options)) (*cloudfront.UpdateDistributionOutput, error) {
	f.updateDistributionInput = in
	if f.updateDistributionErr != nil {
		return nil, f.updateDistributionErr
	}
	if f.updateDistributionOut != nil {
		return f.updateDistributionOut, nil
	}
	return &cloudfront.UpdateDistributionOutput{}, nil
}

func (f *fakeCloudFrontClient) GetDistribution(_ context.Context, in *cloudfront.GetDistributionInput, _ ...func(*cloudfront.Options)) (*cloudfront.GetDistributionOutput, error) {
	f.getDistributionInput = in
	if f.getDistributionOut != nil {
		return f.getDistributionOut, nil
	}
	return &cloudfront.GetDistributionOutput{Distribution: &cftypes.Distribution{ARN: aws.String("arn:aws:cloudfront::123:distribution/E1ABCDE")}}, nil
}

func (f *fakeCloudFrontClient) ListTagsForResource(_ context.Context, in *cloudfront.ListTagsForResourceInput, _ ...func(*cloudfront.Options)) (*cloudfront.ListTagsForResourceOutput, error) {
	f.listTagsForResourceInput = in
	if f.listTagsForResourceOut != nil {
		return f.listTagsForResourceOut, nil
	}
	return &cloudfront.ListTagsForResourceOutput{Tags: &cftypes.Tags{}}, nil
}

func (f *fakeCloudFrontClient) TagResource(_ context.Context, in *cloudfront.TagResourceInput, _ ...func(*cloudfront.Options)) (*cloudfront.TagResourceOutput, error) {
	f.tagResourceInput = in
	return &cloudfront.TagResourceOutput{}, nil
}

func (f *fakeCloudFrontClient) UntagResource(_ context.Context, in *cloudfront.UntagResourceInput, _ ...func(*cloudfront.Options)) (*cloudfront.UntagResourceOutput, error) {
	f.untagResourceInput = in
	return &cloudfront.UntagResourceOutput{}, nil
}

func (f *fakeCloudFrontClient) DescribeFunction(_ context.Context, in *cloudfront.DescribeFunctionInput, _ ...func(*cloudfront.Options)) (*cloudfront.DescribeFunctionOutput, error) {
	f.describeFunctionInput = in
	if f.describeFunctionErr != nil {
		return nil, f.describeFunctionErr
	}
	return f.describeFunctionOut, nil
}

func (f *fakeCloudFrontClient) UpdateFunction(_ context.Context, in *cloudfront.UpdateFunctionInput, _ ...func(*cloudfront.Options)) (*cloudfront.UpdateFunctionOutput, error) {
	f.updateFunctionInput = in
	if f.updateFunctionErr != nil {
		return nil, f.updateFunctionErr
	}
	if f.updateFunctionOut != nil {
		return f.updateFunctionOut, nil
	}
	return &cloudfront.UpdateFunctionOutput{ETag: aws.String("ETAG-AFTER-UPDATE")}, nil
}

func (f *fakeCloudFrontClient) PublishFunction(_ context.Context, in *cloudfront.PublishFunctionInput, _ ...func(*cloudfront.Options)) (*cloudfront.PublishFunctionOutput, error) {
	f.publishFunctionInput = in
	if f.publishFunctionErr != nil {
		return nil, f.publishFunctionErr
	}
	return f.publishFunctionOut, nil
}

func (f *fakeCloudFrontClient) GetFunction(_ context.Context, in *cloudfront.GetFunctionInput, _ ...func(*cloudfront.Options)) (*cloudfront.GetFunctionOutput, error) {
	f.getFunctionInput = in
	if f.getFunctionOut != nil {
		return f.getFunctionOut, nil
	}
	return &cloudfront.GetFunctionOutput{}, nil
}

// ----- helpers -----

func newDistributionForTest(client CloudFrontClientInterface) *Distribution {
	return &Distribution{
		cfg: &config.Config{},
		cloudFrontClientFactory: func(_ *config.Config) (CloudFrontClientInterface, error) {
			return client, nil
		},
	}
}

func liveDistributionConfig() *cftypes.DistributionConfig {
	return &cftypes.DistributionConfig{
		CallerReference: aws.String("caller-ref-original"),
		Comment:         aws.String("original comment"),
		Enabled:         aws.Bool(true),
		Origins: &cftypes.Origins{
			Quantity: aws.Int32(1),
			Items: []cftypes.Origin{
				{
					Id:         aws.String("origin-1"),
					DomainName: aws.String("origin.example.com"),
				},
			},
		},
		DefaultCacheBehavior: &cftypes.DefaultCacheBehavior{
			TargetOriginId:       aws.String("origin-1"),
			ViewerProtocolPolicy: cftypes.ViewerProtocolPolicyRedirectToHttps,
		},
		ViewerCertificate: &cftypes.ViewerCertificate{
			ACMCertificateArn: aws.String("arn:aws:acm:us-east-1:123:certificate/abc"),
			SSLSupportMethod:  cftypes.SSLSupportMethodSniOnly,
		},
	}
}

// ----- tests -----

func TestDistribution_Update_FetchesLiveConfigAndCapturesETag(t *testing.T) {
	client := &fakeCloudFrontClient{
		getDistributionConfigOut: &cloudfront.GetDistributionConfigOutput{
			DistributionConfig: liveDistributionConfig(),
			ETag:               aws.String("E2QWRUHAPOMQZL"),
		},
	}
	d := newDistributionForTest(client)

	desired := map[string]any{
		"Id": "E1ABCDE",
		"DistributionConfig": map[string]any{
			"CallerReference": "caller-ref-original",
			"Comment":         "updated comment",
			"Enabled":         true,
			"Origins": map[string]any{
				"Quantity": float64(1),
				"Items":    []any{map[string]any{"Id": "origin-1", "DomainName": "origin.example.com"}},
			},
			"DefaultCacheBehavior": map[string]any{
				"TargetOriginId":       "origin-1",
				"ViewerProtocolPolicy": "redirect-to-https",
			},
			"ViewerCertificate": map[string]any{
				"AcmCertificateArn": "arn:aws:acm:us-east-1:123:certificate/abc",
				"SslSupportMethod":  "sni-only",
			},
		},
	}
	desiredBytes, _ := json.Marshal(desired)

	prior := map[string]any{
		"Id": "E1ABCDE",
		"DistributionConfig": map[string]any{
			"CallerReference": "caller-ref-original",
			"Comment":         "original comment",
		},
	}
	priorBytes, _ := json.Marshal(prior)

	patch := `[{"op":"replace","path":"/DistributionConfig/Comment","value":"updated comment"}]`

	_, err := d.Update(context.Background(), &resource.UpdateRequest{
		NativeID:          "E1ABCDE",
		ResourceType:      "AWS::CloudFront::Distribution",
		PriorProperties:   priorBytes,
		DesiredProperties: desiredBytes,
		PatchDocument:     &patch,
	})
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	if client.getDistributionConfigInput == nil {
		t.Fatal("expected GetDistributionConfig to be called")
	}
	if aws.ToString(client.getDistributionConfigInput.Id) != "E1ABCDE" {
		t.Fatalf("GetDistributionConfig called with wrong Id: %q", aws.ToString(client.getDistributionConfigInput.Id))
	}

	if client.updateDistributionInput == nil {
		t.Fatal("expected UpdateDistribution to be called")
	}
	if aws.ToString(client.updateDistributionInput.IfMatch) != "E2QWRUHAPOMQZL" {
		t.Fatalf("UpdateDistribution called with wrong IfMatch: %q", aws.ToString(client.updateDistributionInput.IfMatch))
	}
	if aws.ToString(client.updateDistributionInput.Id) != "E1ABCDE" {
		t.Fatalf("UpdateDistribution called with wrong Id: %q", aws.ToString(client.updateDistributionInput.Id))
	}
}

func TestDistribution_Update_AppliesPatchOntoLiveConfigPreservingRequiredFields(t *testing.T) {
	// The bug: CCAPI sends only the diff'd field (e.g. Comment), CloudFront
	// then complains that ViewerCertificate is missing the required ACM ARN.
	// Our fix: start from the live config so all required fields are present,
	// then overlay only the operator's actual change.
	client := &fakeCloudFrontClient{
		getDistributionConfigOut: &cloudfront.GetDistributionConfigOutput{
			DistributionConfig: liveDistributionConfig(),
			ETag:               aws.String("E2QWRUHAPOMQZL"),
		},
	}
	d := newDistributionForTest(client)

	patch := `[{"op":"replace","path":"/DistributionConfig/Comment","value":"updated comment"}]`
	desired, _ := json.Marshal(map[string]any{
		"Id": "E1ABCDE",
		"DistributionConfig": map[string]any{
			"Comment": "updated comment",
		},
	})

	_, err := d.Update(context.Background(), &resource.UpdateRequest{
		NativeID:          "E1ABCDE",
		ResourceType:      "AWS::CloudFront::Distribution",
		DesiredProperties: desired,
		PatchDocument:     &patch,
	})
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	if client.updateDistributionInput == nil || client.updateDistributionInput.DistributionConfig == nil {
		t.Fatal("expected UpdateDistribution to be called with a non-nil DistributionConfig")
	}
	cfg := client.updateDistributionInput.DistributionConfig

	// The operator's change made it through:
	if aws.ToString(cfg.Comment) != "updated comment" {
		t.Errorf("Comment not updated: got %q, want %q", aws.ToString(cfg.Comment), "updated comment")
	}
	// Fields not in the patch survive from live state (this is the bug fix):
	if cfg.ViewerCertificate == nil || aws.ToString(cfg.ViewerCertificate.ACMCertificateArn) != "arn:aws:acm:us-east-1:123:certificate/abc" {
		t.Errorf("ViewerCertificate.ACMCertificateArn lost during merge: %+v", cfg.ViewerCertificate)
	}
	if cfg.DefaultCacheBehavior == nil || aws.ToString(cfg.DefaultCacheBehavior.TargetOriginId) != "origin-1" {
		t.Errorf("DefaultCacheBehavior lost during merge: %+v", cfg.DefaultCacheBehavior)
	}
	if cfg.Origins == nil || len(cfg.Origins.Items) != 1 || aws.ToString(cfg.Origins.Items[0].Id) != "origin-1" {
		t.Errorf("Origins lost during merge: %+v", cfg.Origins)
	}
	if aws.ToString(cfg.CallerReference) != "caller-ref-original" {
		t.Errorf("CallerReference lost during merge: got %q", aws.ToString(cfg.CallerReference))
	}
}

func TestDistribution_Update_AppliesNestedPatchOps(t *testing.T) {
	client := &fakeCloudFrontClient{
		getDistributionConfigOut: &cloudfront.GetDistributionConfigOutput{
			DistributionConfig: liveDistributionConfig(),
			ETag:               aws.String("ETAG"),
		},
	}
	d := newDistributionForTest(client)

	// Operator swaps the origin's DomainName.
	patch := `[{"op":"replace","path":"/DistributionConfig/Origins/Items/0/DomainName","value":"new-origin.example.com"}]`
	desired, _ := json.Marshal(map[string]any{"Id": "E1ABCDE"})

	_, err := d.Update(context.Background(), &resource.UpdateRequest{
		NativeID:          "E1ABCDE",
		ResourceType:      "AWS::CloudFront::Distribution",
		DesiredProperties: desired,
		PatchDocument:     &patch,
	})
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}
	cfg := client.updateDistributionInput.DistributionConfig
	if cfg.Origins == nil || len(cfg.Origins.Items) != 1 {
		t.Fatalf("expected 1 origin, got %d", len(cfg.Origins.Items))
	}
	if aws.ToString(cfg.Origins.Items[0].DomainName) != "new-origin.example.com" {
		t.Errorf("nested patch not applied: DomainName=%q", aws.ToString(cfg.Origins.Items[0].DomainName))
	}
}

func TestDistribution_Update_ReportsErrorWhenGetFails(t *testing.T) {
	client := &fakeCloudFrontClient{
		getDistributionConfigErr: errors.New("GetDistributionConfig failed"),
	}
	d := newDistributionForTest(client)
	desired, _ := json.Marshal(map[string]any{"Id": "E1ABCDE"})
	_, err := d.Update(context.Background(), &resource.UpdateRequest{
		NativeID:          "E1ABCDE",
		ResourceType:      "AWS::CloudFront::Distribution",
		DesiredProperties: desired,
	})
	if err == nil {
		t.Fatal("expected error when GetDistributionConfig fails")
	}
}

func TestDistribution_Update_HandlesTagAddAndRemove(t *testing.T) {
	client := &fakeCloudFrontClient{
		getDistributionConfigOut: &cloudfront.GetDistributionConfigOutput{
			DistributionConfig: liveDistributionConfig(),
			ETag:               aws.String("ETAG"),
		},
	}
	d := newDistributionForTest(client)

	prior, _ := json.Marshal(map[string]any{
		"Id": "E1ABCDE",
		"Tags": []any{
			map[string]any{"Key": "Env", "Value": "staging"},
			map[string]any{"Key": "Owner", "Value": "alice"},
		},
	})
	desired, _ := json.Marshal(map[string]any{
		"Id":  "E1ABCDE",
		"Arn": "arn:aws:cloudfront::123:distribution/E1ABCDE",
		"Tags": []any{
			map[string]any{"Key": "Env", "Value": "prod"},   // changed
			map[string]any{"Key": "Team", "Value": "infra"}, // added; Owner removed
		},
	})

	_, err := d.Update(context.Background(), &resource.UpdateRequest{
		NativeID:          "E1ABCDE",
		ResourceType:      "AWS::CloudFront::Distribution",
		PriorProperties:   prior,
		DesiredProperties: desired,
	})
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	if client.tagResourceInput == nil {
		t.Fatal("expected TagResource to be called (for changed+added tags)")
	}
	// Env changed AND Team added → 2 tags in the add payload
	gotTags := map[string]string{}
	if client.tagResourceInput.Tags != nil {
		for _, t := range client.tagResourceInput.Tags.Items {
			gotTags[aws.ToString(t.Key)] = aws.ToString(t.Value)
		}
	}
	if gotTags["Env"] != "prod" || gotTags["Team"] != "infra" {
		t.Errorf("TagResource called with wrong tags: %+v", gotTags)
	}

	if client.untagResourceInput == nil {
		t.Fatal("expected UntagResource to be called (for removed Owner tag)")
	}
	var removedKeys []string
	if client.untagResourceInput.TagKeys != nil {
		removedKeys = client.untagResourceInput.TagKeys.Items
	}
	foundOwner := false
	for _, k := range removedKeys {
		if k == "Owner" {
			foundOwner = true
		}
	}
	if !foundOwner {
		t.Errorf("UntagResource didn't include Owner: %v", removedKeys)
	}
}
