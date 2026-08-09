// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package servicediscovery

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	servicediscoverysdk "github.com/aws/aws-sdk-go-v2/service/servicediscovery"
	servicediscoverytypes "github.com/aws/aws-sdk-go-v2/service/servicediscovery/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/config"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
)

var testNow = time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)

func newTestNamespace(client serviceDiscoveryClientInterface) *PrivateDnsNamespace {
	return &PrivateDnsNamespace{
		cfg: &config.Config{Region: "us-east-1"},
		clientFactory: func(*config.Config) (serviceDiscoveryClientInterface, error) {
			return client, nil
		},
		namespaceTargetPollInterval: time.Microsecond,
		namespaceTargetPollAttempts: 3,
		now:                         func() time.Time { return testNow },
	}
}

func createRequest(label string, properties map[string]any) *resource.CreateRequest {
	raw, err := json.Marshal(properties)
	if err != nil {
		panic(err)
	}
	return &resource.CreateRequest{
		ResourceType: resourceType,
		Label:        label,
		Properties:   raw,
	}
}

func fullProperties() map[string]any {
	return map[string]any{
		"Name":        "example.internal",
		"Vpc":         "vpc-0123456789abcdef0",
		"Description": "namespace for the example service",
		"Properties": map[string]any{
			"DnsProperties": map[string]any{
				"SOA": map[string]any{"TTL": 60},
			},
		},
		"Tags": []any{
			map[string]any{"Key": "Name", "Value": "example"},
		},
	}
}

func operationOutput(status servicediscoverytypes.OperationStatus, targets map[string]string) *servicediscoverysdk.GetOperationOutput {
	return &servicediscoverysdk.GetOperationOutput{
		Operation: &servicediscoverytypes.Operation{
			Id:      aws.String("op-1"),
			Status:  status,
			Targets: targets,
		},
	}
}

// capturedInput returns the input the named call was made with.
func capturedInput[T any](t *testing.T, client *mockServiceDiscoveryClient, method string) T {
	t.Helper()
	for _, call := range client.Calls {
		if call.Method == method {
			return call.Arguments.Get(1).(T)
		}
	}
	t.Fatalf("%s was never called", method)
	var zero T
	return zero
}

// callOrder reports the calls made on the client, in the order they were made.
func callOrder(client *mockServiceDiscoveryClient) []string {
	methods := make([]string, 0, len(client.Calls))
	for _, call := range client.Calls {
		methods = append(methods, call.Method)
	}
	return methods
}

func capturedCreateInput(t *testing.T, client *mockServiceDiscoveryClient) *servicediscoverysdk.CreatePrivateDnsNamespaceInput {
	t.Helper()
	return capturedInput[*servicediscoverysdk.CreatePrivateDnsNamespaceInput](t, client, "CreatePrivateDnsNamespace")
}

func capturedUpdateInput(t *testing.T, client *mockServiceDiscoveryClient) *servicediscoverysdk.UpdatePrivateDnsNamespaceInput {
	t.Helper()
	return capturedInput[*servicediscoverysdk.UpdatePrivateDnsNamespaceInput](t, client, "UpdatePrivateDnsNamespace")
}

func capturedDeleteInput(t *testing.T, client *mockServiceDiscoveryClient) *servicediscoverysdk.DeleteNamespaceInput {
	t.Helper()
	return capturedInput[*servicediscoverysdk.DeleteNamespaceInput](t, client, "DeleteNamespace")
}

func capturedTagInput(t *testing.T, client *mockServiceDiscoveryClient) *servicediscoverysdk.TagResourceInput {
	t.Helper()
	return capturedInput[*servicediscoverysdk.TagResourceInput](t, client, "TagResource")
}

func capturedUntagInput(t *testing.T, client *mockServiceDiscoveryClient) *servicediscoverysdk.UntagResourceInput {
	t.Helper()
	return capturedInput[*servicediscoverysdk.UntagResourceInput](t, client, "UntagResource")
}

func capturedListInput(t *testing.T, client *mockServiceDiscoveryClient) *servicediscoverysdk.ListNamespacesInput {
	t.Helper()
	return capturedInput[*servicediscoverysdk.ListNamespacesInput](t, client, "ListNamespaces")
}

func capturedListOperationsInput(t *testing.T, client *mockServiceDiscoveryClient) *servicediscoverysdk.ListOperationsInput {
	t.Helper()
	return capturedInput[*servicediscoverysdk.ListOperationsInput](t, client, "ListOperations")
}

func capturedListTagsInput(t *testing.T, client *mockServiceDiscoveryClient) *servicediscoverysdk.ListTagsForResourceInput {
	t.Helper()
	return capturedInput[*servicediscoverysdk.ListTagsForResourceInput](t, client, "ListTagsForResource")
}

const namespaceARN = "arn:aws:servicediscovery:us-east-1:123456789012:namespace/ns-abc"

func fullNamespace() *servicediscoverytypes.Namespace {
	return &servicediscoverytypes.Namespace{
		Id:          aws.String("ns-abc"),
		Arn:         aws.String(namespaceARN),
		Name:        aws.String("example.internal"),
		Description: aws.String("namespace for the example service"),
		Type:        servicediscoverytypes.NamespaceTypeDnsPrivate,
		Properties: &servicediscoverytypes.NamespaceProperties{
			DnsProperties: &servicediscoverytypes.DnsProperties{
				HostedZoneId: aws.String("Z0123456789ABCDEFGHIJ"),
				SOA:          &servicediscoverytypes.SOA{TTL: aws.Int64(60)},
			},
		},
	}
}

// namespaceReader answers a read of the full namespace, tagged with a single tag.
func namespaceReader() *mockServiceDiscoveryClient {
	client := &mockServiceDiscoveryClient{}
	client.On("GetNamespace", mock.Anything, &servicediscoverysdk.GetNamespaceInput{Id: aws.String("ns-abc")}).
		Return(&servicediscoverysdk.GetNamespaceOutput{Namespace: fullNamespace()}, nil)
	client.On("ListTagsForResource", mock.Anything, mock.Anything).
		Return(&servicediscoverysdk.ListTagsForResourceOutput{
			Tags: []servicediscoverytypes.Tag{{Key: aws.String("Name"), Value: aws.String("example")}},
		}, nil)
	return client
}

func decodedProperties(t *testing.T, raw string) map[string]any {
	t.Helper()
	var properties map[string]any
	require.NoError(t, json.Unmarshal([]byte(raw), &properties))
	return properties
}

func readNamespace(t *testing.T, client serviceDiscoveryClientInterface) map[string]any {
	t.Helper()
	result, err := newTestNamespace(client).Read(context.Background(), &resource.ReadRequest{
		NativeID:     "ns-abc",
		ResourceType: resourceType,
	})
	require.NoError(t, err)
	require.Empty(t, result.ErrorCode)
	return decodedProperties(t, result.Properties)
}

func TestCreateSendsDeclaredPropertiesToCloudMap(t *testing.T) {
	client := &mockServiceDiscoveryClient{}
	client.On("CreatePrivateDnsNamespace", mock.Anything, mock.Anything).
		Return(&servicediscoverysdk.CreatePrivateDnsNamespaceOutput{OperationId: aws.String("op-1")}, nil)
	client.On("GetOperation", mock.Anything, mock.Anything).
		Return(operationOutput(servicediscoverytypes.OperationStatusPending, map[string]string{"NAMESPACE": "ns-abc"}), nil)

	_, err := newTestNamespace(client).Create(context.Background(), createRequest("example", fullProperties()))
	require.NoError(t, err)

	input := capturedCreateInput(t, client)
	assert.Equal(t, "example.internal", aws.ToString(input.Name))
	assert.Equal(t, "vpc-0123456789abcdef0", aws.ToString(input.Vpc))
	assert.Equal(t, "namespace for the example service", aws.ToString(input.Description))
	require.NotNil(t, input.Properties)
	require.NotNil(t, input.Properties.DnsProperties)
	require.NotNil(t, input.Properties.DnsProperties.SOA)
	assert.Equal(t, int64(60), aws.ToInt64(input.Properties.DnsProperties.SOA.TTL))
	require.Len(t, input.Tags, 1)
	assert.Equal(t, "Name", aws.ToString(input.Tags[0].Key))
	assert.Equal(t, "example", aws.ToString(input.Tags[0].Value))
}

