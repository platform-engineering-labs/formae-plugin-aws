// © 2026 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package ccx

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/cloudcontrol"
	cctypes "github.com/aws/aws-sdk-go-v2/service/cloudcontrol/types"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/ptr"
)

func TestReadResource_IAMRoleEquivalentTrustPolicyUsesPriorRepresentation(t *testing.T) {
	// The agent converts stored value/reference envelopes to these plain plugin
	// values before placing the stored row in PriorProperties. The deliberately
	// noncanonical order is the representation Read must retain.
	prior := `{
		"RoleName":"stale-prior-role-name",
		"AssumeRolePolicyDocument":{
			"Version":"2012-10-17",
			"Statement":[
				{
					"Sid":"z",
					"Effect":"Allow",
					"Action":["sts:TagSession","sts:AssumeRole","sts:AssumeRole"],
					"Resource":["arn:aws:iam::222222222222:role/z","arn:aws:iam::111111111111:role/a"],
					"Principal":{
						"AWS":["arn:aws:iam::222222222222:root","arn:aws:iam::111111111111:root","arn:aws:iam::111111111111:root"],
						"Federated":["zzz.example","aaa.example"],
						"Service":["lambda.amazonaws.com","ecs.amazonaws.com"],
						"CanonicalUser":["canonical-z","canonical-a"]
					},
					"Condition":{"StringEquals":{"custom:ordered":["second","first"]}},
					"Unknown":{"Items":["second","first"]}
				},
				{
					"Sid":"a",
					"Effect":"Deny",
					"NotAction":["sts:TagSession","sts:SetSourceIdentity"],
					"NotResource":["arn:aws:iam::222222222222:role/z","arn:aws:iam::111111111111:role/a"],
					"NotPrincipal":{
						"AWS":["arn:aws:iam::222222222222:root","arn:aws:iam::111111111111:root"],
						"Service":["lambda.amazonaws.com","ecs.amazonaws.com"]
					}
				}
			]
		}
	}`
	firstActual := `{
		"RoleName":"actual-role-name",
		"AssumeRolePolicyDocument":{
			"Version":"2012-10-17",
			"Statement":[
				{
					"Sid":"a",
					"Effect":"Deny",
					"NotAction":["sts:SetSourceIdentity","sts:TagSession"],
					"NotResource":["arn:aws:iam::111111111111:role/a","arn:aws:iam::222222222222:role/z"],
					"NotPrincipal":{
						"AWS":["arn:aws:iam::111111111111:root","arn:aws:iam::222222222222:root"],
						"Service":["ecs.amazonaws.com","lambda.amazonaws.com"]
					}
				},
				{
					"Sid":"z",
					"Effect":"Allow",
					"Action":["sts:AssumeRole","sts:AssumeRole","sts:TagSession"],
					"Resource":["arn:aws:iam::111111111111:role/a","arn:aws:iam::222222222222:role/z"],
					"Principal":{
						"AWS":["arn:aws:iam::111111111111:root","arn:aws:iam::111111111111:root","arn:aws:iam::222222222222:root"],
						"Federated":["aaa.example","zzz.example"],
						"Service":["ecs.amazonaws.com","lambda.amazonaws.com"],
						"CanonicalUser":["canonical-a","canonical-z"]
					},
					"Condition":{"StringEquals":{"custom:ordered":["second","first"]}},
					"Unknown":{"Items":["second","first"]}
				}
			]
		}
	}`
	secondActual := `{
		"RoleName":"actual-role-name",
		"AssumeRolePolicyDocument":{
			"Version":"2012-10-17",
			"Statement":[
				{
					"Sid":"z",
					"Effect":"Allow",
					"Action":["sts:AssumeRole","sts:TagSession","sts:AssumeRole"],
					"Resource":["arn:aws:iam::111111111111:role/a","arn:aws:iam::222222222222:role/z"],
					"Principal":{
						"AWS":["arn:aws:iam::111111111111:root","arn:aws:iam::222222222222:root","arn:aws:iam::111111111111:root"],
						"Federated":["aaa.example","zzz.example"],
						"Service":["ecs.amazonaws.com","lambda.amazonaws.com"],
						"CanonicalUser":["canonical-a","canonical-z"]
					},
					"Condition":{"StringEquals":{"custom:ordered":["second","first"]}},
					"Unknown":{"Items":["second","first"]}
				},
				{
					"Sid":"a",
					"Effect":"Deny",
					"NotAction":["sts:SetSourceIdentity","sts:TagSession"],
					"NotResource":["arn:aws:iam::111111111111:role/a","arn:aws:iam::222222222222:role/z"],
					"NotPrincipal":{
						"AWS":["arn:aws:iam::111111111111:root","arn:aws:iam::222222222222:root"],
						"Service":["ecs.amazonaws.com","lambda.amazonaws.com"]
					}
				}
			]
		}
	}`
	want := `{
		"RoleName":"actual-role-name",
		"AssumeRolePolicyDocument":{
			"Version":"2012-10-17",
			"Statement":[
				{
					"Sid":"z",
					"Effect":"Allow",
					"Action":["sts:TagSession","sts:AssumeRole","sts:AssumeRole"],
					"Resource":["arn:aws:iam::222222222222:role/z","arn:aws:iam::111111111111:role/a"],
					"Principal":{
						"AWS":["arn:aws:iam::222222222222:root","arn:aws:iam::111111111111:root","arn:aws:iam::111111111111:root"],
						"Federated":["zzz.example","aaa.example"],
						"Service":["lambda.amazonaws.com","ecs.amazonaws.com"],
						"CanonicalUser":["canonical-z","canonical-a"]
					},
					"Condition":{"StringEquals":{"custom:ordered":["second","first"]}},
					"Unknown":{"Items":["second","first"]}
				},
				{
					"Sid":"a",
					"Effect":"Deny",
					"NotAction":["sts:TagSession","sts:SetSourceIdentity"],
					"NotResource":["arn:aws:iam::222222222222:role/z","arn:aws:iam::111111111111:role/a"],
					"NotPrincipal":{
						"AWS":["arn:aws:iam::222222222222:root","arn:aws:iam::111111111111:root"],
						"Service":["lambda.amazonaws.com","ecs.amazonaws.com"]
					}
				}
			]
		}
	}`

	observations := readResourceProperties(t, "AWS::IAM::Role", prior, firstActual, secondActual)
	require.JSONEq(t, want, observations[0])
	require.JSONEq(t, want, observations[1])
	require.Equal(t, observations[0], observations[1],
		"provider permutations must stabilize on the existing noncanonical representation")

	properties := decodeObject(t, observations[0])
	require.Equal(t, "actual-role-name", properties["RoleName"],
		"only verified policy ordering may come from prior properties")
	statements := properties["AssumeRolePolicyDocument"].(map[string]any)["Statement"].([]any)
	allow := statementBySid(t, statements, "z")
	require.Equal(t, []any{"sts:TagSession", "sts:AssumeRole", "sts:AssumeRole"}, allow["Action"],
		"prior ordering and duplicate multiplicity must be retained")
}

