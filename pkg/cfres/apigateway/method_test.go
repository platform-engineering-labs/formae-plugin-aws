// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package apigateway

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// lambdaArnFromInvocationURI is the precise inverse of the write-time builder
// arn:<partition>:apigateway:<region>:lambda:path/2015-03-31/functions/<lambdaArn>/invocations.
// It extracts the full Lambda ARN (including any alias/version qualifier) from a
// Lambda-proxy invocation URI, and reports whether the URI matched the grammar.

func TestLambdaArnFromInvocationURI_Valid(t *testing.T) {
	uri := "arn:aws:apigateway:eu-west-1:lambda:path/2015-03-31/functions/arn:aws:lambda:eu-west-1:123456789012:function:Fleet/invocations"

	arn, ok := lambdaArnFromInvocationURI(uri)

	assert.True(t, ok)
	assert.Equal(t, "arn:aws:lambda:eu-west-1:123456789012:function:Fleet", arn)
}

func TestLambdaArnFromInvocationURI_PreservesQualifier(t *testing.T) {
	uri := "arn:aws:apigateway:eu-west-1:lambda:path/2015-03-31/functions/arn:aws:lambda:eu-west-1:123456789012:function:Fleet:prod/invocations"

	arn, ok := lambdaArnFromInvocationURI(uri)

	assert.True(t, ok)
	assert.Equal(t, "arn:aws:lambda:eu-west-1:123456789012:function:Fleet:prod", arn)
}

func TestLambdaArnFromInvocationURI_GovPartition(t *testing.T) {
	uri := "arn:aws-us-gov:apigateway:us-gov-west-1:lambda:path/2015-03-31/functions/arn:aws-us-gov:lambda:us-gov-west-1:123456789012:function:Fleet/invocations"

	arn, ok := lambdaArnFromInvocationURI(uri)

	assert.True(t, ok)
	assert.Equal(t, "arn:aws-us-gov:lambda:us-gov-west-1:123456789012:function:Fleet", arn)
}

func TestLambdaArnFromInvocationURI_HttpURI_NoMatch(t *testing.T) {
	_, ok := lambdaArnFromInvocationURI("https://example.com/orders")

	assert.False(t, ok)
}

func TestLambdaArnFromInvocationURI_Empty_NoMatch(t *testing.T) {
	_, ok := lambdaArnFromInvocationURI("")

	assert.False(t, ok)
}

func TestLambdaArnFromInvocationURI_NoArnBody_NoMatch(t *testing.T) {
	// Marker present but nothing between /functions/ and /invocations.
	_, ok := lambdaArnFromInvocationURI("arn:aws:apigateway:eu-west-1:lambda:path/2015-03-31/functions//invocations")

	assert.False(t, ok)
}

// reverseLambdaIntegrationURI normalizes a CloudControl read-back Integration:
// a Lambda-proxy Uri is restored to the formae-only LambdaFunctionArn field (and
// Uri dropped) so the rich read-back compares equal to the sparse desired model.
// Non-Lambda integrations (HTTP with a literal uri, MOCK with no uri) are left
// untouched, so no inverted drift is introduced.

func TestReverseLambdaIntegrationURI_LambdaProxy(t *testing.T) {
	integration := map[string]any{
		"Type":                  "AWS_PROXY",
		"IntegrationHttpMethod": "POST",
		"Uri":                   "arn:aws:apigateway:eu-west-1:lambda:path/2015-03-31/functions/arn:aws:lambda:eu-west-1:123456789012:function:Fleet/invocations",
	}

	reverseLambdaIntegrationURI(integration)

	assert.Equal(t, "arn:aws:lambda:eu-west-1:123456789012:function:Fleet", integration["LambdaFunctionArn"])
	_, hasURI := integration["Uri"]
	assert.False(t, hasURI)
}

func TestReverseLambdaIntegrationURI_HttpUntouched(t *testing.T) {
	integration := map[string]any{
		"Type": "HTTP_PROXY",
		"Uri":  "https://example.com/orders",
	}

	reverseLambdaIntegrationURI(integration)

	_, hasArn := integration["LambdaFunctionArn"]
	assert.False(t, hasArn)
	assert.Equal(t, "https://example.com/orders", integration["Uri"])
}

func TestReverseLambdaIntegrationURI_MockUntouched(t *testing.T) {
	integration := map[string]any{
		"Type": "MOCK",
	}

	reverseLambdaIntegrationURI(integration)

	_, hasArn := integration["LambdaFunctionArn"]
	assert.False(t, hasArn)
	_, hasURI := integration["Uri"]
	assert.False(t, hasURI)
}

// normalizeIntegrationOnRead is the read-level transform applied to the
// CloudControl properties JSON before it is returned to core.