// A declared property of the wrong type would otherwise be dropped from the
// create, leaving the namespace on Cloud Map's default and reading back as
// provider state rather than as the declaration.
func TestCreateRejectsDeclaredPropertiesOfTheWrongType(t *testing.T) {
	for name, mutate := range map[string]func(properties map[string]any){
		"Description": func(properties map[string]any) {
			properties["Description"] = 42
		},
		"SOA.TTL": func(properties map[string]any) {
			properties["Properties"] = map[string]any{
				"DnsProperties": map[string]any{"SOA": map[string]any{"TTL": "60"}},
			}
		},
		"DnsProperties": func(properties map[string]any) {
			properties["Properties"] = map[string]any{"DnsProperties": "60"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			client := &mockServiceDiscoveryClient{}
			properties := fullProperties()
			mutate(properties)

			result, err := newTestNamespace(client).Create(context.Background(), createRequest("example", properties))
			require.Error(t, err)
			assert.Nil(t, result)
			client.AssertNotCalled(t, "CreatePrivateDnsNamespace", mock.Anything, mock.Anything)
		})
	}
}

// An absent optional property is not an error: the namespace takes Cloud Map's
// own default for it.
func TestCreateOmitsAbsentOptionalProperties(t *testing.T) {
	client := &mockServiceDiscoveryClient{}
	client.On("CreatePrivateDnsNamespace", mock.Anything, mock.Anything).
		Return(&servicediscoverysdk.CreatePrivateDnsNamespaceOutput{OperationId: aws.String("op-1")}, nil)
	client.On("GetOperation", mock.Anything, mock.Anything).
		Return(operationOutput(servicediscoverytypes.OperationStatusPending, map[string]string{"NAMESPACE": "ns-abc"}), nil)

	_, err := newTestNamespace(client).Create(context.Background(), createRequest("example", map[string]any{
		"Name": "example.internal",
		"Vpc":  "vpc-0123456789abcdef0",
	}))
	require.NoError(t, err)

	input := capturedCreateInput(t, client)
	assert.Nil(t, input.Description)
	assert.Nil(t, input.Properties)
	assert.Nil(t, input.Tags)
}

func TestCreateReturnsNamespaceIDFromOperationTarget(t *testing.T) {
	client := &mockServiceDiscoveryClient{}
	client.On("CreatePrivateDnsNamespace", mock.Anything, mock.Anything).
		Return(&servicediscoverysdk.CreatePrivateDnsNamespaceOutput{OperationId: aws.String("op-1")}, nil)
	client.On("GetOperation", mock.Anything, mock.Anything).
		Return(operationOutput(servicediscoverytypes.OperationStatusPending, map[string]string{"NAMESPACE": "ns-abc"}), nil)

	result, err := newTestNamespace(client).Create(context.Background(), createRequest("example", fullProperties()))
	require.NoError(t, err)

	require.NotNil(t, result.ProgressResult)
	assert.Equal(t, resource.OperationCreate, result.ProgressResult.Operation)
	assert.Equal(t, resource.OperationStatusInProgress, result.ProgressResult.OperationStatus)
	assert.Equal(t, "ns-abc", result.ProgressResult.NativeID)

	state, err := decodeRequestID(result.ProgressResult.RequestID)
	require.NoError(t, err)
	assert.Equal(t, "op-1", state.OperationID)
	assert.Equal(t, testNow.Add(namespaceOperationTimeout), state.Deadline)
}

// The namespace record exists before the operation reports a NAMESPACE target,
// so a create whose operation never reports one resolves the namespace by name
// rather than failing.
func TestCreateResolvesNamespaceByNameWhenOperationReportsNoNamespaceTarget(t *testing.T) {
	client := &mockServiceDiscoveryClient{}
	client.On("CreatePrivateDnsNamespace", mock.Anything, mock.Anything).
		Return(&servicediscoverysdk.CreatePrivateDnsNamespaceOutput{OperationId: aws.String("op-1")}, nil)
	client.On("GetOperation", mock.Anything, mock.Anything).
		Return(operationOutput(servicediscoverytypes.OperationStatusPending, nil), nil)
	client.On("ListNamespaces", mock.Anything, mock.Anything).
		Return(&servicediscoverysdk.ListNamespacesOutput{
			Namespaces: []servicediscoverytypes.NamespaceSummary{
				{Id: aws.String("ns-other"), Name: aws.String("other.internal")},
				{Id: aws.String("ns-byname"), Name: aws.String("example.internal")},
			},
		}, nil)

	result, err := newTestNamespace(client).Create(context.Background(), createRequest("example", fullProperties()))
	require.NoError(t, err)

	assert.Equal(t, "ns-byname", result.ProgressResult.NativeID)
	client.AssertNumberOfCalls(t, "GetOperation", 3)

	// The operation is still the one to poll for completion.
	state, err := decodeRequestID(result.ProgressResult.RequestID)
	require.NoError(t, err)
	assert.Equal(t, "op-1", state.OperationID)
}

// With neither an operation target nor a namespace of that name there is no
// NativeID to return, and the create must fail rather than hand the engine an
// empty one.
func TestCreateFailsWhenNeitherTheOperationTargetNorTheNameResolves(t *testing.T) {
	client := &mockServiceDiscoveryClient{}
	client.On("CreatePrivateDnsNamespace", mock.Anything, mock.Anything).
		Return(&servicediscoverysdk.CreatePrivateDnsNamespaceOutput{OperationId: aws.String("op-1")}, nil)
	client.On("GetOperation", mock.Anything, mock.Anything).
		Return(operationOutput(servicediscoverytypes.OperationStatusPending, nil), nil)
	client.On("ListNamespaces", mock.Anything, mock.Anything).
		Return(&servicediscoverysdk.ListNamespacesOutput{}, nil)

	result, err := newTestNamespace(client).Create(context.Background(), createRequest("example", fullProperties()))
	require.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "NAMESPACE")
	assert.Contains(t, err.Error(), "example.internal")
	client.AssertNumberOfCalls(t, "GetOperation", 3)
}

func TestCreateFailsWhenTheCreateOperationFails(t *testing.T) {
	client := &mockServiceDiscoveryClient{}
	client.On("CreatePrivateDnsNamespace", mock.Anything, mock.Anything).
		Return(&servicediscoverysdk.CreatePrivateDnsNamespaceOutput{OperationId: aws.String("op-1")}, nil)
	failed := operationOutput(servicediscoverytypes.OperationStatusFail, nil)
	failed.Operation.ErrorMessage = aws.String("hosted zone quota exceeded")
	client.On("GetOperation", mock.Anything, mock.Anything).Return(failed, nil)

	_, err := newTestNamespace(client).Create(context.Background(), createRequest("example", fullProperties()))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "hosted zone quota exceeded")
	client.AssertNumberOfCalls(t, "GetOperation", 1)
}

func TestCreateFailsWhenTheCreateOperationIsNotFound(t *testing.T) {
	client := &mockServiceDiscoveryClient{}
	client.On("CreatePrivateDnsNamespace", mock.Anything, mock.Anything).
		Return(&servicediscoverysdk.CreatePrivateDnsNamespaceOutput{OperationId: aws.String("op-1")}, nil)
	client.On("GetOperation", mock.Anything, mock.Anything).
		Return(nil, &servicediscoverytypes.OperationNotFound{Message: aws.String("op-1 not found")})

	_, err := newTestNamespace(client).Create(context.Background(), createRequest("example", fullProperties()))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "op-1")
}

// A replayed create is deduplicated by AWS only when it carries the same
// CreatorRequestId, so the id has to be derived from the resource's identity
// rather than generated per call.
func TestCreateDerivesAStableCreatorRequestID(t *testing.T) {
	newClient := func() *mockServiceDiscoveryClient {
		client := &mockServiceDiscoveryClient{}
		client.On("CreatePrivateDnsNamespace", mock.Anything, mock.Anything).
			Return(&servicediscoverysdk.CreatePrivateDnsNamespaceOutput{OperationId: aws.String("op-1")}, nil)
		client.On("GetOperation", mock.Anything, mock.Anything).
			Return(operationOutput(servicediscoverytypes.OperationStatusPending, map[string]string{"NAMESPACE": "ns-abc"}), nil)
		return client
	}

	first, second, other := newClient(), newClient(), newClient()

	_, err := newTestNamespace(first).Create(context.Background(), createRequest("example", fullProperties()))
	require.NoError(t, err)
	_, err = newTestNamespace(second).Create(context.Background(), createRequest("example", fullProperties()))
	require.NoError(t, err)
	_, err = newTestNamespace(other).Create(context.Background(), createRequest("other", fullProperties()))
	require.NoError(t, err)

	firstID := aws.ToString(capturedCreateInput(t, first).CreatorRequestId)
	assert.NotEmpty(t, firstID)
	assert.LessOrEqual(t, len(firstID), 64)
	assert.Equal(t, firstID, aws.ToString(capturedCreateInput(t, second).CreatorRequestId))
	assert.NotEqual(t, firstID, aws.ToString(capturedCreateInput(t, other).CreatorRequestId))
}

