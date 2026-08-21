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

	t.Run("matches when the property echoes the filtered name in ARN form", func(t *testing.T) {
		properties := `{"FunctionName":"arn:aws:lambda:us-east-1:111122223333:function:my-function","Id":"sid-1"}`
		filter := map[string]string{"FunctionName": "my-function"}
		assert.True(t, matchesFilter(properties, filter))
	})

	t.Run("matches when the filter value is an ARN and the property echoes the short name", func(t *testing.T) {
		properties := `{"FunctionName":"my-function","Id":"sid-1"}`
		filter := map[string]string{"FunctionName": "arn:aws:lambda:us-east-1:111122223333:function:my-function"}
		assert.True(t, matchesFilter(properties, filter))
	})

	t.Run("does not match when the ARN-form property names a different resource", func(t *testing.T) {
		properties := `{"FunctionName":"arn:aws:lambda:us-east-1:111122223333:function:other-function","Id":"sid-1"}`
		filter := map[string]string{"FunctionName": "my-function"}
		assert.False(t, matchesFilter(properties, filter))
	})
}

func TestDiscoveryListExclusions(t *testing.T) {
	t.Run("excludes AWS-managed IAM policies and keeps customer policies", func(t *testing.T) {
		excluded := discoveryListExclusions["AWS::IAM::ManagedPolicy"]
		assert.True(t, excluded("arn:aws:iam::aws:policy/AdministratorAccess"))
		assert.True(t, excluded("arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole"))
		assert.False(t, excluded("arn:aws:iam::111122223333:policy/my-policy"))
	})

	t.Run("excludes AWS-managed KMS aliases and keeps customer aliases", func(t *testing.T) {
		excluded := discoveryListExclusions["AWS::KMS::Alias"]
		assert.True(t, excluded("alias/aws/s3"))
		assert.False(t, excluded("alias/my-key"))
		assert.False(t, excluded("alias/awsome-key"))
	})

	t.Run("has no exclusion for other types", func(t *testing.T) {
		assert.Nil(t, discoveryListExclusions["AWS::S3::Bucket"])
	})
}
