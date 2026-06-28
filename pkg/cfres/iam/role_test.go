// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package iam

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres/registry"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/config"
)

const testRoleName = "formae-test-role"

func newRoleWithMocks(ccx *mockRoleCCXReader, iamc *mockRoleClient) *Role {
	return &Role{cfg: &config.Config{Region: "us-east-1"}, ccxClient: ccx, iamClient: iamc}
}

// rolePropsJSON returns a CloudControl-style read result for a role, with the
// inline Policies already stripped (as ccx.ReadResource does via IgnoredFields).
func rolePropsJSON(t *testing.T, roleName string) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"RoleName": roleName,
		"Arn":      "arn:aws:iam::123456789012:role/" + roleName,
		"RoleId":   "AROAEXAMPLE",
	})
	require.NoError(t, err)
	return string(b)
}

// escapedDoc URL-encodes a policy document the way IAM returns it (path style).
func escapedDoc(t *testing.T, doc string) *string {
	t.Helper()
	return aws.String(url.PathEscape(doc))
}

func readEnrichedPolicies(t *testing.T, properties string) []map[string]any {
	t.Helper()
	var p struct {
		Policies []map[string]any `json:"Policies"`
	}
	require.NoError(t, json.Unmarshal([]byte(properties), &p))
	return p.Policies
}

func readReq(nativeID string) *resource.ReadRequest {
	return &resource.ReadRequest{NativeID: nativeID, ResourceType: roleType}
}

func TestRole_Read_EnrichesInlinePolicies(t *testing.T) {
	ccx := &mockRoleCCXReader{}
	ccx.On("ReadResource", mock.Anything, mock.Anything).Return(&resource.ReadResult{
		ResourceType: roleType, Properties: rolePropsJSON(t, testRoleName),
	}, nil)

	iamc := &mockRoleClient{}
	iamc.On("ListRolePolicies", mock.Anything, matchRoleAndNoMarker(testRoleName)).Return(
		&iam.ListRolePoliciesOutput{PolicyNames: []string{"logs-write"}}, nil)
	iamc.On("GetRolePolicy", mock.Anything, matchGetRolePolicy(testRoleName, "logs-write")).Return(
		&iam.GetRolePolicyOutput{
			RoleName:       aws.String(testRoleName),
			PolicyName:     aws.String("logs-write"),
			PolicyDocument: escapedDoc(t, `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"logs:PutLogEvents","Resource":"*"}]}`),
		}, nil)

	r := newRoleWithMocks(ccx, iamc)
	res, err := r.Read(context.Background(), readReq(testRoleName))

	require.NoError(t, err)
	require.Empty(t, res.ErrorCode)
	policies := readEnrichedPolicies(t, res.Properties)
	require.Len(t, policies, 1)
	assert.Equal(t, "logs-write", policies[0]["PolicyName"])
	doc, ok := policies[0]["PolicyDocument"].(map[string]any)
	require.True(t, ok, "PolicyDocument must be parsed structurally, not left as a string")
	assert.Equal(t, "2012-10-17", doc["Version"])
}

func TestRole_Read_Paginates(t *testing.T) {
	ccx := &mockRoleCCXReader{}
	ccx.On("ReadResource", mock.Anything, mock.Anything).Return(&resource.ReadResult{
		ResourceType: roleType, Properties: rolePropsJSON(t, testRoleName),
	}, nil)

	iamc := &mockRoleClient{}
	iamc.On("ListRolePolicies", mock.Anything, matchRoleAndNoMarker(testRoleName)).Return(
		&iam.ListRolePoliciesOutput{PolicyNames: []string{"a-policy"}, IsTruncated: true, Marker: aws.String("next")}, nil)
	iamc.On("ListRolePolicies", mock.Anything, matchRoleAndMarker(testRoleName, "next")).Return(
		&iam.ListRolePoliciesOutput{PolicyNames: []string{"b-policy"}}, nil)
	for _, name := range []string{"a-policy", "b-policy"} {
		iamc.On("GetRolePolicy", mock.Anything, matchGetRolePolicy(testRoleName, name)).Return(
			&iam.GetRolePolicyOutput{
				PolicyName:     aws.String(name),
				PolicyDocument: escapedDoc(t, `{"Version":"2012-10-17"}`),
			}, nil)
	}

	r := newRoleWithMocks(ccx, iamc)
	res, err := r.Read(context.Background(), readReq(testRoleName))

	require.NoError(t, err)
	policies := readEnrichedPolicies(t, res.Properties)
	require.Len(t, policies, 2)
	// deterministic sort by PolicyName
	assert.Equal(t, "a-policy", policies[0]["PolicyName"])
	assert.Equal(t, "b-policy", policies[1]["PolicyName"])
	iamc.AssertExpectations(t)
}

