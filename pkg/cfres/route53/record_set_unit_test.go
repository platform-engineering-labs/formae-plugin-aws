// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package route53

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/route53/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// AWS Route53 canonicalizes DNS names with a trailing dot. Formae stores them
// without one (Name is already stripped on read), so the alias target DNSName
// must be stripped the same way — otherwise an unchanged ALIAS record shows
// phantom drift on every reconcile and a fresh create loops on read-back.
func TestBuildReadProperties_StripsAliasTargetTrailingDot(t *testing.T) {
	found := &types.ResourceRecordSet{
		Name: aws.String("formae-agent.platform.engineering."),
		Type: types.RRTypeA,
		AliasTarget: &types.AliasTarget{
			DNSName:              aws.String("formae-agent-bridge-1460787171.us-west-2.elb.amazonaws.com."),
			HostedZoneId:         aws.String("Z1H1FL5HABSF5"),
			EvaluateTargetHealth: true,
		},
	}

	props := buildReadProperties(found, "Z9999999", "formae-agent.platform.engineering.", "A")

	alias, ok := props["AliasTarget"].(map[string]any)
	require.True(t, ok, "AliasTarget should be present in read properties")
	assert.Equal(t, "formae-agent-bridge-1460787171.us-west-2.elb.amazonaws.com", alias["DNSName"],
		"alias DNSName should have its trailing dot stripped to match stored state")
}

// When building an AWS AliasTarget from stored (no-dot) state to send back to
// Route53, the trailing dot must be restored. The Update path deletes the prior
// record by value, and Route53 rejects the delete unless the alias DNSName
// matches its canonical (dotted) form — the "values provided do not match the
// current values" failure.
func TestBuildAliasTarget_AddsTrailingDot(t *testing.T) {
	raw := map[string]any{
		"DNSName":              "formae-agent-bridge-1460787171.us-west-2.elb.amazonaws.com",
		"HostedZoneId":         "Z1H1FL5HABSF5",
		"EvaluateTargetHealth": true,
	}

	target, err := buildAliasTarget(raw)
	require.NoError(t, err)
	require.NotNil(t, target)
	assert.Equal(t, "formae-agent-bridge-1460787171.us-west-2.elb.amazonaws.com.", aws.ToString(target.DNSName),
		"outbound alias DNSName should carry the canonical trailing dot")
}

// EvaluateTargetHealth must be carried through from the declared properties, not
// hardcoded — otherwise an update silently flips a `true` setting to `false`.
func TestBuildAliasTarget_HonorsEvaluateTargetHealth(t *testing.T) {
	target, err := buildAliasTarget(map[string]any{
		"DNSName":              "example.elb.amazonaws.com.",
		"HostedZoneId":         "Z1H1FL5HABSF5",
		"EvaluateTargetHealth": true,
	})
	require.NoError(t, err)
	assert.True(t, target.EvaluateTargetHealth, "EvaluateTargetHealth should reflect the declared value")
}
