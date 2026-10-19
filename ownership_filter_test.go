// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package main

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"

	pkgmodel "github.com/platform-engineering-labs/formae/pkg/model"
)

// excluded reports what discovery would do with this resource: run every filter
// that applies to the type and see whether any of them matches.
func excluded(t *testing.T, resourceType, properties string) bool {
	t.Helper()
	filters := pkgmodel.FiltersForType((&Plugin{}).DiscoveryFilters(), resourceType)
	for i := range filters {
		if filters[i].Excludes(json.RawMessage(properties)) {
			return true
		}
	}
	return false
}

func TestOwnershipMarkerExcludesATaggedResource(t *testing.T) {
	role := `{"RoleName":"fai-t-i","Tags":[{"Key":"formae-ai:owner","Value":"provx"},{"Key":"formae-owned","Value":"true"}]}`

	assert.True(t, excluded(t, "AWS::IAM::Role", role))
}

func TestOwnershipMarkerLeavesEveryOtherResourceAlone(t *testing.T) {
	// Sharing the name prefix is not ownership. These three exist in a real
	// account and none of them is formae's to hide.
	for _, name := range []string{"formae-ecs-task-role", "formae-lgtm-exec-role", "formae-lgtm-task-role"} {
		role := `{"RoleName":"` + name + `","Tags":[{"Key":"app","Value":"formae-agent"}]}`
		assert.False(t, excluded(t, "AWS::IAM::Role", role), name)
	}

	untagged := `{"RoleName":"someone-elses-role"}`
	assert.False(t, excluded(t, "AWS::IAM::Role", untagged))
}

func TestOwnershipMarkerExcludesTheSharedOIDCProvider(t *testing.T) {
	provider := `{"Url":"oidc.cloud.formae.ai","Tags":[{"Key":"formae-owned","Value":"true"}]}`

	assert.True(t, excluded(t, "AWS::IAM::OIDCProvider", provider))
}

// EFS is the exception to $.Tags: FileSystem and AccessPoint expose their tags
// under per-type properties, so a single $.Tags filter would silently miss them.
func TestOwnershipMarkerReachesEFSThroughItsOwnTagProperties(t *testing.T) {
	fs := `{"FileSystemId":"fs-1","FileSystemTags":[{"Key":"formae-owned","Value":"true"}]}`
	ap := `{"AccessPointId":"fsap-1","AccessPointTags":[{"Key":"formae-owned","Value":"true"}]}`

	assert.True(t, excluded(t, "AWS::EFS::FileSystem", fs))
	assert.True(t, excluded(t, "AWS::EFS::AccessPoint", ap))

	assert.False(t, excluded(t, "AWS::EFS::FileSystem", `{"FileSystemId":"fs-2","FileSystemTags":[{"Key":"app","Value":"something"}]}`))
}

func TestOwnershipMarkerIgnoresAValueOtherThanTrue(t *testing.T) {
	role := `{"RoleName":"r","Tags":[{"Key":"formae-owned","Value":"false"}]}`

	assert.False(t, excluded(t, "AWS::IAM::Role", role))
}
