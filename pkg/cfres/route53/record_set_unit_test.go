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

// AWS canonicalizes domain-name record values with a trailing dot. A CNAME
// whose desired value is dot-less (e.g. an ACM DNS-validation target) reads back
// dotted and shows phantom drift on every reconcile, so hostname-valued
// ResourceRecords must be stripped the same way Name and AliasTarget.DNSName are.
func TestBuildReadProperties_StripsCNAMEResourceRecordTrailingDot(t *testing.T) {
	found := &types.ResourceRecordSet{
		Name: aws.String("_abc.example.com."),
		Type: types.RRTypeCname,
		ResourceRecords: []types.ResourceRecord{
			{Value: aws.String("_7a296c6d591b252a4209b817f2ba6fbe.jkddzztszm.acm-validations.aws.")},
		},
	}

	props := buildReadProperties(found, "Z9999999", "_abc.example.com.", "CNAME")

	records, ok := props["ResourceRecords"].([]string)
	require.True(t, ok, "ResourceRecords should be present")
	require.Len(t, records, 1)
	assert.Equal(t, "_7a296c6d591b252a4209b817f2ba6fbe.jkddzztszm.acm-validations.aws", records[0],
		"CNAME record value should have its trailing dot stripped to match dot-less desired state")
}

// MX values are "<pref> <hostname>." — the hostname's trailing dot must be
// stripped too.
func TestBuildReadProperties_StripsMXResourceRecordTrailingDot(t *testing.T) {
	found := &types.ResourceRecordSet{
		Name: aws.String("example.com."),
		Type: types.RRTypeMx,
		ResourceRecords: []types.ResourceRecord{
			{Value: aws.String("10 mail.example.com.")},
		},
	}

	props := buildReadProperties(found, "Z9999999", "example.com.", "MX")

	records := props["ResourceRecords"].([]string)
	require.Len(t, records, 1)
	assert.Equal(t, "10 mail.example.com", records[0])
}

// TXT (and SPF) values are quoted character strings, not domain names — a
// trailing dot is legitimate content and must NOT be stripped.
func TestBuildReadProperties_PreservesTXTResourceRecordTrailingDot(t *testing.T) {
	found := &types.ResourceRecordSet{
		Name: aws.String("example.com."),
		Type: types.RRTypeTxt,
		ResourceRecords: []types.ResourceRecord{
			{Value: aws.String(`"some verification token ending in a dot."`)},
		},
	}

	props := buildReadProperties(found, "Z9999999", "example.com.", "TXT")

	records := props["ResourceRecords"].([]string)
	require.Len(t, records, 1)
	assert.Equal(t, `"some verification token ending in a dot."`, records[0],
		"TXT values must be preserved verbatim — the trailing dot is data, not an FQDN terminator")
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
