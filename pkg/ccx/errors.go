// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package ccx

import (
	"regexp"

	cctypes "github.com/aws/aws-sdk-go-v2/service/cloudcontrol/types"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"

	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/helper"
)

// awsEventualConsistencyPatterns describes AWS errors that surface as
// HandlerErrorCodeInvalidRequest on synchronous Create/Update/Delete but are
// actually transient cross-service state-propagation issues. The plugin SDK's
// recoverableErrorCodes set excludes InvalidRequest (correctly — most
// InvalidRequest errors really are bad request payloads), so these specific
// patterns get re-classified to ResourceConflict (which IS recoverable) so the
// PluginOperator's retry pipeline absorbs the race.
//
// Pairing each pattern with the InvalidRequest code means a wording change
// degrades gracefully (no remap, the original failure surfaces) while a code
// change is caught loudly by the dedicated unit tests.
var awsEventualConsistencyPatterns = []*regexp.Regexp{
	// RDS rejects DBSubnetGroup creates when EC2-created subnets aren't yet
	// visible to RDS's internal subnet cache.
	regexp.MustCompile(`(?i)some input subnets .* are invalid`),
}

// classifyCloudControlError converts a synchronous CloudControl SDK error into
// a ProgressResult with the appropriate OperationErrorCode. Returns
// (nil, false) when the error is not a recognised CloudControl exception, in
// which case the caller should return the raw Go error (the agent will then
// tag it OperationErrorCodeUnforeseenError and surface it as terminal).
//
// This classification is the foundation of the agent's recoverability
// machinery: without it every sync error from CCAPI becomes terminal,
// regardless of whether it's actually transient. See
// formae/pkg/plugin/plugin_operator.go for the agent-side retry logic and
// formae/pkg/plugin/resource/resource.go for recoverableErrorCodes.
func classifyCloudControlError(err error, op resource.Operation) (*resource.ProgressResult, bool) {
	code, ok := helper.HandleCloudControlError(err)
	if !ok {
		return nil, false
	}
	msg := err.Error()
	opCode := resource.OperationErrorCode(code)
	if code == cctypes.HandlerErrorCodeInvalidRequest && matchesEventualConsistency(msg) {
		// Re-classify as a recoverable code so PluginOperator retries.
		// ResourceConflict isn't a perfect semantic fit (there's no
		// conflict, just stale state); a dedicated
		// OperationErrorCodeEventualConsistency is tracked as a follow-up
		// in the engineering notes.
		opCode = resource.OperationErrorCodeResourceConflict
	}
	return &resource.ProgressResult{
		Operation:       op,
		OperationStatus: resource.OperationStatusFailure,
		ErrorCode:       opCode,
		StatusMessage:   msg,
	}, true
}

func matchesEventualConsistency(msg string) bool {
	for _, re := range awsEventualConsistencyPatterns {
		if re.MatchString(msg) {
			return true
		}
	}
	return false
}
