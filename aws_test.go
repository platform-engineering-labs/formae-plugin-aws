// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMatchesFilter(t *testing.T) {
	t.Run("matches when all filter properties are present and equal", func(t *testing.T) {
		properties := `{"VpcId":"vpc-123","SubnetId":"subnet-456","CidrBlock":"10.0.0.0/24"}`
		filter := map[string]string{"VpcId": "vpc-123"}
		assert.True(t, matchesFilter(properties, filter))
	})

	t.Run("does not match when filter property value differs", func(t *testing.T) {
		properties := `{"VpcId":"vpc-999","SubnetId":"subnet-456"}`
		filter := map[string]string{"VpcId": "vpc-123"}
		assert.False(t, matchesFilter(properties, filter))
	})

	t.Run("matches with multiple filter properties", func(t *testing.T) {
		properties := `{"ClusterName":"my-cluster","ServiceName":"my-service","TaskSetId":"ts-1"}`
		filter := map[string]string{"ClusterName": "my-cluster", "ServiceName": "my-service"}
		assert.True(t, matchesFilter(properties, filter))
	})

	t.Run("does not match when one of multiple filter properties differs", func(t *testing.T) {
		properties := `{"ClusterName":"my-cluster","ServiceName":"other-service","TaskSetId":"ts-1"}`
		filter := map[string]string{"ClusterName": "my-cluster", "ServiceName": "my-service"}
		assert.False(t, matchesFilter(properties, filter))
	})

	t.Run("includes resource when filter property is missing from response", func(t *testing.T) {
		properties := `{"SubnetId":"subnet-456"}`
		filter := map[string]string{"VpcId": "vpc-123"}
		assert.True(t, matchesFilter(properties, filter))
	})

	t.Run("includes resource when properties JSON is malformed", func(t *testing.T) {
		properties := `not-json`
		filter := map[string]string{"VpcId": "vpc-123"}
		assert.True(t, matchesFilter(properties, filter))
	})

	t.Run("includes resource when property value is not a string", func(t *testing.T) {
		properties := `{"VpcId":{"nested":"object"},"SubnetId":"subnet-456"}`
		filter := map[string]string{"VpcId": "vpc-123"}
		assert.True(t, matchesFilter(properties, filter))
	})

	t.Run("matches with empty filter", func(t *testing.T) {
		properties := `{"VpcId":"vpc-123"}`
		filter := map[string]string{}
		assert.True(t, matchesFilter(properties, filter))
	})
}