func TestRole_Read_SortsByPolicyName(t *testing.T) {
	ccx := &mockRoleCCXReader{}
	ccx.On("ReadResource", mock.Anything, mock.Anything).Return(&resource.ReadResult{
		ResourceType: roleType, Properties: rolePropsJSON(t, testRoleName),
	}, nil)

	iamc := &mockRoleClient{}
	iamc.On("ListRolePolicies", mock.Anything, matchRoleAndNoMarker(testRoleName)).Return(
		&iam.ListRolePoliciesOutput{PolicyNames: []string{"zebra", "alpha", "mike"}}, nil)
	for _, name := range []string{"zebra", "alpha", "mike"} {
		iamc.On("GetRolePolicy", mock.Anything, matchGetRolePolicy(testRoleName, name)).Return(
			&iam.GetRolePolicyOutput{PolicyName: aws.String(name), PolicyDocument: escapedDoc(t, `{}`)}, nil)
	}

	r := newRoleWithMocks(ccx, iamc)
	res, err := r.Read(context.Background(), readReq(testRoleName))

	require.NoError(t, err)
	policies := readEnrichedPolicies(t, res.Properties)
	require.Len(t, policies, 3)
	assert.Equal(t, []any{"alpha", "mike", "zebra"},
		[]any{policies[0]["PolicyName"], policies[1]["PolicyName"], policies[2]["PolicyName"]})
}

func TestRole_Read_NoInlinePolicies_OmitsPoliciesKey(t *testing.T) {
	ccx := &mockRoleCCXReader{}
	ccx.On("ReadResource", mock.Anything, mock.Anything).Return(&resource.ReadResult{
		ResourceType: roleType, Properties: rolePropsJSON(t, testRoleName),
	}, nil)

	iamc := &mockRoleClient{}
	iamc.On("ListRolePolicies", mock.Anything, matchRoleAndNoMarker(testRoleName)).Return(
		&iam.ListRolePoliciesOutput{PolicyNames: []string{}}, nil)

	r := newRoleWithMocks(ccx, iamc)
	res, err := r.Read(context.Background(), readReq(testRoleName))

	require.NoError(t, err)
	var props map[string]any
	require.NoError(t, json.Unmarshal([]byte(res.Properties), &props))
	assert.NotContains(t, props, "Policies", "a role with no inline policies must not carry an empty Policies key")
	iamc.AssertNotCalled(t, "GetRolePolicy", mock.Anything, mock.Anything)
}