func TestNormalizeIntegrationOnRead_LambdaProxy(t *testing.T) {
	in := `{"HttpMethod":"GET","Integration":{"Type":"AWS_PROXY","IntegrationHttpMethod":"POST","Uri":"arn:aws:apigateway:eu-west-1:lambda:path/2015-03-31/functions/arn:aws:lambda:eu-west-1:123456789012:function:Fleet/invocations"}}`

	out, err := normalizeIntegrationOnRead(in)

	require.NoError(t, err)
	var props map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &props))
	integration := props["Integration"].(map[string]any)
	assert.Equal(t, "arn:aws:lambda:eu-west-1:123456789012:function:Fleet", integration["LambdaFunctionArn"])
	_, hasURI := integration["Uri"]
	assert.False(t, hasURI)
}

func TestNormalizeIntegrationOnRead_HttpPassThrough(t *testing.T) {
	in := `{"HttpMethod":"ANY","Integration":{"Type":"HTTP_PROXY","Uri":"https://example.com/orders"}}`

	out, err := normalizeIntegrationOnRead(in)

	require.NoError(t, err)
	assert.JSONEq(t, in, out)
}

func TestNormalizeIntegrationOnRead_NoIntegration(t *testing.T) {
	in := `{"HttpMethod":"GET"}`

	out, err := normalizeIntegrationOnRead(in)

	require.NoError(t, err)
	assert.JSONEq(t, in, out)
}

// transformLambdaIntegrationPatch rewrites the formae-only LambdaFunctionArn
// inside a JSON Patch document into the CloudControl invocation Uri. Integration
// uses an Atomic update method, so a re-pointed Lambda integration arrives as a
// single add/replace op at /Integration carrying the whole object; CloudControl
// rejects LambdaFunctionArn, so it must be converted just like the write path.

func TestTransformLambdaIntegrationPatch_AtomicReplace(t *testing.T) {
	m := &Method{}
	in := `[{"op":"replace","path":"/Integration","value":{"Type":"AWS_PROXY","IntegrationHttpMethod":"POST","LambdaFunctionArn":"arn:aws:lambda:eu-west-1:123456789012:function:Fleet"}}]`

	out, err := m.transformLambdaIntegrationPatch(in)

	require.NoError(t, err)
	var ops []map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &ops))
	require.Len(t, ops, 1)
	integration := ops[0]["value"].(map[string]any)
	assert.Equal(t, "arn:aws:apigateway:eu-west-1:lambda:path/2015-03-31/functions/arn:aws:lambda:eu-west-1:123456789012:function:Fleet/invocations", integration["Uri"])
	_, hasArn := integration["LambdaFunctionArn"]
	assert.False(t, hasArn)
}

func TestTransformLambdaIntegrationPatch_AtomicAdd(t *testing.T) {
	m := &Method{}
	in := `[{"op":"add","path":"/Integration","value":{"Type":"AWS_PROXY","IntegrationHttpMethod":"POST","LambdaFunctionArn":"arn:aws:lambda:eu-west-1:123456789012:function:Fleet"}}]`

	out, err := m.transformLambdaIntegrationPatch(in)

	require.NoError(t, err)
	var ops []map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &ops))
	integration := ops[0]["value"].(map[string]any)
	assert.Equal(t, "arn:aws:apigateway:eu-west-1:lambda:path/2015-03-31/functions/arn:aws:lambda:eu-west-1:123456789012:function:Fleet/invocations", integration["Uri"])
	_, hasArn := integration["LambdaFunctionArn"]
	assert.False(t, hasArn)
}

func TestTransformLambdaIntegrationPatch_HttpIntegrationUntouched(t *testing.T) {
	m := &Method{}
	in := `[{"op":"replace","path":"/Integration","value":{"Type":"HTTP_PROXY","Uri":"https://example.com/orders"}}]`

	out, err := m.transformLambdaIntegrationPatch(in)

	require.NoError(t, err)
	assert.JSONEq(t, in, out)
}

func TestTransformLambdaIntegrationPatch_OtherOpsUntouched(t *testing.T) {
	m := &Method{}
	in := `[{"op":"replace","path":"/OperationName","value":"updated"}]`

	out, err := m.transformLambdaIntegrationPatch(in)

	require.NoError(t, err)
	assert.JSONEq(t, in, out)
}

// handleLambdaIntegration makes lambdaFunctionArn win when both it and a uri are
// set: it derives the invocation Uri from the ARN, overwrites any supplied uri,
// and drops the convenience field before the CloudControl call.
func TestHandleLambdaIntegration_LambdaFunctionArnWinsOverUri(t *testing.T) {
	m := &Method{}
	in := []byte(`{"Integration":{"Type":"AWS_PROXY","IntegrationHttpMethod":"POST","LambdaFunctionArn":"arn:aws:lambda:eu-west-1:123456789012:function:Fleet","Uri":"https://stale.example.com"}}`)

	out, err := m.handleLambdaIntegration(context.Background(), in)

	require.NoError(t, err)
	var props map[string]any
	require.NoError(t, json.Unmarshal(out, &props))
	integration := props["Integration"].(map[string]any)
	assert.Equal(t, "arn:aws:apigateway:eu-west-1:lambda:path/2015-03-31/functions/arn:aws:lambda:eu-west-1:123456789012:function:Fleet/invocations", integration["Uri"])
	_, hasArn := integration["LambdaFunctionArn"]
	assert.False(t, hasArn)
}