// A deduplicated create loses the response that carried the operation id, so the
// namespace id is recovered from the operation the duplicate error points at.
func TestCreateRecoversNamespaceIDFromDuplicateOperationID(t *testing.T) {
	client := &mockServiceDiscoveryClient{}
	client.On("CreatePrivateDnsNamespace", mock.Anything, mock.Anything).
		Return(nil, &servicediscoverytypes.DuplicateRequest{
			Message:              aws.String("operation already in progress"),
			DuplicateOperationId: aws.String("op-dup"),
		})
	client.On("GetOperation", mock.Anything, &servicediscoverysdk.GetOperationInput{OperationId: aws.String("op-dup")}).
		Return(operationOutput(servicediscoverytypes.OperationStatusPending, map[string]string{"NAMESPACE": "ns-dup"}), nil)

	result, err := newTestNamespace(client).Create(context.Background(), createRequest("example", fullProperties()))
	require.NoError(t, err)

	assert.Equal(t, resource.OperationStatusInProgress, result.ProgressResult.OperationStatus)
	assert.Equal(t, "ns-dup", result.ProgressResult.NativeID)
	state, err := decodeRequestID(result.ProgressResult.RequestID)
	require.NoError(t, err)
	assert.Equal(t, "op-dup", state.OperationID)
	client.AssertNotCalled(t, "ListNamespaces", mock.Anything, mock.Anything)
}

// The duplicate error does not always carry an operation id, and the operation it
// names may already have expired, so the namespace is resolved by name before the
// create is allowed to fail.
func TestCreateRecoversNamespaceIDByNameWhenTheDuplicateOperationIsUnusable(t *testing.T) {
	client := &mockServiceDiscoveryClient{}
	client.On("CreatePrivateDnsNamespace", mock.Anything, mock.Anything).
		Return(nil, &servicediscoverytypes.DuplicateRequest{Message: aws.String("operation already in progress")})
	client.On("ListNamespaces", mock.Anything, mock.Anything).
		Return(&servicediscoverysdk.ListNamespacesOutput{
			Namespaces: []servicediscoverytypes.NamespaceSummary{
				{Id: aws.String("ns-other"), Name: aws.String("other.internal")},
				{Id: aws.String("ns-byname"), Name: aws.String("example.internal")},
			},
		}, nil)

	result, err := newTestNamespace(client).Create(context.Background(), createRequest("example", fullProperties()))
	require.NoError(t, err)

	assert.Equal(t, resource.OperationStatusInProgress, result.ProgressResult.OperationStatus)
	assert.Equal(t, "ns-byname", result.ProgressResult.NativeID)

	// No operation is known, so Status falls back to checking the namespace.
	state, err := decodeRequestID(result.ProgressResult.RequestID)
	require.NoError(t, err)
	assert.Empty(t, state.OperationID)

	assert.ElementsMatch(t, []servicediscoverytypes.NamespaceFilter{
		{
			Name:      servicediscoverytypes.NamespaceFilterNameType,
			Values:    []string{string(servicediscoverytypes.NamespaceTypeDnsPrivate)},
			Condition: servicediscoverytypes.FilterConditionEq,
		},
		{
			Name:      servicediscoverytypes.NamespaceFilterNameResourceOwner,
			Values:    []string{resourceOwnerSelf},
			Condition: servicediscoverytypes.FilterConditionEq,
		},
	}, capturedListInput(t, client).Filters)
}

func TestCreateResolvesNamespaceByNameAcrossListPages(t *testing.T) {
	client := &mockServiceDiscoveryClient{}
	client.On("CreatePrivateDnsNamespace", mock.Anything, mock.Anything).
		Return(nil, &servicediscoverytypes.DuplicateRequest{Message: aws.String("operation already in progress")})
	client.On("ListNamespaces", mock.Anything, mock.MatchedBy(func(in *servicediscoverysdk.ListNamespacesInput) bool {
		return in.NextToken == nil
	})).Return(&servicediscoverysdk.ListNamespacesOutput{
		Namespaces: []servicediscoverytypes.NamespaceSummary{{Id: aws.String("ns-other"), Name: aws.String("other.internal")}},
		NextToken:  aws.String("page-2"),
	}, nil)
	client.On("ListNamespaces", mock.Anything, mock.MatchedBy(func(in *servicediscoverysdk.ListNamespacesInput) bool {
		return aws.ToString(in.NextToken) == "page-2"
	})).Return(&servicediscoverysdk.ListNamespacesOutput{
		Namespaces: []servicediscoverytypes.NamespaceSummary{{Id: aws.String("ns-byname"), Name: aws.String("example.internal")}},
	}, nil)

	result, err := newTestNamespace(client).Create(context.Background(), createRequest("example", fullProperties()))
	require.NoError(t, err)
	assert.Equal(t, "ns-byname", result.ProgressResult.NativeID)
}

func TestCreateFailsWhenADuplicateRequestCannotBeResolved(t *testing.T) {
	client := &mockServiceDiscoveryClient{}
	client.On("CreatePrivateDnsNamespace", mock.Anything, mock.Anything).
		Return(nil, &servicediscoverytypes.DuplicateRequest{Message: aws.String("operation already in progress")})
	client.On("ListNamespaces", mock.Anything, mock.Anything).
		Return(&servicediscoverysdk.ListNamespacesOutput{}, nil)

	result, err := newTestNamespace(client).Create(context.Background(), createRequest("example", fullProperties()))
	require.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "example.internal")
}

func TestStatusReportsSuccessWhenTheOperationSucceeded(t *testing.T) {
	client := namespaceReader()
	client.On("GetOperation", mock.Anything, &servicediscoverysdk.GetOperationInput{OperationId: aws.String("op-1")}).
		Return(operationOutput(servicediscoverytypes.OperationStatusSuccess, map[string]string{"NAMESPACE": "ns-abc"}), nil)

	requestID := encodeRequestID(requestState{OperationID: "op-1", Deadline: testNow.Add(time.Minute)})
	result, err := newTestNamespace(client).Status(context.Background(), &resource.StatusRequest{
		RequestID:    requestID,
		NativeID:     "ns-abc",
		ResourceType: resourceType,
	})
	require.NoError(t, err)

	assert.Equal(t, resource.OperationStatusSuccess, result.ProgressResult.OperationStatus)
	assert.Equal(t, "ns-abc", result.ProgressResult.NativeID)
	assert.Equal(t, requestID, result.ProgressResult.RequestID)
}

func TestStatusReportsFailureWithTheOperationErrorMessage(t *testing.T) {
	client := &mockServiceDiscoveryClient{}
	failed := operationOutput(servicediscoverytypes.OperationStatusFail, nil)
	failed.Operation.ErrorMessage = aws.String("CANNOT_CREATE_HOSTED_ZONE: quota exceeded")
	client.On("GetOperation", mock.Anything, mock.Anything).Return(failed, nil)

	result, err := newTestNamespace(client).Status(context.Background(), &resource.StatusRequest{
		RequestID: encodeRequestID(requestState{OperationID: "op-1", Deadline: testNow.Add(time.Minute)}),
		NativeID:  "ns-abc",
	})
	require.NoError(t, err)

	assert.Equal(t, resource.OperationStatusFailure, result.ProgressResult.OperationStatus)
	assert.Contains(t, result.ProgressResult.StatusMessage, "CANNOT_CREATE_HOSTED_ZONE: quota exceeded")
}

// A namespace name that collides with a hosted zone already associated with the
// VPC is accepted by Cloud Map and only fails on the operation, so the
// operation's error code and message are the only account of what went wrong.
func TestStatusReportsFailureWhenTheHostedZoneCannotBeCreated(t *testing.T) {
	client := &mockServiceDiscoveryClient{}
	failed := operationOutput(servicediscoverytypes.OperationStatusFail, map[string]string{"NAMESPACE": "ns-abc"})
	failed.Operation.ErrorCode = aws.String("CANNOT_CREATE_HOSTED_ZONE")
	failed.Operation.ErrorMessage = aws.String(
		"An error occurred while creating the hosted zone: ConflictingDomainExists: " +
			"The VPC vpc-0123456789abcdef0 has already been associated with the hosted zone " +
			"Z0123456789ABCDEFGHIJ with the same domain name")
	client.On("GetOperation", mock.Anything, mock.Anything).Return(failed, nil)

	result, err := newTestNamespace(client).Status(context.Background(), &resource.StatusRequest{
		RequestID: encodeRequestID(requestState{OperationID: "op-1", Deadline: testNow.Add(time.Minute)}),
		NativeID:  "ns-abc",
	})
	require.NoError(t, err)

	assert.Equal(t, resource.OperationStatusFailure, result.ProgressResult.OperationStatus)
	assert.Contains(t, result.ProgressResult.StatusMessage, "CANNOT_CREATE_HOSTED_ZONE")
	assert.Contains(t, result.ProgressResult.StatusMessage,
		"has already been associated with the hosted zone Z0123456789ABCDEFGHIJ with the same domain name")
}