func TestReadResource_IAMRoleMatchesDuplicateStatementsWithoutDroppingThem(t *testing.T) {
	prior := `{"AssumeRolePolicyDocument":{"Statement":[{"Effect":"Allow","Action":["b","a"],"Principal":{"Service":["z","a"]}},{"Effect":"Allow","Action":["a","b"],"Principal":{"Service":["a","z"]}}]}}`
	actual := `{"AssumeRolePolicyDocument":{"Statement":[{"Effect":"Allow","Action":["a","b"],"Principal":{"Service":["z","a"]}},{"Effect":"Allow","Action":["b","a"],"Principal":{"Service":["a","z"]}}]}}`
	want := `{"AssumeRolePolicyDocument":{"Statement":[{"Effect":"Allow","Action":["b","a"],"Principal":{"Service":["z","a"]}},{"Effect":"Allow","Action":["a","b"],"Principal":{"Service":["a","z"]}}]}}`

	observation := readResourceProperties(t, "AWS::IAM::Role", prior, actual)[0]
	require.JSONEq(t, want, observation)
	statements := decodeObject(t, observation)["AssumeRolePolicyDocument"].(map[string]any)["Statement"].([]any)
	require.Len(t, statements, 2)
}

func TestReadResource_IAMRoleEquivalentSingletonStatementUsesPriorChildOrder(t *testing.T) {
	prior := `{"AssumeRolePolicyDocument":{"Statement":{"Effect":"Allow","Action":["sts:TagSession","sts:AssumeRole","sts:AssumeRole"],"Resource":"*","Principal":{"AWS":"arn:aws:iam::111111111111:root"},"Condition":{"ForAnyValue:StringEquals":{"custom:ordered":["second","first"]}},"Unknown":{"Items":["second","first"]}}}}`
	actual := `{"AssumeRolePolicyDocument":{"Statement":{"Effect":"Allow","Action":["sts:AssumeRole","sts:TagSession","sts:AssumeRole"],"Resource":"*","Principal":{"AWS":"arn:aws:iam::111111111111:root"},"Condition":{"ForAnyValue:StringEquals":{"custom:ordered":["second","first"]}},"Unknown":{"Items":["second","first"]}}}}`

	observation := readResourceProperties(t, "AWS::IAM::Role", prior, actual)[0]
	require.JSONEq(t, prior, observation)
	statement := decodeObject(t, observation)["AssumeRolePolicyDocument"].(map[string]any)["Statement"]
	require.IsType(t, map[string]any{}, statement, "a singleton Statement object must remain an object")
}