func TestRole_Read_UsesPathUnescapeNotQueryUnescape(t *testing.T) {
	ccx := &mockRoleCCXReader{}
	ccx.On("ReadResource", mock.Anything, mock.Anything).Return(&resource.ReadResult{
		ResourceType: roleType, Properties: rolePropsJSON(t, testRoleName),
	}, nil)

	// A document carrying a literal '+'. PathUnescape preserves it; QueryUnescape
	// would corrupt it into a space.
	doc := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"svc:Get+Put","Resource":"*"}]}`
	iamc := &mockRoleClient{}
	iamc.On("ListRolePolicies", mock.Anything, matchRoleAndNoMarker(testRoleName)).Return(
		&iam.ListRolePoliciesOutput{PolicyNames: []string{"p"}}, nil)
	iamc.On("GetRolePolicy", mock.Anything, matchGetRolePolicy(testRoleName, "p")).Return(
		&iam.GetRolePolicyOutput{PolicyName: aws.String("p"), PolicyDocument: aws.String(doc)}, nil)

	r := newRoleWithMocks(ccx, iamc)
	res, err := r.Read(context.Background(), readReq(testRoleName))

	require.NoError(t, err)
	policies := readEnrichedPolicies(t, res.Properties)
	require.Len(t, policies, 1)
	parsed, _ := json.Marshal(policies[0]["PolicyDocument"])
	assert.Contains(t, string(parsed), "svc:Get+Put")
}

func TestRole_Read_ScalarActionRoundTrips(t *testing.T) {
	ccx := &mockRoleCCXReader{}
	ccx.On("ReadResource", mock.Anything, mock.Anything).Return(&resource.ReadResult{
		ResourceType: roleType, Properties: rolePropsJSON(t, testRoleName),
	}, nil)

	doc := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"*"}]}`
	iamc := &mockRoleClient{}
	iamc.On("ListRolePolicies", mock.Anything, matchRoleAndNoMarker(testRoleName)).Return(
		&iam.ListRolePoliciesOutput{PolicyNames: []string{"p"}}, nil)
	iamc.On("GetRolePolicy", mock.Anything, matchGetRolePolicy(testRoleName, "p")).Return(
		&iam.GetRolePolicyOutput{PolicyName: aws.String("p"), PolicyDocument: escapedDoc(t, doc)}, nil)

	r := newRoleWithMocks(ccx, iamc)
	res, err := r.Read(context.Background(), readReq(testRoleName))

	require.NoError(t, err)
	policies := readEnrichedPolicies(t, res.Properties)
	pdoc := policies[0]["PolicyDocument"].(map[string]any)
	stmt := pdoc["Statement"].([]any)[0].(map[string]any)
	assert.Equal(t, "s3:GetObject", stmt["Action"], "scalar Action must survive as a scalar")
}

func TestRole_Read_PunctuatedPolicyName(t *testing.T) {
	ccx := &mockRoleCCXReader{}
	ccx.On("ReadResource", mock.Anything, mock.Anything).Return(&resource.ReadResult{
		ResourceType: roleType, Properties: rolePropsJSON(t, testRoleName),
	}, nil)

	name := "team+role=app,scope.read@1-x"
	iamc := &mockRoleClient{}
	iamc.On("ListRolePolicies", mock.Anything, matchRoleAndNoMarker(testRoleName)).Return(
		&iam.ListRolePoliciesOutput{PolicyNames: []string{name}}, nil)
	iamc.On("GetRolePolicy", mock.Anything, matchGetRolePolicy(testRoleName, name)).Return(
		&iam.GetRolePolicyOutput{PolicyName: aws.String(name), PolicyDocument: escapedDoc(t, `{}`)}, nil)

	r := newRoleWithMocks(ccx, iamc)
	res, err := r.Read(context.Background(), readReq(testRoleName))

	require.NoError(t, err)
	policies := readEnrichedPolicies(t, res.Properties)
	require.Len(t, policies, 1)
	assert.Equal(t, name, policies[0]["PolicyName"])
}

func TestRole_Read_DecodeErrorNotSwallowed(t *testing.T) {
	ccx := &mockRoleCCXReader{}
	ccx.On("ReadResource", mock.Anything, mock.Anything).Return(&resource.ReadResult{
		ResourceType: roleType, Properties: rolePropsJSON(t, testRoleName),
	}, nil)

	iamc := &mockRoleClient{}
	iamc.On("ListRolePolicies", mock.Anything, matchRoleAndNoMarker(testRoleName)).Return(
		&iam.ListRolePoliciesOutput{PolicyNames: []string{"p"}}, nil)
	// Invalid percent-encoding => url.PathUnescape returns an error.
	iamc.On("GetRolePolicy", mock.Anything, matchGetRolePolicy(testRoleName, "p")).Return(
		&iam.GetRolePolicyOutput{PolicyName: aws.String("p"), PolicyDocument: aws.String("%zz")}, nil)

	r := newRoleWithMocks(ccx, iamc)
	_, err := r.Read(context.Background(), readReq(testRoleName))

	require.Error(t, err)
}

