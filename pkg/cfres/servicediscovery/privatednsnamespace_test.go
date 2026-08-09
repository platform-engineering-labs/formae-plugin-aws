// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package servicediscovery

import (
	"context"
	"encoding/json"
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

func capturedCreateInput(t *testing.T, client *mockServiceDiscoveryClient) *servicediscoverysdk.CreatePrivateDnsNamespaceInput {
	t.Helper()
	for _, call := range client.Calls {
		if call.Method == "CreatePrivateDnsNamespace" {
			return call.Arguments.Get(1).(*servicediscoverysdk.CreatePrivateDnsNamespaceInput)
		}
	}
	t.Fatal("CreatePrivateDnsNamespace was never called")
	return nil
}

func capturedListInput(t *testing.T, client *mockServiceDiscoveryClient) *servicediscoverysdk.ListNamespacesInput {
	t.Helper()
	for _, call := range client.Calls {
		if call.Method == "ListNamespaces" {
			return call.Arguments.Get(1).(*servicediscoverysdk.ListNamespacesInput)
		}
	}
	t.Fatal("ListNamespaces was never called")
	return nil
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
	client := &mockServiceDiscoveryClient{}
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
// it, so Status confirms the namespace itself instead.
func TestStatusFallsBackToTheNamespaceWhenNoOperationIsKnown(t *testing.T) {
	client := &mockServiceDiscoveryClient{}
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
