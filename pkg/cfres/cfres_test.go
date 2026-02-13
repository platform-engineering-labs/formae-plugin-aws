// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package cfres

import (
	"testing"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/config"
	"github.com/stretchr/testify/assert"
)

func TestGetProvisioner(t *testing.T) {
	assert.NotNil(t, GetProvisionerForOperation("AWS::Route53::RecordSet", resource.OperationRead, &config.Config{Region: "us-east-1"}))
}

func TestRolePolicyRegistration(t *testing.T) {
	assert.NotNil(t, GetProvisionerForOperation("AWS::IAM::RolePolicy", resource.OperationList, &config.Config{Region: "us-east-1"}))
}

func TestS3ObjectRegistration(t *testing.T) {
	cfg := &config.Config{Region: "us-east-1"}
	assert.NotNil(t, GetProvisionerForOperation("AWS::S3::Object", resource.OperationCreate, cfg))
	assert.NotNil(t, GetProvisionerForOperation("AWS::S3::Object", resource.OperationRead, cfg))
	assert.NotNil(t, GetProvisionerForOperation("AWS::S3::Object", resource.OperationUpdate, cfg))
	assert.NotNil(t, GetProvisionerForOperation("AWS::S3::Object", resource.OperationDelete, cfg))
	assert.NotNil(t, GetProvisionerForOperation("AWS::S3::Object", resource.OperationCheckStatus, cfg))
	assert.NotNil(t, GetProvisionerForOperation("AWS::S3::Object", resource.OperationList, cfg))
}