func TestStatusReportsInProgressWhileTheOperationRuns(t *testing.T) {
	for _, status := range []servicediscoverytypes.OperationStatus{
		servicediscoverytypes.OperationStatusSubmitted,
		servicediscoverytypes.OperationStatusPending,
	} {
		t.Run(string(status), func(t *testing.T) {
			client := &mockServiceDiscoveryClient{}
			client.On("GetOperation", mock.Anything, mock.Anything).Return(operationOutput(status, nil), nil)

			result, err := newTestNamespace(client).Status(context.Background(), &resource.StatusRequest{
				RequestID: encodeRequestID(requestState{OperationID: "op-1", Deadline: testNow.Add(time.Minute)}),
				NativeID:  "ns-abc",
			})
			require.NoError(t, err)
			assert.Equal(t, resource.OperationStatusInProgress, result.ProgressResult.OperationStatus)
		})
	}
}

func TestStatusReportsFailureWhenTheOperationOutlivesItsDeadline(t *testing.T) {
	client := &mockServiceDiscoveryClient{}
	client.On("GetOperation", mock.Anything, mock.Anything).
		Return(operationOutput(servicediscoverytypes.OperationStatusPending, nil), nil)

	result, err := newTestNamespace(client).Status(context.Background(), &resource.StatusRequest{
		RequestID: encodeRequestID(requestState{OperationID: "op-1", Deadline: testNow.Add(-time.Minute)}),
		NativeID:  "ns-abc",
	})
	require.NoError(t, err)

	assert.Equal(t, resource.OperationStatusFailure, result.ProgressResult.OperationStatus)
	assert.Contains(t, result.ProgressResult.StatusMessage, "timeout")
}

func TestStatusReportsFailureWhenTheOperationIsNotFound(t *testing.T) {
	client := &mockServiceDiscoveryClient{}
	client.On("GetOperation", mock.Anything, mock.Anything).
		Return(nil, &servicediscoverytypes.OperationNotFound{Message: aws.String("op-1 not found")})

	result, err := newTestNamespace(client).Status(context.Background(), &resource.StatusRequest{
		RequestID: encodeRequestID(requestState{OperationID: "op-1", Deadline: testNow.Add(time.Minute)}),
		NativeID:  "ns-abc",
	})
	require.NoError(t, err)

	assert.Equal(t, resource.OperationStatusFailure, result.ProgressResult.OperationStatus)
	assert.Contains(t, result.ProgressResult.StatusMessage, "op-1")
}

// A create adopted by name knows the namespace but not the operation that made
// it. Cloud Map keeps an operation for a day, so a namespace whose create is no
// longer listed settled long ago and the namespace itself is all there is left
// to confirm.
func TestStatusFallsBackToTheNamespaceWhenItsCreateOperationIsNoLongerListed(t *testing.T) {
	client := &mockServiceDiscoveryClient{}
	client.On("ListOperations", mock.Anything, mock.Anything).
		Return(&servicediscoverysdk.ListOperationsOutput{}, nil)
	client.On("GetNamespace", mock.Anything, &servicediscoverysdk.GetNamespaceInput{Id: aws.String("ns-abc")}).
		Return(&servicediscoverysdk.GetNamespaceOutput{
			Namespace: &servicediscoverytypes.Namespace{Id: aws.String("ns-abc")},
		}, nil)

	result, err := newTestNamespace(client).Status(context.Background(), &resource.StatusRequest{
		RequestID: encodeRequestID(requestState{Deadline: testNow.Add(time.Minute)}),
		NativeID:  "ns-abc",
	})
	require.NoError(t, err)

	assert.Equal(t, resource.OperationStatusSuccess, result.ProgressResult.OperationStatus)
	assert.Equal(t, "ns-abc", result.ProgressResult.NativeID)
	client.AssertNotCalled(t, "GetOperation", mock.Anything, mock.Anything)
}

func TestStatusWaitsForAnAdoptedNamespaceToBecomeVisible(t *testing.T) {
	client := &mockServiceDiscoveryClient{}
	client.On("ListOperations", mock.Anything, mock.Anything).
		Return(&servicediscoverysdk.ListOperationsOutput{}, nil)
	client.On("GetNamespace", mock.Anything, mock.Anything).
		Return(nil, &servicediscoverytypes.NamespaceNotFound{Message: aws.String("ns-abc not found")})

	provisioner := newTestNamespace(client)

	inProgress, err := provisioner.Status(context.Background(), &resource.StatusRequest{
		RequestID: encodeRequestID(requestState{Deadline: testNow.Add(time.Minute)}),
		NativeID:  "ns-abc",
	})
	require.NoError(t, err)
	assert.Equal(t, resource.OperationStatusInProgress, inProgress.ProgressResult.OperationStatus)

	failed, err := provisioner.Status(context.Background(), &resource.StatusRequest{
		RequestID: encodeRequestID(requestState{Deadline: testNow.Add(-time.Minute)}),
		NativeID:  "ns-abc",
	})
	require.NoError(t, err)
	assert.Equal(t, resource.OperationStatusFailure, failed.ProgressResult.OperationStatus)
}

func TestStatusRejectsAnUndecodableRequestID(t *testing.T) {
	_, err := newTestNamespace(&mockServiceDiscoveryClient{}).Status(context.Background(), &resource.StatusRequest{
		RequestID: "op=op-1",
		NativeID:  "ns-abc",
	})
	require.Error(t, err)
}

// The schema declares the hosted zone id as a top-level read-only field and the
// SOA TTL as a nested one, while Cloud Map nests both under the namespace's
// DnsProperties. Both shapes have to be emitted: the top-level one backs the
// hostedZoneId resolvable, the nested one is what the declared field compares
// against.
func TestReadFlattensTheNamespaceToTheDeclaredShape(t *testing.T) {
	properties := readNamespace(t, namespaceReader())

	assert.Equal(t, map[string]any{
		"Id":           "ns-abc",
		"Arn":          namespaceARN,
		"Name":         "example.internal",
		"Description":  "namespace for the example service",
		"HostedZoneId": "Z0123456789ABCDEFGHIJ",
		"Properties": map[string]any{
			"DnsProperties": map[string]any{
				"SOA": map[string]any{"TTL": float64(60)},
			},
		},
		"Tags": []any{map[string]any{"Key": "Name", "Value": "example"}},
	}, properties)
}

// Cloud Map exposes a namespace's VPC through no API at all, so a read can only
// ever fabricate one — and a fabricated value for a create-only field reads as
// drift and drives a destructive replace.
func TestReadOmitsTheUnreadableVpc(t *testing.T) {
	assert.NotContains(t, readNamespace(t, namespaceReader()), "Vpc")
}

// The tag APIs address a namespace by ARN, which the read response carries.
func TestReadTakesTagsFromTheNamespaceARN(t *testing.T) {
	client := namespaceReader()
	readNamespace(t, client)

	assert.Equal(t, namespaceARN, aws.ToString(capturedListTagsInput(t, client).ResourceARN))
}

// A property the namespace does not carry is absent rather than empty: an empty
// value would compare as drift against a declaration that never set it.
func TestReadOmitsPropertiesTheNamespaceDoesNotCarry(t *testing.T) {
	client := &mockServiceDiscoveryClient{}
	client.On("GetNamespace", mock.Anything, mock.Anything).
		Return(&servicediscoverysdk.GetNamespaceOutput{
			Namespace: &servicediscoverytypes.Namespace{
				Id:   aws.String("ns-abc"),
				Arn:  aws.String(namespaceARN),
				Name: aws.String("example.internal"),
			},
		}, nil)
	client.On("ListTagsForResource", mock.Anything, mock.Anything).
		Return(&servicediscoverysdk.ListTagsForResourceOutput{}, nil)

	assert.Equal(t, map[string]any{
		"Id":   "ns-abc",
		"Arn":  namespaceARN,
		"Name": "example.internal",
	}, readNamespace(t, client))
}