func TestRole_Read_ParseErrorNotSwallowed(t *testing.T) {
	ccx := &mockRoleCCXReader{}
	ccx.On("ReadResource", mock.Anything, mock.Anything).Return(&resource.ReadResult{
		ResourceType: roleType, Properties: rolePropsJSON(t, testRoleName),
	}, nil)

	iamc := &mockRoleClient{}
	iamc.On("ListRolePolicies", mock.Anything, matchRoleAndNoMarker(testRoleName)).Return(
		&iam.ListRolePoliciesOutput{PolicyNames: []string{"p"}}, nil)
	// Valid percent-decoding but not valid JSON => json.Unmarshal fails.
	iamc.On("GetRolePolicy", mock.Anything, matchGetRolePolicy(testRoleName, "p")).Return(
		&iam.GetRolePolicyOutput{PolicyName: aws.String("p"), PolicyDocument: aws.String("not-json")}, nil)

	r := newRoleWithMocks(ccx, iamc)
	_, err := r.Read(context.Background(), readReq(testRoleName))

	require.Error(t, err)
}

func TestRole_Read_NilPolicyDocumentErrors(t *testing.T) {
	ccx := &mockRoleCCXReader{}
	ccx.On("ReadResource", mock.Anything, mock.Anything).Return(&resource.ReadResult{
		ResourceType: roleType, Properties: rolePropsJSON(t, testRoleName),
	}, nil)

	iamc := &mockRoleClient{}
	iamc.On("ListRolePolicies", mock.Anything, matchRoleAndNoMarker(testRoleName)).Return(
		&iam.ListRolePoliciesOutput{PolicyNames: []string{"p"}}, nil)
	iamc.On("GetRolePolicy", mock.Anything, matchGetRolePolicy(testRoleName, "p")).Return(
		&iam.GetRolePolicyOutput{PolicyName: aws.String("p"), PolicyDocument: nil}, nil)

	r := newRoleWithMocks(ccx, iamc)
	_, err := r.Read(context.Background(), readReq(testRoleName))

	require.Error(t, err)
}

func TestRole_Read_ListedPolicyNotRetrievable_IsRaceError(t *testing.T) {
	ccx := &mockRoleCCXReader{}
	ccx.On("ReadResource", mock.Anything, mock.Anything).Return(&resource.ReadResult{
		ResourceType: roleType, Properties: rolePropsJSON(t, testRoleName),
	}, nil)

	iamc := &mockRoleClient{}
	iamc.On("ListRolePolicies", mock.Anything, matchRoleAndNoMarker(testRoleName)).Return(
		&iam.ListRolePoliciesOutput{PolicyNames: []string{"p"}}, nil)
	iamc.On("GetRolePolicy", mock.Anything, matchGetRolePolicy(testRoleName, "p")).Return(
		nil, &iamtypes.NoSuchEntityException{})

	r := newRoleWithMocks(ccx, iamc)
	res, err := r.Read(context.Background(), readReq(testRoleName))

	// A listed-but-unretrievable policy is a mid-read race: surface an error so the
	// caller (StatusResource retryRead / sync) retries, rather than returning a
	// partial actual that would emit a spurious add/remove.
	require.Error(t, err)
	assert.Nil(t, res)
}

func TestRole_Read_RoleLevelNoSuchEntity_IsNotFound(t *testing.T) {
	ccx := &mockRoleCCXReader{}
	ccx.On("ReadResource", mock.Anything, mock.Anything).Return(&resource.ReadResult{
		ResourceType: roleType, Properties: rolePropsJSON(t, testRoleName),
	}, nil)

	iamc := &mockRoleClient{}
	iamc.On("ListRolePolicies", mock.Anything, matchRoleAndNoMarker(testRoleName)).Return(
		nil, &iamtypes.NoSuchEntityException{})

	r := newRoleWithMocks(ccx, iamc)
	res, err := r.Read(context.Background(), readReq(testRoleName))

	require.NoError(t, err)
	assert.Equal(t, resource.OperationErrorCodeNotFound, res.ErrorCode)
}

