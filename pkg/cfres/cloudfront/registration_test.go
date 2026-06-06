// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package cloudfront

import (
	"testing"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres/registry"
)

// TestCustomProvisioners_AreRegisteredForUpdate guards against init()
// ordering or build-tag mishaps that would let our Function/Distribution
// Update overrides fall through to CCAPI silently. If this test
// regresses, the dispatcher in aws.go won't see HasProvisioner==true and
// CCAPI handles Update instead — which is the silent failure mode that
// reintroduced Bug 1 post-dev.12.
func TestCustomProvisioners_AreRegisteredForUpdate(t *testing.T) {
	cases := []struct {
		resourceType string
		operation    resource.Operation
	}{
		{"AWS::CloudFront::Function", resource.OperationUpdate},
		{"AWS::CloudFront::Distribution", resource.OperationUpdate},
	}
	for _, tc := range cases {
		if !registry.HasProvisioner(tc.resourceType, tc.operation) {
			t.Errorf("expected registry.HasProvisioner(%q, %v) == true; init() did not register the custom provisioner",
				tc.resourceType, tc.operation)
		}
	}
}