func TestReadReportsNotFoundWhenTheNamespaceIsGone(t *testing.T) {
	client := &mockServiceDiscoveryClient{}
	client.On("GetNamespace", mock.Anything, mock.Anything).
		Return(nil, &servicediscoverytypes.NamespaceNotFound{Message: aws.String("ns-abc not found")})

	result, err := newTestNamespace(client).Read(context.Background(), &resource.ReadRequest{
		NativeID:     "ns-abc",
		ResourceType: resourceType,
	})
	require.NoError(t, err)

	assert.Equal(t, resource.OperationErrorCodeNotFound, result.ErrorCode)
	assert.Empty(t, result.Properties)
	client.AssertNotCalled(t, "ListTagsForResource", mock.Anything, mock.Anything)
}

func TestReadFailsWhenTheNamespaceCannotBeRetrieved(t *testing.T) {
	client := &mockServiceDiscoveryClient{}
	client.On("GetNamespace", mock.Anything, mock.Anything).
		Return(nil, errors.New("throttled"))

	_, err := newTestNamespace(client).Read(context.Background(), &resource.ReadRequest{
		NativeID:     "ns-abc",
		ResourceType: resourceType,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "throttled")
}

// Namespaces shared into this account are listed too, and this account can
// neither tag nor delete them, so the listing is narrowed to the private DNS
// namespaces it owns itself.
func TestListReturnsThePrivateDnsNamespacesThisAccountOwns(t *testing.T) {
	client := &mockServiceDiscoveryClient{}
	client.On("ListNamespaces", mock.Anything, mock.Anything).
		Return(&servicediscoverysdk.ListNamespacesOutput{
			Namespaces: []servicediscoverytypes.NamespaceSummary{
				{Id: aws.String("ns-1"), Name: aws.String("one.internal")},
				{Id: aws.String("ns-2"), Name: aws.String("two.internal")},
			},
		}, nil)

	result, err := newTestNamespace(client).List(context.Background(), &resource.ListRequest{
		ResourceType: resourceType,
		PageSize:     25,
	})
	require.NoError(t, err)

	assert.Equal(t, []string{"ns-1", "ns-2"}, result.NativeIDs)
	assert.Nil(t, result.NextPageToken)

	input := capturedListInput(t, client)
	assert.ElementsMatch(t, []servicediscoverytypes.NamespaceFilter{
		{
			Name:      servicediscoverytypes.NamespaceFilterNameType,
			Values:    []string{string(servicediscoverytypes.NamespaceTypeDnsPrivate)},
			Condition: servicediscoverytypes.FilterConditionEq,
		},
		{
			Name:      servicediscoverytypes.NamespaceFilterNameResourceOwner,
			Values:    []string{resourceOwnerSelf},
			Condition: servicediscoverytypes.FilterConditionEq,
		},
	}, input.Filters)
	assert.Equal(t, int32(25), aws.ToInt32(input.MaxResults))
	assert.Nil(t, input.NextToken)
}

func TestListResumesFromThePageTokenAndReportsTheNextOne(t *testing.T) {
	client := &mockServiceDiscoveryClient{}
	client.On("ListNamespaces", mock.Anything, mock.Anything).
		Return(&servicediscoverysdk.ListNamespacesOutput{
			Namespaces: []servicediscoverytypes.NamespaceSummary{{Id: aws.String("ns-2")}},
			NextToken:  aws.String("page-3"),
		}, nil)

	result, err := newTestNamespace(client).List(context.Background(), &resource.ListRequest{
		ResourceType: resourceType,
		PageToken:    aws.String("page-2"),
	})
	require.NoError(t, err)

	assert.Equal(t, []string{"ns-2"}, result.NativeIDs)
	assert.Equal(t, "page-3", aws.ToString(result.NextPageToken))
	assert.Equal(t, "page-2", aws.ToString(capturedListInput(t, client).NextToken))
}

// Cloud Map applies the filters after it has taken a page, so a page can come
// back empty with a token that still leads to matching namespaces. Stopping on
// the empty page would truncate the listing.
func TestListContinuesPastAnEmptyPageThatStillCarriesAToken(t *testing.T) {
	client := &mockServiceDiscoveryClient{}
	client.On("ListNamespaces", mock.Anything, mock.MatchedBy(func(in *servicediscoverysdk.ListNamespacesInput) bool {
		return in.NextToken == nil
	})).Return(&servicediscoverysdk.ListNamespacesOutput{NextToken: aws.String("page-2")}, nil)
	client.On("ListNamespaces", mock.Anything, mock.MatchedBy(func(in *servicediscoverysdk.ListNamespacesInput) bool {
		return aws.ToString(in.NextToken) == "page-2"
	})).Return(&servicediscoverysdk.ListNamespacesOutput{
		Namespaces: []servicediscoverytypes.NamespaceSummary{{Id: aws.String("ns-late")}},
	}, nil)

	result, err := newTestNamespace(client).List(context.Background(), &resource.ListRequest{ResourceType: resourceType})
	require.NoError(t, err)

	assert.Equal(t, []string{"ns-late"}, result.NativeIDs)
	assert.Nil(t, result.NextPageToken)
	client.AssertNumberOfCalls(t, "ListNamespaces", 2)
}

func TestListStopsWhenTheLastPageIsEmptyAndCarriesNoToken(t *testing.T) {
	client := &mockServiceDiscoveryClient{}
	client.On("ListNamespaces", mock.Anything, mock.Anything).
		Return(&servicediscoverysdk.ListNamespacesOutput{}, nil)

	result, err := newTestNamespace(client).List(context.Background(), &resource.ListRequest{ResourceType: resourceType})
	require.NoError(t, err)

	assert.Empty(t, result.NativeIDs)
	assert.Nil(t, result.NextPageToken)
	client.AssertNumberOfCalls(t, "ListNamespaces", 1)
}

// The namespace's read-only fields are only knowable once it exists, so the
// poll that observes it settle is what carries them into the stored row the
// resolvables of dependent resources resolve against.
func TestStatusCarriesTheNamespacePropertiesWhenTheOperationSucceeds(t *testing.T) {
	client := namespaceReader()
	client.On("GetOperation", mock.Anything, mock.Anything).
		Return(operationOutput(servicediscoverytypes.OperationStatusSuccess, map[string]string{"NAMESPACE": "ns-abc"}), nil)

	result, err := newTestNamespace(client).Status(context.Background(), &resource.StatusRequest{
		RequestID:    encodeRequestID(requestState{OperationID: "op-1", Deadline: testNow.Add(time.Minute)}),
		NativeID:     "ns-abc",
		ResourceType: resourceType,
	})
	require.NoError(t, err)

	properties := decodedProperties(t, string(result.ProgressResult.ResourceProperties))
	assert.Equal(t, "ns-abc", properties["Id"])
	assert.Equal(t, namespaceARN, properties["Arn"])
	assert.Equal(t, "Z0123456789ABCDEFGHIJ", properties["HostedZoneId"])
}

func TestStatusCarriesTheNamespacePropertiesWhenNoOperationIsListed(t *testing.T) {
	client := namespaceReader()
	client.On("ListOperations", mock.Anything, mock.Anything).
		Return(&servicediscoverysdk.ListOperationsOutput{}, nil)

	result, err := newTestNamespace(client).Status(context.Background(), &resource.StatusRequest{
		RequestID:    encodeRequestID(requestState{Deadline: testNow.Add(time.Minute)}),
		NativeID:     "ns-abc",
		ResourceType: resourceType,
	})
	require.NoError(t, err)

	properties := decodedProperties(t, string(result.ProgressResult.ResourceProperties))
	assert.Equal(t, "ns-abc", properties["Id"])
	assert.Equal(t, namespaceARN, properties["Arn"])
	assert.Equal(t, "Z0123456789ABCDEFGHIJ", properties["HostedZoneId"])
	client.AssertNumberOfCalls(t, "GetNamespace", 1)
}

// ----- Delete -----

func namespaceInUse() error {
	return &servicediscoverytypes.ResourceInUse{
		Message: aws.String("Namespace has associated services; delete the services before deleting the namespace"),
	}
}

func TestDeleteStartsTheDeleteOperationAndTracksIt(t *testing.T) {
	client := &mockServiceDiscoveryClient{}
	client.On("DeleteNamespace", mock.Anything, mock.Anything).
		Return(&servicediscoverysdk.DeleteNamespaceOutput{OperationId: aws.String("op-del")}, nil)

	result, err := newTestNamespace(client).Delete(context.Background(), &resource.DeleteRequest{
		NativeID:     "ns-abc",
		ResourceType: resourceType,
	})
	require.NoError(t, err)

	assert.Equal(t, "ns-abc", aws.ToString(capturedDeleteInput(t, client).Id))
	assert.Equal(t, resource.OperationDelete, result.ProgressResult.Operation)
	assert.Equal(t, resource.OperationStatusInProgress, result.ProgressResult.OperationStatus)
	assert.Equal(t, "ns-abc", result.ProgressResult.NativeID)

	state, err := decodeRequestID(result.ProgressResult.RequestID)
	require.NoError(t, err)
	assert.Equal(t, phaseDelete, state.Phase)
	assert.Equal(t, "op-del", state.OperationID)
	assert.Equal(t, testNow.Add(namespaceDeleteTimeout), state.Deadline)
}

// A destroy that reaches a namespace another destroy already removed has nothing
// left to do.
func TestDeleteSucceedsWhenTheNamespaceIsAlreadyGone(t *testing.T) {
	client := &mockServiceDiscoveryClient{}
	client.On("DeleteNamespace", mock.Anything, mock.Anything).
		Return(nil, &servicediscoverytypes.NamespaceNotFound{Message: aws.String("ns-abc not found")})

	result, err := newTestNamespace(client).Delete(context.Background(), &resource.DeleteRequest{
		NativeID:     "ns-abc",
		ResourceType: resourceType,
	})
	require.NoError(t, err)

	assert.Equal(t, resource.OperationDelete, result.ProgressResult.Operation)
	assert.Equal(t, resource.OperationStatusSuccess, result.ProgressResult.OperationStatus)
	assert.Equal(t, "ns-abc", result.ProgressResult.NativeID)
}

// Cloud Map rejects the delete of a namespace that still holds services outright,
// without an operation to poll. ECS deregisters a service's instances only after
// the ECS service is gone, so a destroy legitimately reaches the namespace while
// deregistration is still settling and the delete has to be re-issued.
func TestDeleteEntersTheRetryPhaseWhenTheNamespaceStillHoldsResources(t *testing.T) {
	client := &mockServiceDiscoveryClient{}
	client.On("DeleteNamespace", mock.Anything, mock.Anything).Return(nil, namespaceInUse())

	result, err := newTestNamespace(client).Delete(context.Background(), &resource.DeleteRequest{
		NativeID:     "ns-abc",
		ResourceType: resourceType,
	})
	require.NoError(t, err)

	assert.Equal(t, resource.OperationStatusInProgress, result.ProgressResult.OperationStatus)
	assert.Equal(t, "ns-abc", result.ProgressResult.NativeID)

	state, err := decodeRequestID(result.ProgressResult.RequestID)
	require.NoError(t, err)
	assert.Equal(t, phaseRetryDelete, state.Phase)
	assert.Empty(t, state.OperationID)
	assert.Equal(t, testNow.Add(namespaceDeleteTimeout), state.Deadline)
}

// A throttled or otherwise transient rejection is not dependency contention: it
// is an ordinary retryable error and must not be mistaken for a namespace that
// still holds resources.
func TestDeleteFailsOnATransientErrorRatherThanRetryingTheDelete(t *testing.T) {
	client := &mockServiceDiscoveryClient{}
	client.On("DeleteNamespace", mock.Anything, mock.Anything).Return(nil, errors.New("throttled"))

	result, err := newTestNamespace(client).Delete(context.Background(), &resource.DeleteRequest{
		NativeID:     "ns-abc",
		ResourceType: resourceType,
	})
	require.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "throttled")
}