func TestReadResource_IAMRoleSemanticChangesReturnActualUnchanged(t *testing.T) {
	prior := `{"RoleName":"example","AssumeRolePolicyDocument":{"Statement":[{"Effect":"Allow","Action":["sts:TagSession","sts:AssumeRole","sts:AssumeRole"],"Principal":{"Service":["lambda.amazonaws.com","ecs.amazonaws.com"]},"Condition":{"StringEquals":{"custom:ordered":["first","second"]}},"Unknown":{"Items":["first","second"]}}]}}`
	tests := map[string]string{
		"permission":        `{"RoleName":"example","AssumeRolePolicyDocument":{"Statement":[{"Effect":"Allow","Action":["sts:SetSourceIdentity","sts:AssumeRole","sts:AssumeRole"],"Principal":{"Service":["ecs.amazonaws.com","lambda.amazonaws.com"]},"Condition":{"StringEquals":{"custom:ordered":["first","second"]}},"Unknown":{"Items":["first","second"]}}]}}`,
		"principal":         `{"RoleName":"example","AssumeRolePolicyDocument":{"Statement":[{"Effect":"Allow","Action":["sts:AssumeRole","sts:TagSession","sts:AssumeRole"],"Principal":{"Service":["states.amazonaws.com","ecs.amazonaws.com"]},"Condition":{"StringEquals":{"custom:ordered":["first","second"]}},"Unknown":{"Items":["first","second"]}}]}}`,
		"effect":            `{"RoleName":"example","AssumeRolePolicyDocument":{"Statement":[{"Effect":"Deny","Action":["sts:AssumeRole","sts:TagSession","sts:AssumeRole"],"Principal":{"Service":["ecs.amazonaws.com","lambda.amazonaws.com"]},"Condition":{"StringEquals":{"custom:ordered":["first","second"]}},"Unknown":{"Items":["first","second"]}}]}}`,
		"condition value":   `{"RoleName":"example","AssumeRolePolicyDocument":{"Statement":[{"Effect":"Allow","Action":["sts:AssumeRole","sts:TagSession","sts:AssumeRole"],"Principal":{"Service":["ecs.amazonaws.com","lambda.amazonaws.com"]},"Condition":{"StringEquals":{"custom:ordered":["first","third"]}},"Unknown":{"Items":["first","second"]}}]}}`,
		"condition order":   `{"RoleName":"example","AssumeRolePolicyDocument":{"Statement":[{"Effect":"Allow","Action":["sts:AssumeRole","sts:TagSession","sts:AssumeRole"],"Principal":{"Service":["ecs.amazonaws.com","lambda.amazonaws.com"]},"Condition":{"StringEquals":{"custom:ordered":["second","first"]}},"Unknown":{"Items":["first","second"]}}]}}`,
		"unknown order":     `{"RoleName":"example","AssumeRolePolicyDocument":{"Statement":[{"Effect":"Allow","Action":["sts:AssumeRole","sts:TagSession","sts:AssumeRole"],"Principal":{"Service":["ecs.amazonaws.com","lambda.amazonaws.com"]},"Condition":{"StringEquals":{"custom:ordered":["first","second"]}},"Unknown":{"Items":["second","first"]}}]}}`,
		"duplicate count":   `{"RoleName":"example","AssumeRolePolicyDocument":{"Statement":[{"Effect":"Allow","Action":["sts:AssumeRole","sts:TagSession"],"Principal":{"Service":["ecs.amazonaws.com","lambda.amazonaws.com"]},"Condition":{"StringEquals":{"custom:ordered":["first","second"]}},"Unknown":{"Items":["first","second"]}}]}}`,
		"scalar/list shape": `{"RoleName":"example","AssumeRolePolicyDocument":{"Statement":[{"Effect":"Allow","Action":"sts:AssumeRole","Principal":{"Service":["ecs.amazonaws.com","lambda.amazonaws.com"]},"Condition":{"StringEquals":{"custom:ordered":["first","second"]}},"Unknown":{"Items":["first","second"]}}]}}`,
		"extra actual key":  `{"RoleName":"example","AssumeRolePolicyDocument":{"Statement":[{"Effect":"Allow","Action":["sts:AssumeRole","sts:TagSession","sts:AssumeRole"],"Principal":{"Service":["ecs.amazonaws.com","lambda.amazonaws.com"]},"Condition":{"StringEquals":{"custom:ordered":["first","second"]}},"Unknown":{"Items":["first","second"]}}],"ProviderVersion":"actual"}}`,
	}

	for name, actual := range tests {
		t.Run(name, func(t *testing.T) {
			observation := readResourceProperties(t, "AWS::IAM::Role", prior, actual)[0]
			require.JSONEq(t, actual, observation,
				"a semantic or shape change must return the actual provider representation")
		})
	}
}

