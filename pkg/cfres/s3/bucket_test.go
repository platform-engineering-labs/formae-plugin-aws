// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package s3

import (
	"testing"
)

// enrichBucketProperties derives WebsiteEndpoint (hostname-only) from
// WebsiteURL whenever CCAPI populates the latter. CFN's GetAtt schema for
// AWS::S3::Bucket exposes WebsiteURL but not WebsiteEndpoint, even though
// CloudFront's Distribution.Origin.domainName requires the hostname-only
// form. The enrichment closes that gap without forcing operators to
// string-compose.
//
// The function under test lives in bucket.go.

func TestEnrichBucketProperties_StripsHttpPrefixAndTrailingSlash(t *testing.T) {
	props := map[string]any{
		"WebsiteURL": "http://my-bucket.s3-website-us-west-2.amazonaws.com/",
	}
	enrichBucketProperties(props)

	got, ok := props["WebsiteEndpoint"].(string)
	if !ok {
		t.Fatalf("expected WebsiteEndpoint string, got %T (%v)", props["WebsiteEndpoint"], props["WebsiteEndpoint"])
	}
	want := "my-bucket.s3-website-us-west-2.amazonaws.com"
	if got != want {
		t.Errorf("WebsiteEndpoint = %q, want %q", got, want)
	}
}

func TestEnrichBucketProperties_StripsHttpPrefixWithoutTrailingSlash(t *testing.T) {
	props := map[string]any{
		"WebsiteURL": "http://my-bucket.s3-website-us-west-2.amazonaws.com",
	}
	enrichBucketProperties(props)

	got, ok := props["WebsiteEndpoint"].(string)
	if !ok {
		t.Fatalf("expected WebsiteEndpoint string, got %T (%v)", props["WebsiteEndpoint"], props["WebsiteEndpoint"])
	}
	want := "my-bucket.s3-website-us-west-2.amazonaws.com"
	if got != want {
		t.Errorf("WebsiteEndpoint = %q, want %q", got, want)
	}
}

func TestEnrichBucketProperties_NoWebsiteURL_NoEndpointAdded(t *testing.T) {
	props := map[string]any{
		"BucketName": "no-website-bucket",
	}
	enrichBucketProperties(props)

	if _, present := props["WebsiteEndpoint"]; present {
		t.Errorf("WebsiteEndpoint should not be added when WebsiteURL absent, got %v", props["WebsiteEndpoint"])
	}
}

func TestEnrichBucketProperties_EmptyWebsiteURL_NoEndpointAdded(t *testing.T) {
	props := map[string]any{
		"WebsiteURL": "",
	}
	enrichBucketProperties(props)

	if _, present := props["WebsiteEndpoint"]; present {
		t.Errorf("WebsiteEndpoint should not be added when WebsiteURL is empty, got %v", props["WebsiteEndpoint"])
	}
}

func TestEnrichBucketProperties_DualStackWebsiteURL_StripsCorrectly(t *testing.T) {
	// Dual-stack form returned by S3 in regions that support it; structure differs
	// (`.s3.dualstack.<region>.` instead of `.s3-website-<region>.`) but the
	// transformation is still strip-http://-and-trailing-slash.
	props := map[string]any{
		"WebsiteURL": "http://my-bucket.s3.dualstack.us-east-2.amazonaws.com/",
	}
	enrichBucketProperties(props)

	got, ok := props["WebsiteEndpoint"].(string)
	if !ok {
		t.Fatalf("expected WebsiteEndpoint string, got %T", props["WebsiteEndpoint"])
	}
	want := "my-bucket.s3.dualstack.us-east-2.amazonaws.com"
	if got != want {
		t.Errorf("WebsiteEndpoint = %q, want %q", got, want)
	}
}