// The namespace is gone by the time a delete operation succeeds, so reading it
// back would only find nothing.
func TestStatusReportsADeleteSuccessWithoutReadingTheNamespaceBack(t *testing.T) {
	client := &mockServiceDiscoveryClient{}
	client.On("GetOperation", mock.Anything, &servicediscoverysdk.GetOperationInput{OperationId: aws.String("op-del")}).
		Return(operationOutput(servicediscoverytypes.OperationStatusSuccess, map[string]string{"NAMESPACE": "ns-abc"}), nil)

	result, err := newTestNamespace(client).Status(context.Background(), &resource.StatusRequest{
		RequestID: encodeRequestID(requestState{
			Phase:       phaseDelete,
			OperationID: "op-del",
			Deadline:    testNow.Add(time.Minute),
		}),
		NativeID:     "ns-abc",
		ResourceType: resourceType,
	})
	require.NoError(t, err)

	assert.Equal(t, resource.OperationStatusSuccess, result.ProgressResult.OperationStatus)
	assert.Equal(t, "ns-abc", result.ProgressResult.NativeID)
	client.AssertNotCalled(t, "GetNamespace", mock.Anything, mock.Anything)
}

// Contention can also surface on the operation rather than on the call, and it
// is no more terminal there than it is when the call is rejected outright. The
// deadline the delete started against carries across, so contention that keeps
// surfacing on the operation runs the wait out instead of renewing it forever.
func TestStatusEntersTheRetryPhaseWhenTheDeleteOperationReportsResourcesStillHeld(t *testing.T) {
	client := &mockServiceDiscoveryClient{}
	failed := operationOutput(servicediscoverytypes.OperationStatusFail, map[string]string{"NAMESPACE": "ns-abc"})
	failed.Operation.ErrorCode = aws.String("RESOURCE_IN_USE")
	failed.Operation.ErrorMessage = aws.String("Namespace has associated services")
	client.On("GetOperation", mock.Anything, mock.Anything).Return(failed, nil)

	deadline := testNow.Add(time.Minute)
	result, err := newTestNamespace(client).Status(context.Background(), &resource.StatusRequest{
		RequestID: encodeRequestID(requestState{
			Phase:       phaseDelete,
			OperationID: "op-del",
			Deadline:    deadline,
		}),
		NativeID: "ns-abc",
	})
	require.NoError(t, err)

	assert.Equal(t, resource.OperationStatusInProgress, result.ProgressResult.OperationStatus)
	state, err := decodeRequestID(result.ProgressResult.RequestID)
	require.NoError(t, err)
	assert.Equal(t, phaseRetryDelete, state.Phase)
	assert.Equal(t, deadline, state.Deadline)
	client.AssertNotCalled(t, "DeleteNamespace", mock.Anything, mock.Anything)
}

func TestStatusReportsFailureWhenADeleteOperationFailsForAnotherReason(t *testing.T) {
	client := &mockServiceDiscoveryClient{}
	failed := operationOutput(servicediscoverytypes.OperationStatusFail, nil)
	failed.Operation.ErrorCode = aws.String("ACCESS_DENIED")
	client.On("GetOperation", mock.Anything, mock.Anything).Return(failed, nil)

	result, err := newTestNamespace(client).Status(context.Background(), &resource.StatusRequest{
		RequestID: encodeRequestID(requestState{
			Phase:       phaseDelete,
			OperationID: "op-del",
			Deadline:    testNow.Add(time.Minute),
		}),
		NativeID: "ns-abc",
	})
	require.NoError(t, err)

	assert.Equal(t, resource.OperationStatusFailure, result.ProgressResult.OperationStatus)
	assert.Contains(t, result.ProgressResult.StatusMessage, "ACCESS_DENIED")
}

// The rejection carries no operation id, so there is nothing to poll: re-issuing
// the delete is what the poll does.
func TestStatusReissuesTheDeleteWhileTheRetryDeadlineHolds(t *testing.T) {
	client := &mockServiceDiscoveryClient{}
	client.On("DeleteNamespace", mock.Anything, mock.Anything).
		Return(&servicediscoverysdk.DeleteNamespaceOutput{OperationId: aws.String("op-del")}, nil)

	deadline := testNow.Add(time.Minute)
	result, err := newTestNamespace(client).Status(context.Background(), &resource.StatusRequest{
		RequestID: encodeRequestID(requestState{Phase: phaseRetryDelete, Deadline: deadline}),
		NativeID:  "ns-abc",
	})
	require.NoError(t, err)

	assert.Equal(t, "ns-abc", aws.ToString(capturedDeleteInput(t, client).Id))
	assert.Equal(t, resource.OperationStatusInProgress, result.ProgressResult.OperationStatus)

	state, err := decodeRequestID(result.ProgressResult.RequestID)
	require.NoError(t, err)
	assert.Equal(t, phaseDelete, state.Phase)
	assert.Equal(t, "op-del", state.OperationID)
	assert.Equal(t, deadline, state.Deadline)
}

// The retry deadline is what bounds the wait, so a retry that is rejected again
// keeps the deadline it started with rather than pushing it further out.
func TestStatusKeepsTheRetryDeadlineWhileTheNamespaceStillHoldsResources(t *testing.T) {
	client := &mockServiceDiscoveryClient{}
	client.On("DeleteNamespace", mock.Anything, mock.Anything).Return(nil, namespaceInUse())

	deadline := testNow.Add(time.Minute)
	result, err := newTestNamespace(client).Status(context.Background(), &resource.StatusRequest{
		RequestID: encodeRequestID(requestState{Phase: phaseRetryDelete, Deadline: deadline}),
		NativeID:  "ns-abc",
	})
	require.NoError(t, err)

	assert.Equal(t, resource.OperationStatusInProgress, result.ProgressResult.OperationStatus)
	state, err := decodeRequestID(result.ProgressResult.RequestID)
	require.NoError(t, err)
	assert.Equal(t, phaseRetryDelete, state.Phase)
	assert.Equal(t, deadline, state.Deadline)
}