func TestReadResource_IAMRolePriorWithStrippedNestedEmptyCollectionReturnsActualUnchanged(t *testing.T) {
	tests := map[string]struct {
		prior  string
		actual string
	}{
		"empty object": {
			prior:  `{"AssumeRolePolicyDocument":{"Statement":[{"Sid":"a","Effect":"Allow","Action":["a","z"]},{"Sid":"z","Effect":"Deny"}]}}`,
			actual: `{"AssumeRolePolicyDocument":{"Statement":[{"Sid":"z","Effect":"Deny"},{"Sid":"a","Effect":"Allow","Action":["z","a"],"Condition":{"StringEquals":{}}}]}}`,
		},
		"empty list": {
			prior:  `{"AssumeRolePolicyDocument":{"Statement":[{"Sid":"a","Effect":"Allow","Action":["a","z"]},{"Sid":"z","Effect":"Deny"}]}}`,
			actual: `{"AssumeRolePolicyDocument":{"Statement":[{"Sid":"z","Effect":"Deny"},{"Sid":"a","Effect":"Allow","Action":["z","a"],"Unknown":[]}]}}`,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			observation := readResourceProperties(t, "AWS::IAM::Role", tc.prior, tc.actual)[0]
			require.JSONEq(t, tc.actual, observation,
				"a collection stripped from prior is a shape difference, not permission to reorder actual")
		})
	}
}

func TestReadResource_IAMRoleDistinctLargeConditionNumbersReturnActualUnchanged(t *testing.T) {
	prior := `{"AssumeRolePolicyDocument":{"Statement":{"Effect":"Allow","Action":["a","z"],"Condition":{"NumericEquals":{"custom:value":9007199254740992}}}}}`
	actual := `{"AssumeRolePolicyDocument":{"Statement":{"Effect":"Allow","Action":["z","a"],"Condition":{"NumericEquals":{"custom:value":9007199254740993}}}}}`

	observation := readResourceProperties(t, "AWS::IAM::Role", prior, actual)[0]
	require.JSONEq(t, actual, observation)
	require.Contains(t, observation, `9007199254740993`,
		"equivalence checking and re-marshalling must not round a large actual condition integer")
}