func TestRole_Read_DuplicatePolicyNameErrors(t *testing.T) {
	ccx := &mockRoleCCXReader{}
	ccx.On("ReadResource", mock.Anything, mock.Anything).Return(&resource.ReadResult{
		ResourceType: roleType, Properties: rolePropsJSON(t, testRoleName),
	}, nil)

	iamc := &mockRoleClient{}
	iamc.On("ListRolePolicies", mock.Anything, matchRoleAndNoMarker(testRoleName)).Return(
		&iam.ListRolePoliciesOutput{PolicyNames: []string{"dup", "dup"}}, nil)
	iamc.On("GetRolePolicy", mock.Anything, matchGetRolePolicy(testRoleName, "dup")).Return(
		&iam.GetRolePolicyOutput{PolicyName: aws.String("dup"), PolicyDocument: escapedDoc(t, `{}`)}, nil).Maybe()

	r := newRoleWithMocks(ccx, iamc)
	_, err := r.Read(context.Background(), readReq(testRoleName))

	require.Error(t, err)
}

func TestRole_Read_RoleNameFromNativeIDFallback(t *testing.T) {
	ccx := &mockRoleCCXReader{}
	// Read-back props lack RoleName; NativeID (= role name for AWS::IAM::Role) is used.
	props, _ := json.Marshal(map[string]any{"Arn": "arn:aws:iam::123456789012:role/" + testRoleName})
	ccx.On("ReadResource", mock.Anything, mock.Anything).Return(&resource.ReadResult{
		ResourceType: roleType, Properties: string(props),
	}, nil)

	iamc := &mockRoleClient{}
	iamc.On("ListRolePolicies", mock.Anything, matchRoleAndNoMarker(testRoleName)).Return(
		&iam.ListRolePoliciesOutput{PolicyNames: []string{}}, nil)

	r := newRoleWithMocks(ccx, iamc)
	res, err := r.Read(context.Background(), readReq(testRoleName))

	require.NoError(t, err)
	require.Empty(t, res.ErrorCode)
	iamc.AssertExpectations(t)
}

func TestRole_Read_RoleNameUnresolvableErrors(t *testing.T) {
	ccx := &mockRoleCCXReader{}
	props, _ := json.Marshal(map[string]any{"Arn": "arn:aws:iam::123456789012:role/x"})
	ccx.On("ReadResource", mock.Anything, mock.Anything).Return(&resource.ReadResult{
		ResourceType: roleType, Properties: string(props),
	}, nil)

	iamc := &mockRoleClient{}
	r := newRoleWithMocks(ccx, iamc)
	_, err := r.Read(context.Background(), &resource.ReadRequest{NativeID: "", ResourceType: roleType})

	require.Error(t, err)
	iamc.AssertNotCalled(t, "ListRolePolicies", mock.Anything, mock.Anything)
}

func TestRole_Read_CCXErrorCodePassthrough(t *testing.T) {
	ccx := &mockRoleCCXReader{}
	ccx.On("ReadResource", mock.Anything, mock.Anything).Return(&resource.ReadResult{
		ResourceType: roleType, ErrorCode: resource.OperationErrorCodeNotFound,
	}, nil)

	iamc := &mockRoleClient{}
	r := newRoleWithMocks(ccx, iamc)
	res, err := r.Read(context.Background(), readReq(testRoleName))

	require.NoError(t, err)
	assert.Equal(t, resource.OperationErrorCodeNotFound, res.ErrorCode)
	iamc.AssertNotCalled(t, "ListRolePolicies", mock.Anything, mock.Anything)
}

func TestRole_Read_CCXReadError(t *testing.T) {
	ccx := &mockRoleCCXReader{}
	ccx.On("ReadResource", mock.Anything, mock.Anything).Return(nil, errors.New("boom"))

	iamc := &mockRoleClient{}
	r := newRoleWithMocks(ccx, iamc)
	_, err := r.Read(context.Background(), readReq(testRoleName))

	require.Error(t, err)
}

func TestRole_IsRegisteredForRead(t *testing.T) {
	if !registry.HasProvisioner(roleType, resource.OperationRead) {
		t.Errorf("expected registry.HasProvisioner(%q, Read) == true; init() did not register the custom Role Read", roleType)
	}
}