func TestStatusReportsSuccessWhenTheRetriedDeleteFindsTheNamespaceGone(t *testing.T) {
	client := &mockServiceDiscoveryClient{}
	client.On("DeleteNamespace", mock.Anything, mock.Anything).
		Return(nil, &servicediscoverytypes.NamespaceNotFound{Message: aws.String("ns-abc not found")})

	result, err := newTestNamespace(client).Status(context.Background(), &resource.StatusRequest{
		RequestID: encodeRequestID(requestState{Phase: phaseRetryDelete, Deadline: testNow.Add(time.Minute)}),
		NativeID:  "ns-abc",
	})
	require.NoError(t, err)

	assert.Equal(t, resource.OperationStatusSuccess, result.ProgressResult.OperationStatus)
	assert.Equal(t, "ns-abc", result.ProgressResult.NativeID)
}

func TestStatusFailsTheDeleteOncePastTheRetryDeadline(t *testing.T) {
	client := &mockServiceDiscoveryClient{}

	result, err := newTestNamespace(client).Status(context.Background(), &resource.StatusRequest{
		RequestID: encodeRequestID(requestState{Phase: phaseRetryDelete, Deadline: testNow.Add(-time.Minute)}),
		NativeID:  "ns-abc",
	})
	require.NoError(t, err)

	assert.Equal(t, resource.OperationStatusFailure, result.ProgressResult.OperationStatus)
	assert.Contains(t, result.ProgressResult.StatusMessage, "timeout")
	assert.Contains(t, result.ProgressResult.StatusMessage, "ns-abc")
	client.AssertNotCalled(t, "DeleteNamespace", mock.Anything, mock.Anything)
}

// ----- Update -----

// storedProperties is a namespace's row as Read renders it, which is what an
// update compares its declaration against.
func storedProperties() map[string]any {
	return map[string]any{
		"Id":           "ns-abc",
		"Arn":          namespaceARN,
		"Name":         "example.internal",
		"Description":  "namespace for the example service",
		"HostedZoneId": "Z0123456789ABCDEFGHIJ",
		"Properties": map[string]any{
			"DnsProperties": map[string]any{"SOA": map[string]any{"TTL": 60}},
		},
		"Tags": []any{
			map[string]any{"Key": "Name", "Value": "example"},
			map[string]any{"Key": "Team", "Value": "platform"},
		},
	}
}

func updateRequest(prior, desired map[string]any) *resource.UpdateRequest {
	priorRaw, err := json.Marshal(prior)
	if err != nil {
		panic(err)
	}
	desiredRaw, err := json.Marshal(desired)
	if err != nil {
		panic(err)
	}
	return &resource.UpdateRequest{
		NativeID:          "ns-abc",
		ResourceType:      resourceType,
		Label:             "example",
		PriorProperties:   priorRaw,
		DesiredProperties: desiredRaw,
	}
}

// The tag APIs are synchronous, so a tag change on its own leaves nothing to
// wait for and must not mint an operation phase the engine would then poll.
func TestUpdateAppliesTagsWithoutAnOperationWhenOnlyTagsChanged(t *testing.T) {
	client := namespaceReader()
	client.On("UntagResource", mock.Anything, mock.Anything).
		Return(&servicediscoverysdk.UntagResourceOutput{}, nil)
	client.On("TagResource", mock.Anything, mock.Anything).
		Return(&servicediscoverysdk.TagResourceOutput{}, nil)

	desired := storedProperties()
	desired["Tags"] = []any{
		map[string]any{"Key": "Name", "Value": "renamed"},
		map[string]any{"Key": "Owner", "Value": "infra"},
	}

	result, err := newTestNamespace(client).Update(context.Background(), updateRequest(storedProperties(), desired))
	require.NoError(t, err)

	// Team is gone, Owner is new and Name changed value; Cloud Map overwrites the
	// value of a key that is tagged again, so only the dropped key is untagged.
	assert.Equal(t, namespaceARN, aws.ToString(capturedUntagInput(t, client).ResourceARN))
	assert.Equal(t, []string{"Team"}, capturedUntagInput(t, client).TagKeys)
	assert.Equal(t, namespaceARN, aws.ToString(capturedTagInput(t, client).ResourceARN))
	assert.Equal(t, []servicediscoverytypes.Tag{
		{Key: aws.String("Name"), Value: aws.String("renamed")},
		{Key: aws.String("Owner"), Value: aws.String("infra")},
	}, capturedTagInput(t, client).Tags)

	client.AssertNotCalled(t, "UpdatePrivateDnsNamespace", mock.Anything, mock.Anything)
	assert.Equal(t, resource.OperationUpdate, result.ProgressResult.Operation)
	assert.Equal(t, resource.OperationStatusSuccess, result.ProgressResult.OperationStatus)
	assert.Equal(t, "ns-abc", result.ProgressResult.NativeID)
	assert.Empty(t, result.ProgressResult.RequestID)

	properties := decodedProperties(t, string(result.ProgressResult.ResourceProperties))
	assert.Equal(t, namespaceARN, properties["Arn"])
	assert.Equal(t, "Z0123456789ABCDEFGHIJ", properties["HostedZoneId"])
}

// The tag calls are synchronous and idempotent while the property update is not,
// so tagging first means an update interrupted between the two re-applies the
// tags harmlessly and then dispatches the property update again.
func TestUpdateAppliesTagsBeforeDispatchingThePropertyUpdate(t *testing.T) {
	client := &mockServiceDiscoveryClient{}
	client.On("UntagResource", mock.Anything, mock.Anything).
		Return(&servicediscoverysdk.UntagResourceOutput{}, nil)
	client.On("TagResource", mock.Anything, mock.Anything).
		Return(&servicediscoverysdk.TagResourceOutput{}, nil)
	client.On("UpdatePrivateDnsNamespace", mock.Anything, mock.Anything).
		Return(&servicediscoverysdk.UpdatePrivateDnsNamespaceOutput{OperationId: aws.String("op-upd")}, nil)

	desired := storedProperties()
	desired["Description"] = "namespace for the renamed service"
	desired["Properties"] = map[string]any{
		"DnsProperties": map[string]any{"SOA": map[string]any{"TTL": 120}},
	}
	desired["Tags"] = []any{map[string]any{"Key": "Owner", "Value": "infra"}}

	result, err := newTestNamespace(client).Update(context.Background(), updateRequest(storedProperties(), desired))
	require.NoError(t, err)

	assert.Equal(t, []string{"UntagResource", "TagResource", "UpdatePrivateDnsNamespace"}, callOrder(client))

	input := capturedUpdateInput(t, client)
	assert.Equal(t, "ns-abc", aws.ToString(input.Id))
	require.NotNil(t, input.Namespace)
	assert.Equal(t, "namespace for the renamed service", aws.ToString(input.Namespace.Description))
	require.NotNil(t, input.Namespace.Properties)
	require.NotNil(t, input.Namespace.Properties.DnsProperties)
	require.NotNil(t, input.Namespace.Properties.DnsProperties.SOA)
	assert.Equal(t, int64(120), aws.ToInt64(input.Namespace.Properties.DnsProperties.SOA.TTL))

	assert.Equal(t, resource.OperationUpdate, result.ProgressResult.Operation)
	assert.Equal(t, resource.OperationStatusInProgress, result.ProgressResult.OperationStatus)
	assert.Equal(t, "ns-abc", result.ProgressResult.NativeID)

	state, err := decodeRequestID(result.ProgressResult.RequestID)
	require.NoError(t, err)
	assert.Empty(t, state.Phase)
	assert.Equal(t, "op-upd", state.OperationID)
	assert.Equal(t, testNow.Add(namespaceOperationTimeout), state.Deadline)
}

// A property update dispatched before the tags were applied would leave the tags
// unapplied for good if the tag call is what fails.
func TestUpdateDoesNotDispatchThePropertyUpdateWhenTaggingFails(t *testing.T) {
	client := &mockServiceDiscoveryClient{}
	client.On("TagResource", mock.Anything, mock.Anything).Return(nil, errors.New("throttled"))

	desired := storedProperties()
	desired["Description"] = "namespace for the renamed service"
	desired["Tags"] = []any{
		map[string]any{"Key": "Name", "Value": "example"},
		map[string]any{"Key": "Team", "Value": "platform"},
		map[string]any{"Key": "Owner", "Value": "infra"},
	}

	_, err := newTestNamespace(client).Update(context.Background(), updateRequest(storedProperties(), desired))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "throttled")
	client.AssertNotCalled(t, "UpdatePrivateDnsNamespace", mock.Anything, mock.Anything)
}