func TestReadResource_IAMRoleUnusablePriorReturnsActualUnchanged(t *testing.T) {
	actual := `{"RoleName":"example","AssumeRolePolicyDocument":{"Statement":[{"Sid":"z","Effect":"Allow","Action":["sts:TagSession","sts:AssumeRole"]},{"Sid":"a","Effect":"Deny","Principal":{"Service":["lambda.amazonaws.com","ecs.amazonaws.com"]}}]}}`
	tests := map[string]string{
		"missing":               "",
		"invalid JSON":          `{`,
		"missing policy":        `{"RoleName":"example"}`,
		"mixed action array":    `{"AssumeRolePolicyDocument":{"Statement":[{"Sid":"z","Effect":"Allow","Action":["sts:TagSession",7,"sts:AssumeRole"]},{"Sid":"a","Effect":"Deny","Principal":{"Service":["lambda.amazonaws.com","ecs.amazonaws.com"]}}]}}`,
		"mixed principal array": `{"AssumeRolePolicyDocument":{"Statement":[{"Sid":"z","Effect":"Allow","Action":["sts:TagSession","sts:AssumeRole"]},{"Sid":"a","Effect":"Deny","Principal":{"Service":["lambda.amazonaws.com",7,"ecs.amazonaws.com"]}}]}}`,
		"mixed statement array": `{"AssumeRolePolicyDocument":{"Statement":[{"Sid":"z","Effect":"Allow","Action":["sts:TagSession","sts:AssumeRole"]},"unresolved",{"Sid":"a","Effect":"Deny","Principal":{"Service":["lambda.amazonaws.com","ecs.amazonaws.com"]}}]}}`,
		"unresolved value":      `{"AssumeRolePolicyDocument":{"Statement":[{"Sid":"z","Effect":"Allow","Action":[{"$ref":"formae://unresolved#/Action"},"sts:AssumeRole"]},{"Sid":"a","Effect":"Deny","Principal":{"Service":["lambda.amazonaws.com","ecs.amazonaws.com"]}}]}}`,
	}

	for name, prior := range tests {
		t.Run(name, func(t *testing.T) {
			observation := readResourceProperties(t, "AWS::IAM::Role", prior, actual)[0]
			require.JSONEq(t, actual, observation)
		})
	}
}

func TestReadResource_DoesNotStabilizeTrustPolicyShapedPropertiesOnOtherTypes(t *testing.T) {
	prior := `{"Name":"prior","AssumeRolePolicyDocument":{"Statement":[{"Sid":"a","Action":["a","z"]},{"Sid":"z","Action":["a"]}]}}`
	actual := `{"Name":"actual","AssumeRolePolicyDocument":{"Statement":[{"Sid":"z","Action":["z","a"]},{"Sid":"a","Action":["a"]}]}}`
	observation := readResourceProperties(t, "AWS::Example::Unrelated", prior, actual)[0]
	require.JSONEq(t, actual, observation)
}

func TestCreateResource_IAMRoleNoPriorReturnsProviderRepresentation(t *testing.T) {
	actual := `{"RoleName":"example","AssumeRolePolicyDocument":{"Statement":[{"Sid":"z","Action":["z","a"]},{"Sid":"a","Action":["a"]}]}}`
	mockAPI := new(mockCloudControlAPI)
	client := &Client{api: mockAPI}
	mockAPI.On("CreateResource", mock.Anything, mock.Anything).Return(&cloudcontrol.CreateResourceOutput{
		ProgressEvent: &cctypes.ProgressEvent{
			OperationStatus: cctypes.OperationStatusSuccess,
			Identifier:      ptr.Of("example"),
			RequestToken:    ptr.Of("request-token"),
		},
	}, nil)
	mockAPI.On("GetResource", mock.Anything, mock.Anything).
		Return(readOutput("AWS::IAM::Role", "example", actual), nil)

	result, err := client.CreateResource(context.Background(), &resource.CreateRequest{
		ResourceType: "AWS::IAM::Role",
		Properties:   json.RawMessage(`{"RoleName":"example"}`),
	})
	require.NoError(t, err)
	require.JSONEq(t, actual, string(result.ProgressResult.ResourceProperties))
}

