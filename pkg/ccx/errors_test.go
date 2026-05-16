// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package ccx

import (
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	cctypes "github.com/aws/aws-sdk-go-v2/service/cloudcontrol/types"
	"github.com/aws/smithy-go"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	"github.com/stretchr/testify/require"
)

// ccOpError wraps an underlying CloudControl exception in the same shape the
// AWS SDK produces, so HandleCloudControlError's errors.As checks succeed.
func ccOpError(underlying error) error {
	return &smithy.OperationError{
		ServiceID:     "CloudControl",
		OperationName: "CreateResource",
		Err:           underlying,
	}
}

func TestClassifyCloudControlError_InvalidRequest_PassesThrough(t *testing.T) {
	err := ccOpError(&cctypes.InvalidRequestException{Message: aws.String("DesiredState contains an unknown property")})

	pr, ok := classifyCloudControlError(err, resource.OperationCreate)
	require.True(t, ok, "InvalidRequestException must be classified")
	require.NotNil(t, pr)
	require.Equal(t, resource.OperationStatusFailure, pr.OperationStatus)
	require.Equal(t, resource.OperationErrorCodeInvalidRequest, pr.ErrorCode,
		"non-eventual-consistency InvalidRequest must remain InvalidRequest (non-recoverable, terminal)")
	require.Equal(t, resource.OperationCreate, pr.Operation)
	require.Contains(t, pr.StatusMessage, "DesiredState")
}

func TestClassifyCloudControlError_RDSSubnetEventualConsistency_RemapsToResourceConflict(t *testing.T) {
	// AWS RDS error surfaced when EC2-created subnets aren't yet visible to RDS.
	err := ccOpError(&cctypes.InvalidRequestException{
		Message: aws.String("Some input subnets in :[subnet-0f7a9adc9560fae45, subnet-07544dcf861d1f761] are invalid."),
	})

	pr, ok := classifyCloudControlError(err, resource.OperationCreate)
	require.True(t, ok)
	require.NotNil(t, pr)
	require.Equal(t, resource.OperationStatusFailure, pr.OperationStatus)
	require.Equal(t, resource.OperationErrorCodeResourceConflict, pr.ErrorCode,
		"subnet-invalid-after-create is AWS eventual consistency; must remap to ResourceConflict (recoverable) so PluginOperator retries")
	require.True(t, resource.IsRecoverable(pr.ErrorCode), "remapped code must be in recoverableErrorCodes")
}

func TestClassifyCloudControlError_SubnetMessage_NotRemappedOnDifferentCode(t *testing.T) {
	// Same message text but a different exception type — message is a hint, code is the safety rail.
	err := ccOpError(&cctypes.ResourceNotFoundException{
		Message: aws.String("Some input subnets in :[subnet-abc] are invalid."),
	})

	pr, ok := classifyCloudControlError(err, resource.OperationCreate)
	require.True(t, ok)
	require.NotNil(t, pr)
	require.Equal(t, resource.OperationErrorCodeNotFound, pr.ErrorCode,
		"message match alone (without InvalidRequest code) must not remap — code is the safety rail")
}

func TestClassifyCloudControlError_InvalidRequest_UnrelatedMessage_NotRemapped(t *testing.T) {
	err := ccOpError(&cctypes.InvalidRequestException{
		Message: aws.String("PatchDocument operation 'replace' at /Tags is not supported."),
	})

	pr, ok := classifyCloudControlError(err, resource.OperationUpdate)
	require.True(t, ok)
	require.NotNil(t, pr)
	require.Equal(t, resource.OperationErrorCodeInvalidRequest, pr.ErrorCode,
		"InvalidRequest with non-eventual-consistency message must stay InvalidRequest (terminal)")
}

func TestClassifyCloudControlError_Throttling_PassesThrough(t *testing.T) {
	err := ccOpError(&cctypes.ThrottlingException{Message: aws.String("Rate exceeded")})

	pr, ok := classifyCloudControlError(err, resource.OperationCreate)
	require.True(t, ok)
	require.NotNil(t, pr)
	require.Equal(t, resource.OperationErrorCodeThrottling, pr.ErrorCode)
	require.True(t, resource.IsRecoverable(pr.ErrorCode))
}

func TestClassifyCloudControlError_UnknownError_NotClassified(t *testing.T) {
	// Plain Go error, not a smithy OperationError — caller must bubble it up
	// as raw so the agent tags it OperationErrorCodeUnforeseenError.
	pr, ok := classifyCloudControlError(errors.New("boom"), resource.OperationCreate)
	require.False(t, ok)
	require.Nil(t, pr)
}

func TestMatchesEventualConsistency(t *testing.T) {
	cases := []struct {
		name string
		msg  string
		want bool
	}{
		{"RDS lowercase", "some input subnets in :[subnet-abc] are invalid.", true},
		{"RDS mixed case", "Some input Subnets in :[subnet-abc] Are Invalid.", true},
		{"RDS with prefix", "InvalidRequestException: Some input subnets in :[subnet-abc] are invalid.", true},
		{"unrelated invalid message", "the patch document is invalid", false},
		{"empty", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, matchesEventualConsistency(tc.msg))
		})
	}
}