// A description the declaration no longer carries is cleared rather than left
// behind, which an update that only sends the fields that changed would do.
func TestUpdateClearsADescriptionThatIsNoLongerDeclared(t *testing.T) {
	client := &mockServiceDiscoveryClient{}
	client.On("UpdatePrivateDnsNamespace", mock.Anything, mock.Anything).
		Return(&servicediscoverysdk.UpdatePrivateDnsNamespaceOutput{OperationId: aws.String("op-upd")}, nil)

	desired := storedProperties()
	delete(desired, "Description")

	_, err := newTestNamespace(client).Update(context.Background(), updateRequest(storedProperties(), desired))
	require.NoError(t, err)

	input := capturedUpdateInput(t, client)
	require.NotNil(t, input.Namespace)
	assert.Equal(t, "", aws.ToString(input.Namespace.Description))
	client.AssertNotCalled(t, "TagResource", mock.Anything, mock.Anything)
	client.AssertNotCalled(t, "UntagResource", mock.Anything, mock.Anything)
}

// The tag APIs address a namespace by ARN rather than by id, so an update of a
// row that predates the ARN being recorded reads it off the namespace itself.
func TestUpdateResolvesTheNamespaceARNWhenTheStoredRowCarriesNone(t *testing.T) {
	client := namespaceReader()
	client.On("TagResource", mock.Anything, mock.Anything).
		Return(&servicediscoverysdk.TagResourceOutput{}, nil)

	prior := storedProperties()
	delete(prior, "Arn")
	desired := storedProperties()
	delete(desired, "Arn")
	desired["Tags"] = []any{
		map[string]any{"Key": "Name", "Value": "example"},
		map[string]any{"Key": "Team", "Value": "platform"},
		map[string]any{"Key": "Owner", "Value": "infra"},
	}

	_, err := newTestNamespace(client).Update(context.Background(), updateRequest(prior, desired))
	require.NoError(t, err)

	assert.Equal(t, namespaceARN, aws.ToString(capturedTagInput(t, client).ResourceARN))
}

// A TTL the declaration does not carry is left as it is: Cloud Map's own default
// is unknowable from here, so there is nothing to reset it to.
func TestUpdateLeavesAnUndeclaredSOATTLAlone(t *testing.T) {
	client := &mockServiceDiscoveryClient{}
	client.On("UpdatePrivateDnsNamespace", mock.Anything, mock.Anything).
		Return(&servicediscoverysdk.UpdatePrivateDnsNamespaceOutput{OperationId: aws.String("op-upd")}, nil)

	desired := storedProperties()
	desired["Description"] = "namespace for the renamed service"
	delete(desired, "Properties")

	_, err := newTestNamespace(client).Update(context.Background(), updateRequest(storedProperties(), desired))
	require.NoError(t, err)

	assert.Nil(t, capturedUpdateInput(t, client).Namespace.Properties)
}

func TestUpdateRejectsADeclaredPropertyOfTheWrongType(t *testing.T) {
	client := &mockServiceDiscoveryClient{}

	desired := storedProperties()
	desired["Properties"] = map[string]any{
		"DnsProperties": map[string]any{"SOA": map[string]any{"TTL": "120"}},
	}

	_, err := newTestNamespace(client).Update(context.Background(), updateRequest(storedProperties(), desired))
	require.Error(t, err)
	client.AssertNotCalled(t, "UpdatePrivateDnsNamespace", mock.Anything, mock.Anything)
	client.AssertNotCalled(t, "TagResource", mock.Anything, mock.Anything)
}

// A create adopted by name has no operation id of its own, and a namespace
// record exists from the moment the create is accepted — including for a create
// that goes on to fail and is rolled back. Reporting success on the strength of
// the namespace being retrievable would report such a create as done, so the
// operation behind the namespace is recovered and reported on instead.
func TestStatusFailsAnAdoptedNamespaceWhoseCreateOperationFailed(t *testing.T) {
	client := &mockServiceDiscoveryClient{}
	client.On("ListOperations", mock.Anything, mock.Anything).
		Return(&servicediscoverysdk.ListOperationsOutput{
			Operations: []servicediscoverytypes.OperationSummary{
				{Id: aws.String("op-adopted"), Status: servicediscoverytypes.OperationStatusFail},
			},
		}, nil)
	failed := operationOutput(servicediscoverytypes.OperationStatusFail, map[string]string{"NAMESPACE": "ns-abc"})
	failed.Operation.ErrorCode = aws.String("CANNOT_CREATE_HOSTED_ZONE")
	client.On("GetOperation", mock.Anything, &servicediscoverysdk.GetOperationInput{OperationId: aws.String("op-adopted")}).
		Return(failed, nil)

	result, err := newTestNamespace(client).Status(context.Background(), &resource.StatusRequest{
		RequestID:    encodeRequestID(requestState{Deadline: testNow.Add(time.Minute)}),
		NativeID:     "ns-abc",
		ResourceType: resourceType,
	})
	require.NoError(t, err)

	assert.Equal(t, resource.OperationStatusFailure, result.ProgressResult.OperationStatus)
	assert.Contains(t, result.ProgressResult.StatusMessage, "CANNOT_CREATE_HOSTED_ZONE")
	client.AssertNotCalled(t, "GetNamespace", mock.Anything, mock.Anything)

	assert.Equal(t, []servicediscoverytypes.OperationFilter{
		{
			Name:      servicediscoverytypes.OperationFilterNameNamespaceId,
			Values:    []string{"ns-abc"},
			Condition: servicediscoverytypes.FilterConditionEq,
		},
		{
			Name:      servicediscoverytypes.OperationFilterNameType,
			Values:    []string{string(servicediscoverytypes.OperationTypeCreateNamespace)},
			Condition: servicediscoverytypes.FilterConditionEq,
		},
	}, capturedListOperationsInput(t, client).Filters)
}

func TestStatusReportsAnAdoptedNamespaceReadyWhenItsCreateOperationSucceeded(t *testing.T) {
	client := namespaceReader()
	client.On("ListOperations", mock.Anything, mock.Anything).
		Return(&servicediscoverysdk.ListOperationsOutput{
			Operations: []servicediscoverytypes.OperationSummary{
				{Id: aws.String("op-adopted"), Status: servicediscoverytypes.OperationStatusSuccess},
			},
		}, nil)
	client.On("GetOperation", mock.Anything, &servicediscoverysdk.GetOperationInput{OperationId: aws.String("op-adopted")}).
		Return(operationOutput(servicediscoverytypes.OperationStatusSuccess, map[string]string{"NAMESPACE": "ns-abc"}), nil)

	result, err := newTestNamespace(client).Status(context.Background(), &resource.StatusRequest{
		RequestID:    encodeRequestID(requestState{Deadline: testNow.Add(time.Minute)}),
		NativeID:     "ns-abc",
		ResourceType: resourceType,
	})
	require.NoError(t, err)

	assert.Equal(t, resource.OperationStatusSuccess, result.ProgressResult.OperationStatus)
	properties := decodedProperties(t, string(result.ProgressResult.ResourceProperties))
	assert.Equal(t, "ns-abc", properties["Id"])
	assert.Equal(t, "Z0123456789ABCDEFGHIJ", properties["HostedZoneId"])
}

// An adopted create is still running while its operation is, and the deadline
// the RequestID carries is what bounds the wait.
func TestStatusWaitsForTheRecoveredCreateOperationOfAnAdoptedNamespace(t *testing.T) {
	client := &mockServiceDiscoveryClient{}
	client.On("ListOperations", mock.Anything, mock.Anything).
		Return(&servicediscoverysdk.ListOperationsOutput{
			Operations: []servicediscoverytypes.OperationSummary{
				{Id: aws.String("op-adopted"), Status: servicediscoverytypes.OperationStatusPending},
			},
		}, nil)
	client.On("GetOperation", mock.Anything, mock.Anything).
		Return(operationOutput(servicediscoverytypes.OperationStatusPending, nil), nil)

	result, err := newTestNamespace(client).Status(context.Background(), &resource.StatusRequest{
		RequestID: encodeRequestID(requestState{Deadline: testNow.Add(time.Minute)}),
		NativeID:  "ns-abc",
	})
	require.NoError(t, err)

	assert.Equal(t, resource.OperationStatusInProgress, result.ProgressResult.OperationStatus)
	client.AssertNotCalled(t, "GetNamespace", mock.Anything, mock.Anything)
}