func TestStatusResource_IAMRoleNoPriorLeavesProviderPermutationsUnchanged(t *testing.T) {
	first := `{"RoleName":"example","AssumeRolePolicyDocument":{"Statement":[{"Sid":"z","Effect":"Allow","Action":["sts:TagSession","sts:AssumeRole"]},{"Sid":"a","Effect":"Allow","Principal":{"Service":["lambda.amazonaws.com","ecs.amazonaws.com"]},"Action":"sts:AssumeRole"}]}}`
	second := `{"RoleName":"example","AssumeRolePolicyDocument":{"Statement":[{"Sid":"a","Effect":"Allow","Principal":{"Service":["ecs.amazonaws.com","lambda.amazonaws.com"]},"Action":"sts:AssumeRole"},{"Sid":"z","Effect":"Allow","Action":["sts:AssumeRole","sts:TagSession"]}]}}`

	mockAPI := new(mockCloudControlAPI)
	client := &Client{api: mockAPI, now: time.Now}
	for _, properties := range []string{first, second} {
		mockAPI.On("GetResourceRequestStatus", mock.Anything, mock.Anything).Return(&cloudcontrol.GetResourceRequestStatusOutput{
			ProgressEvent: &cctypes.ProgressEvent{
				Operation:       cctypes.OperationUpdate,
				OperationStatus: cctypes.OperationStatusSuccess,
				Identifier:      ptr.Of("example"),
				RequestToken:    ptr.Of("request-token"),
				TypeName:        ptr.Of("AWS::IAM::Role"),
			},
		}, nil).Once()
		mockAPI.On("GetResource", mock.Anything, mock.Anything).
			Return(readOutput("AWS::IAM::Role", "example", properties), nil).
			Once()
	}

	var observations []string
	for range 2 {
		result, err := client.StatusResource(context.Background(), &resource.StatusRequest{
			RequestID: "request-token",
			NativeID:  "example",
		}, client.ReadResource)
		require.NoError(t, err)
		require.NotNil(t, result.ProgressResult.ResourceProperties)
		observations = append(observations, string(result.ProgressResult.ResourceProperties))
	}

	require.JSONEq(t, first, observations[0])
	require.JSONEq(t, second, observations[1])
	require.NotEqual(t, observations[0], observations[1],
		"status read-back has no prior representation and must keep existing provider behavior")
}

func readResourceProperties(t *testing.T, resourceType, priorProperties string, properties ...string) []string {
	t.Helper()
	mockAPI := new(mockCloudControlAPI)
	client := &Client{api: mockAPI}
	for _, propertyDocument := range properties {
		mockAPI.On("GetResource", mock.Anything, mock.Anything).
			Return(readOutput(resourceType, "example", propertyDocument), nil).
			Once()
	}

	observations := make([]string, 0, len(properties))
	for range properties {
		result, err := client.ReadResource(context.Background(), &resource.ReadRequest{
			ResourceType:    resourceType,
			NativeID:        "example",
			PriorProperties: json.RawMessage(priorProperties),
		})
		require.NoError(t, err)
		observations = append(observations, result.Properties)
	}
	return observations
}

func readOutput(resourceType, identifier, properties string) *cloudcontrol.GetResourceOutput {
	return &cloudcontrol.GetResourceOutput{
		ResourceDescription: &cctypes.ResourceDescription{
			Identifier: ptr.Of(identifier),
			Properties: ptr.Of(properties),
		},
		TypeName: ptr.Of(resourceType),
	}
}

func decodeObject(t *testing.T, document string) map[string]any {
	t.Helper()
	var object map[string]any
	require.NoError(t, json.Unmarshal([]byte(document), &object))
	return object
}

func statementBySid(t *testing.T, statements []any, sid string) map[string]any {
	t.Helper()
	for _, value := range statements {
		statement, ok := value.(map[string]any)
		if ok && statement["Sid"] == sid {
			return statement
		}
	}
	t.Fatalf("statement with Sid %q not found", sid)
	return nil
}
