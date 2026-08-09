// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package ccx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudcontrol"
	cctypes "github.com/aws/aws-sdk-go-v2/service/cloudcontrol/types"
	"github.com/platform-engineering-labs/formae/pkg/plugin"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/ptr"
)

func TestStripIgnoredFields(t *testing.T) {
	jsonPayload := []byte(`{
	"foo": "value to ignore",
	"bar": "another value",
	"baz": {
		"qux": "value to ignore",
		"quux": "value to keep"
	}
}`)
	unmarshaled := make(map[string]any)
	err := json.Unmarshal(jsonPayload, &unmarshaled)
	require.NoError(t, err)

	ignoredFields := []string{"$.foo", "$.baz.qux"}

	err = stripIgnoredFields(unmarshaled, ignoredFields)
	require.NoError(t, err)

	require.NotContains(t, unmarshaled, "foo")
	require.Contains(t, unmarshaled, "bar")
	require.Contains(t, unmarshaled, "baz")
	require.NotContains(t, unmarshaled["baz"].(map[string]any), "qux")
	require.Contains(t, unmarshaled["baz"].(map[string]any), "quux")
}

// mockCloudControlAPI is a testify mock for the cloudControlAPI interface.
type mockCloudControlAPI struct {
	mock.Mock
}

func (m *mockCloudControlAPI) CreateResource(ctx context.Context, params *cloudcontrol.CreateResourceInput, optFns ...func(*cloudcontrol.Options)) (*cloudcontrol.CreateResourceOutput, error) {
	args := m.Called(ctx, params)
	return args.Get(0).(*cloudcontrol.CreateResourceOutput), args.Error(1)
}

func (m *mockCloudControlAPI) UpdateResource(ctx context.Context, params *cloudcontrol.UpdateResourceInput, optFns ...func(*cloudcontrol.Options)) (*cloudcontrol.UpdateResourceOutput, error) {
	args := m.Called(ctx, params)
	return args.Get(0).(*cloudcontrol.UpdateResourceOutput), args.Error(1)
}

func (m *mockCloudControlAPI) DeleteResource(ctx context.Context, params *cloudcontrol.DeleteResourceInput, optFns ...func(*cloudcontrol.Options)) (*cloudcontrol.DeleteResourceOutput, error) {
	args := m.Called(ctx, params)
	return args.Get(0).(*cloudcontrol.DeleteResourceOutput), args.Error(1)
}

func (m *mockCloudControlAPI) GetResource(ctx context.Context, params *cloudcontrol.GetResourceInput, optFns ...func(*cloudcontrol.Options)) (*cloudcontrol.GetResourceOutput, error) {
	args := m.Called(ctx, params)
	return args.Get(0).(*cloudcontrol.GetResourceOutput), args.Error(1)
}

func (m *mockCloudControlAPI) GetResourceRequestStatus(ctx context.Context, params *cloudcontrol.GetResourceRequestStatusInput, optFns ...func(*cloudcontrol.Options)) (*cloudcontrol.GetResourceRequestStatusOutput, error) {
	args := m.Called(ctx, params)
	return args.Get(0).(*cloudcontrol.GetResourceRequestStatusOutput), args.Error(1)
}

func (m *mockCloudControlAPI) ListResources(ctx context.Context, params *cloudcontrol.ListResourcesInput, optFns ...func(*cloudcontrol.Options)) (*cloudcontrol.ListResourcesOutput, error) {
	args := m.Called(ctx, params)
	return args.Get(0).(*cloudcontrol.ListResourcesOutput), args.Error(1)
}

func TestCreateResource_SetsNativeID(t *testing.T) {
	mockAPI := new(mockCloudControlAPI)
	client := &Client{api: mockAPI}

	mockAPI.On("CreateResource", mock.Anything, mock.Anything).Return(
		&cloudcontrol.CreateResourceOutput{
			ProgressEvent: &cctypes.ProgressEvent{
				OperationStatus: cctypes.OperationStatusSuccess,
				RequestToken:    ptr.Of("req-token-123"),
				Identifier:      ptr.Of("fl-test123"),
			},
		}, nil,
	)

	// Post-success Read
	mockAPI.On("GetResource", mock.Anything, mock.Anything).Return(&cloudcontrol.GetResourceOutput{
		ResourceDescription: &cctypes.ResourceDescription{
			Identifier: ptr.Of("fl-test123"),
			Properties: ptr.Of(`{"LogGroupName":"test","FlowLogId":"fl-test123"}`),
		},
		TypeName: ptr.Of("AWS::EC2::FlowLog"),
	}, nil)

	result, err := client.CreateResource(context.Background(), &resource.CreateRequest{
		ResourceType: "AWS::EC2::FlowLog",
		Properties:   json.RawMessage(`{"LogGroupName": "test"}`),
	})

	require.NoError(t, err)
	require.Equal(t, "fl-test123", result.ProgressResult.NativeID)
}

func TestCreateResource_SynchronousSuccess_PopulatesResourceProperties(t *testing.T) {
	mockAPI := new(mockCloudControlAPI)
	client := &Client{api: mockAPI}

	mockAPI.On("CreateResource", mock.Anything, mock.Anything).Return(
		&cloudcontrol.CreateResourceOutput{
			ProgressEvent: &cctypes.ProgressEvent{
				OperationStatus: cctypes.OperationStatusSuccess,
				RequestToken:    ptr.Of("req-token-123"),
				Identifier:      ptr.Of("fl-test123"),
			},
		}, nil,
	)

	// GetResource (post-success Read) returns full properties
	mockAPI.On("GetResource", mock.Anything, mock.MatchedBy(func(input *cloudcontrol.GetResourceInput) bool {
		return *input.Identifier == "fl-test123" && *input.TypeName == "AWS::EC2::FlowLog"
	})).Return(&cloudcontrol.GetResourceOutput{
		ResourceDescription: &cctypes.ResourceDescription{
			Identifier: ptr.Of("fl-test123"),
			Properties: ptr.Of(`{"LogGroupName":"test","FlowLogId":"fl-test123","ResourceType":"VPC"}`),
		},
		TypeName: ptr.Of("AWS::EC2::FlowLog"),
	}, nil)

	result, err := client.CreateResource(context.Background(), &resource.CreateRequest{
		ResourceType: "AWS::EC2::FlowLog",
		Properties:   json.RawMessage(`{"LogGroupName": "test"}`),
	})

	require.NoError(t, err)
	require.Equal(t, "fl-test123", result.ProgressResult.NativeID)
	require.NotNil(t, result.ProgressResult.ResourceProperties)
	require.Contains(t, string(result.ProgressResult.ResourceProperties), "FlowLogId")
}

func TestCreateResource_InProgress_NilIdentifier(t *testing.T) {
	mockAPI := new(mockCloudControlAPI)
	client := &Client{api: mockAPI}

	mockAPI.On("CreateResource", mock.Anything, mock.Anything).Return(
		&cloudcontrol.CreateResourceOutput{
			ProgressEvent: &cctypes.ProgressEvent{
				OperationStatus: cctypes.OperationStatusInProgress,
				RequestToken:    ptr.Of("req-token-456"),
				Identifier:      nil,
			},
		}, nil,
	)

	result, err := client.CreateResource(context.Background(), &resource.CreateRequest{
		ResourceType: "AWS::EC2::FlowLog",
		Properties:   json.RawMessage(`{"LogGroupName": "test"}`),
	})

	require.NoError(t, err)
	require.Equal(t, "", result.ProgressResult.NativeID)
}

func TestStatusResource_TGCreateRace_RemapsToInProgress(t *testing.T) {
	mockAPI := new(mockCloudControlAPI)
	client := &Client{api: mockAPI}

	mockAPI.On("GetResourceRequestStatus", mock.Anything, mock.Anything).Return(
		&cloudcontrol.GetResourceRequestStatusOutput{
			ProgressEvent: &cctypes.ProgressEvent{
				Operation:       cctypes.OperationCreate,
				OperationStatus: cctypes.OperationStatusFailed,
				ErrorCode:       cctypes.HandlerErrorCodeInvalidRequest,
				StatusMessage:   ptr.Of("The target group with targetGroupArn arn:aws:elasticloadbalancing:us-west-2:123:targetgroup/foo/abc does not have an associated load balancer."),
				TypeName:        ptr.Of("AWS::ECS::Service"),
			},
		}, nil,
	)

	result, err := client.StatusResource(
		context.Background(),
		&resource.StatusRequest{RequestID: "req-token-tg-race"},
		func(_ context.Context, _ *resource.ReadRequest) (*resource.ReadResult, error) {
			t.Fatalf("readFunc should not be called when remapping to InProgress")
			return nil, nil
		},
	)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, resource.OperationStatusInProgress, result.ProgressResult.OperationStatus,
		"'TG not associated' on Create must remap to InProgress so PluginOperator keeps polling")
}

func TestStatusResource_TGCreateRace_NotRemappedOnUpdate(t *testing.T) {
	mockAPI := new(mockCloudControlAPI)
	client := &Client{api: mockAPI}

	mockAPI.On("GetResourceRequestStatus", mock.Anything, mock.Anything).Return(
		&cloudcontrol.GetResourceRequestStatusOutput{
			ProgressEvent: &cctypes.ProgressEvent{
				Operation:       cctypes.OperationUpdate,
				OperationStatus: cctypes.OperationStatusFailed,
				ErrorCode:       cctypes.HandlerErrorCodeInvalidRequest,
				StatusMessage:   ptr.Of("The target group with targetGroupArn arn:aws:elasticloadbalancing:us-west-2:123:targetgroup/foo/abc does not have an associated load balancer."),
				TypeName:        ptr.Of("AWS::ECS::Service"),
			},
		}, nil,
	)

	result, err := client.StatusResource(
		context.Background(),
		&resource.StatusRequest{RequestID: "req-token-tg-race-update"},
		func(_ context.Context, _ *resource.ReadRequest) (*resource.ReadResult, error) {
			return nil, nil
		},
	)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotEqual(t, resource.OperationStatusInProgress, result.ProgressResult.OperationStatus,
		"'TG not associated' on Update is a different state (not the create-vs-listener race) — must not remap")
}

func TestStatusResource_TGCreateRace_NotRemappedOnDifferentErrorCode(t *testing.T) {
	mockAPI := new(mockCloudControlAPI)
	client := &Client{api: mockAPI}

	mockAPI.On("GetResourceRequestStatus", mock.Anything, mock.Anything).Return(
		&cloudcontrol.GetResourceRequestStatusOutput{
			ProgressEvent: &cctypes.ProgressEvent{
				Operation:       cctypes.OperationCreate,
				OperationStatus: cctypes.OperationStatusFailed,
				ErrorCode:       cctypes.HandlerErrorCodeAccessDenied,
				StatusMessage:   ptr.Of("The target group with targetGroupArn arn:aws:elasticloadbalancing:us-west-2:123:targetgroup/foo/abc does not have an associated load balancer."),
				TypeName:        ptr.Of("AWS::ECS::Service"),
			},
		}, nil,
	)

	result, err := client.StatusResource(
		context.Background(),
		&resource.StatusRequest{RequestID: "req-token-tg-race-wrong-code"},
		func(_ context.Context, _ *resource.ReadRequest) (*resource.ReadResult, error) {
			return nil, nil
		},
	)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotEqual(t, resource.OperationStatusInProgress, result.ProgressResult.OperationStatus,
		"matching message text under a different error code must not remap — code is the safety rail")
}

func TestStatusResource_TGCreateRace_NotRemappedOnUnrelatedMessage(t *testing.T) {
	mockAPI := new(mockCloudControlAPI)
	client := &Client{api: mockAPI}

	mockAPI.On("GetResourceRequestStatus", mock.Anything, mock.Anything).Return(
		&cloudcontrol.GetResourceRequestStatusOutput{
			ProgressEvent: &cctypes.ProgressEvent{
				Operation:       cctypes.OperationCreate,
				OperationStatus: cctypes.OperationStatusFailed,
				ErrorCode:       cctypes.HandlerErrorCodeInvalidRequest,
				StatusMessage:   ptr.Of("Some other validation error about a different field"),
				TypeName:        ptr.Of("AWS::ECS::Service"),
			},
		}, nil,
	)

	result, err := client.StatusResource(
		context.Background(),
		&resource.StatusRequest{RequestID: "req-token-tg-race-wrong-msg"},
		func(_ context.Context, _ *resource.ReadRequest) (*resource.ReadResult, error) {
			return nil, nil
		},
	)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotEqual(t, resource.OperationStatusInProgress, result.ProgressResult.OperationStatus,
		"InvalidRequest on Create with an unrelated message must not remap — message is the discriminator")
}

func TestUpdateResource_SynchronousSuccess_PopulatesResourceProperties(t *testing.T) {
	mockAPI := new(mockCloudControlAPI)
	client := &Client{api: mockAPI}

	nativeID := "my-queue-url"
	resourceType := "AWS::SQS::QueueInlinePolicy"
	patchDoc := `[{"op":"replace","path":"/PolicyDocument","value":{"Statement":[{"Effect":"Allow","Action":"sqs:*","Resource":"*"}]}}]`

	// GetResource (existence check) returns success
	mockAPI.On("GetResource", mock.Anything, mock.MatchedBy(func(input *cloudcontrol.GetResourceInput) bool {
		return *input.Identifier == nativeID && *input.TypeName == resourceType
	})).Return(&cloudcontrol.GetResourceOutput{
		ResourceDescription: &cctypes.ResourceDescription{
			Identifier: ptr.Of(nativeID),
			Properties: ptr.Of(`{"PolicyDocument":{"Statement":[{"Effect":"Deny","Action":"sqs:*","Resource":"*"}]}}`),
		},
		TypeName: ptr.Of(resourceType),
	}, nil)

	// UpdateResource returns synchronous SUCCESS
	mockAPI.On("UpdateResource", mock.Anything, mock.Anything).Return(
		&cloudcontrol.UpdateResourceOutput{
			ProgressEvent: &cctypes.ProgressEvent{
				OperationStatus: cctypes.OperationStatusSuccess,
				RequestToken:    ptr.Of("req-token-update"),
				Identifier:      ptr.Of(nativeID),
			},
		}, nil,
	)

	result, err := client.UpdateResource(context.Background(), &resource.UpdateRequest{
		NativeID:      nativeID,
		ResourceType:  resourceType,
		PatchDocument: ptr.Of(patchDoc),
	})

	require.NoError(t, err)
	require.Equal(t, resource.OperationStatusSuccess, result.ProgressResult.OperationStatus)
	// The key assertion: ResourceProperties should be populated from a post-update Read
	require.NotNil(t, result.ProgressResult.ResourceProperties)
	require.Contains(t, string(result.ProgressResult.ResourceProperties), "PolicyDocument")
	require.Contains(t, string(result.ProgressResult.ResourceProperties), "Statement")
}

func TestUpdateResource_InProgress_DoesNotRead(t *testing.T) {
	mockAPI := new(mockCloudControlAPI)
	client := &Client{api: mockAPI}

	nativeID := "my-queue-url"
	resourceType := "AWS::SQS::QueueInlinePolicy"

	// GetResource (existence check) returns success
	mockAPI.On("GetResource", mock.Anything, mock.Anything).Return(&cloudcontrol.GetResourceOutput{
		ResourceDescription: &cctypes.ResourceDescription{
			Identifier: ptr.Of(nativeID),
			Properties: ptr.Of(`{}`),
		},
		TypeName: ptr.Of(resourceType),
	}, nil)

	// UpdateResource returns IN_PROGRESS (async)
	mockAPI.On("UpdateResource", mock.Anything, mock.Anything).Return(
		&cloudcontrol.UpdateResourceOutput{
			ProgressEvent: &cctypes.ProgressEvent{
				OperationStatus: cctypes.OperationStatusInProgress,
				RequestToken:    ptr.Of("req-token-async"),
				Identifier:      ptr.Of(nativeID),
			},
		}, nil,
	)

	result, err := client.UpdateResource(context.Background(), &resource.UpdateRequest{
		NativeID:      nativeID,
		ResourceType:  resourceType,
		PatchDocument: ptr.Of(`[{"op":"replace","path":"/PolicyDocument","value":{}}]`),
	})

	require.NoError(t, err)
	require.Equal(t, resource.OperationStatusInProgress, result.ProgressResult.OperationStatus)
	// For in-progress, ResourceProperties should NOT be populated — StatusResource handles that
	require.Nil(t, result.ProgressResult.ResourceProperties)
	// GetResource should only be called once (existence check), not twice (no post-update Read)
	mockAPI.AssertNumberOfCalls(t, "GetResource", 1)
}

// testWatchedBudget stands in for the production watched-call budget so the
// budget-exhaustion tests exit on the first declined backoff instead of
// burning seconds of wall clock.
const testWatchedBudget = 25 * time.Millisecond

// throttledGetResource makes every subsequent GetResource throttle — the
// recoverable condition the enrichment retry loop rides until the watched-call
// budget stops it.
func throttledGetResource(mockAPI *mockCloudControlAPI) {
	mockAPI.On("GetResource", mock.Anything, mock.Anything).Return(
		(*cloudcontrol.GetResourceOutput)(nil),
		ccOpError(&cctypes.ThrottlingException{Message: aws.String("Rate exceeded")}),
	)
}

func TestCreateResource_SynchronousSuccess_EnrichmentBudgetExhausted_ReturnsInProgress(t *testing.T) {
	mockAPI := new(mockCloudControlAPI)
	client := &Client{api: mockAPI, watchedBudget: testWatchedBudget}

	mockAPI.On("CreateResource", mock.Anything, mock.Anything).Return(
		&cloudcontrol.CreateResourceOutput{
			ProgressEvent: &cctypes.ProgressEvent{
				OperationStatus: cctypes.OperationStatusSuccess,
				RequestToken:    ptr.Of("req-token-throttled"),
				Identifier:      ptr.Of("fl-test123"),
			},
		}, nil,
	)
	throttledGetResource(mockAPI)

	result, err := client.CreateResource(context.Background(), &resource.CreateRequest{
		ResourceType: "AWS::EC2::FlowLog",
		Properties:   json.RawMessage(`{"LogGroupName": "test"}`),
	})

	require.NoError(t, err)
	require.Equal(t, resource.OperationStatusInProgress, result.ProgressResult.OperationStatus,
		"CloudControl already reported the create as successful — enrichment that outran its budget must be handed to the poll loop, not blocked on")
	require.Equal(t, "req-token-throttled", result.ProgressResult.RequestID,
		"the request token must survive the conversion so the operator polls the same request")
	require.Equal(t, "fl-test123", result.ProgressResult.NativeID)
	require.Nil(t, result.ProgressResult.ResourceProperties,
		"no properties were read, so none may be fabricated")
	require.Empty(t, string(result.ProgressResult.ErrorCode),
		"an InProgress conversion is not an error condition")
}

func TestCreateResource_SynchronousSuccess_NonRecoverableReadError_StaysSuccess(t *testing.T) {
	mockAPI := new(mockCloudControlAPI)
	client := &Client{api: mockAPI, watchedBudget: testWatchedBudget}

	mockAPI.On("CreateResource", mock.Anything, mock.Anything).Return(
		&cloudcontrol.CreateResourceOutput{
			ProgressEvent: &cctypes.ProgressEvent{
				OperationStatus: cctypes.OperationStatusSuccess,
				RequestToken:    ptr.Of("req-token-unreadable"),
				Identifier:      ptr.Of("fl-test123"),
			},
		}, nil,
	)
	mockAPI.On("GetResource", mock.Anything, mock.Anything).Return(
		(*cloudcontrol.GetResourceOutput)(nil),
		ccOpError(&cctypes.ResourceNotFoundException{Message: aws.String("not found")}),
	)

	result, err := client.CreateResource(context.Background(), &resource.CreateRequest{
		ResourceType: "AWS::EC2::FlowLog",
		Properties:   json.RawMessage(`{"LogGroupName": "test"}`),
	})

	require.NoError(t, err)
	require.Equal(t, resource.OperationStatusSuccess, result.ProgressResult.OperationStatus,
		"polling cannot fix a non-recoverable read error, so the create stays Success")
	require.Equal(t, "fl-test123", result.ProgressResult.NativeID)
	require.Nil(t, result.ProgressResult.ResourceProperties)
	mockAPI.AssertNumberOfCalls(t, "GetResource", 1)
}

func TestUpdateResource_SynchronousSuccess_EnrichmentBudgetExhausted_ReturnsInProgress(t *testing.T) {
	mockAPI := new(mockCloudControlAPI)
	client := &Client{api: mockAPI, watchedBudget: testWatchedBudget}

	nativeID := "my-queue-url"
	resourceType := "AWS::SQS::QueueInlinePolicy"

	// The existence pre-check succeeds; every later Read (the enrichment one)
	// is throttled.
	mockAPI.On("GetResource", mock.Anything, mock.Anything).Return(&cloudcontrol.GetResourceOutput{
		ResourceDescription: &cctypes.ResourceDescription{
			Identifier: ptr.Of(nativeID),
			Properties: ptr.Of(`{"PolicyDocument":{}}`),
		},
		TypeName: ptr.Of(resourceType),
	}, nil).Once()
	throttledGetResource(mockAPI)

	mockAPI.On("UpdateResource", mock.Anything, mock.Anything).Return(
		&cloudcontrol.UpdateResourceOutput{
			ProgressEvent: &cctypes.ProgressEvent{
				OperationStatus: cctypes.OperationStatusSuccess,
				RequestToken:    ptr.Of("req-token-update-throttled"),
				Identifier:      ptr.Of(nativeID),
			},
		}, nil,
	)

	result, err := client.UpdateResource(context.Background(), &resource.UpdateRequest{
		NativeID:      nativeID,
		ResourceType:  resourceType,
		PatchDocument: ptr.Of(`[{"op":"replace","path":"/PolicyDocument","value":{"Statement":[]}}]`),
	})

	require.NoError(t, err)
	require.Equal(t, resource.OperationStatusInProgress, result.ProgressResult.OperationStatus,
		"CloudControl already reported the update as successful — enrichment that outran its budget must be handed to the poll loop, not blocked on")
	require.Equal(t, "req-token-update-throttled", result.ProgressResult.RequestID)
	require.Equal(t, nativeID, result.ProgressResult.NativeID)
	require.Nil(t, result.ProgressResult.ResourceProperties)
	require.Empty(t, string(result.ProgressResult.ErrorCode))
}

func TestUpdateResource_SynchronousSuccess_NonRecoverableReadError_StaysSuccess(t *testing.T) {
	mockAPI := new(mockCloudControlAPI)
	client := &Client{api: mockAPI, watchedBudget: testWatchedBudget}

	nativeID := "my-queue-url"
	resourceType := "AWS::SQS::QueueInlinePolicy"

	mockAPI.On("GetResource", mock.Anything, mock.Anything).Return(&cloudcontrol.GetResourceOutput{
		ResourceDescription: &cctypes.ResourceDescription{
			Identifier: ptr.Of(nativeID),
			Properties: ptr.Of(`{"PolicyDocument":{}}`),
		},
		TypeName: ptr.Of(resourceType),
	}, nil).Once()
	mockAPI.On("GetResource", mock.Anything, mock.Anything).Return(
		(*cloudcontrol.GetResourceOutput)(nil),
		ccOpError(&cctypes.ResourceNotFoundException{Message: aws.String("not found")}),
	)

	mockAPI.On("UpdateResource", mock.Anything, mock.Anything).Return(
		&cloudcontrol.UpdateResourceOutput{
			ProgressEvent: &cctypes.ProgressEvent{
				OperationStatus: cctypes.OperationStatusSuccess,
				RequestToken:    ptr.Of("req-token-update-unreadable"),
				Identifier:      ptr.Of(nativeID),
			},
		}, nil,
	)

	result, err := client.UpdateResource(context.Background(), &resource.UpdateRequest{
		NativeID:      nativeID,
		ResourceType:  resourceType,
		PatchDocument: ptr.Of(`[{"op":"replace","path":"/PolicyDocument","value":{"Statement":[]}}]`),
	})

	require.NoError(t, err)
	require.Equal(t, resource.OperationStatusSuccess, result.ProgressResult.OperationStatus,
		"polling cannot fix a non-recoverable read error, so the update stays Success")
	require.Equal(t, nativeID, result.ProgressResult.NativeID)
	require.Nil(t, result.ProgressResult.ResourceProperties)
	mockAPI.AssertNumberOfCalls(t, "GetResource", 2)
}

func TestCreateResource_SyncCloudControlError_ReturnsFailureProgress(t *testing.T) {
	mockAPI := new(mockCloudControlAPI)
	client := &Client{api: mockAPI}

	awsErr := ccOpError(&cctypes.InvalidRequestException{
		Message: aws.String("DesiredState contains an unknown property 'Foo'"),
	})
	mockAPI.On("CreateResource", mock.Anything, mock.Anything).Return(
		(*cloudcontrol.CreateResourceOutput)(nil), awsErr,
	)

	result, err := client.CreateResource(context.Background(), &resource.CreateRequest{
		ResourceType: "AWS::EC2::FlowLog",
		Properties:   json.RawMessage(`{}`),
	})

	require.NoError(t, err, "classified CC errors must surface via ProgressResult, not as a raw Go error")
	require.NotNil(t, result)
	require.NotNil(t, result.ProgressResult)
	require.Equal(t, resource.OperationStatusFailure, result.ProgressResult.OperationStatus)
	require.Equal(t, resource.OperationErrorCodeInvalidRequest, result.ProgressResult.ErrorCode)
	require.Equal(t, resource.OperationCreate, result.ProgressResult.Operation)
}

func TestCreateResource_RDSSubnetEventualConsistency_RemapsToResourceConflict(t *testing.T) {
	mockAPI := new(mockCloudControlAPI)
	client := &Client{api: mockAPI}

	awsErr := ccOpError(&cctypes.InvalidRequestException{
		Message: aws.String("Some input subnets in :[subnet-0f7a9adc9560fae45, subnet-07544dcf861d1f761] are invalid."),
	})
	mockAPI.On("CreateResource", mock.Anything, mock.Anything).Return(
		(*cloudcontrol.CreateResourceOutput)(nil), awsErr,
	)

	result, err := client.CreateResource(context.Background(), &resource.CreateRequest{
		ResourceType: "AWS::RDS::DBSubnetGroup",
		Properties:   json.RawMessage(`{"DBSubnetGroupName":"test","SubnetIds":["subnet-x","subnet-y"]}`),
	})

	require.NoError(t, err)
	require.NotNil(t, result.ProgressResult)
	require.Equal(t, resource.OperationStatusFailure, result.ProgressResult.OperationStatus)
	require.Equal(t, resource.OperationErrorCodeResourceConflict, result.ProgressResult.ErrorCode,
		"AWS subnet-invalid-after-create is eventual consistency; must remap to ResourceConflict so PluginOperator retries")
}

func TestCreateResource_NonCloudControlError_BubblesAsRawError(t *testing.T) {
	mockAPI := new(mockCloudControlAPI)
	client := &Client{api: mockAPI}

	mockAPI.On("CreateResource", mock.Anything, mock.Anything).Return(
		(*cloudcontrol.CreateResourceOutput)(nil), errors.New("transport: connection reset"),
	)

	result, err := client.CreateResource(context.Background(), &resource.CreateRequest{
		ResourceType: "AWS::EC2::FlowLog",
		Properties:   json.RawMessage(`{}`),
	})

	require.Error(t, err, "unclassified errors must bubble as raw Go errors so the agent tags them UnforeseenError")
	require.Nil(t, result)
}

func TestUpdateResource_SyncCloudControlError_ReturnsFailureProgress(t *testing.T) {
	mockAPI := new(mockCloudControlAPI)
	client := &Client{api: mockAPI}

	// Existence pre-check succeeds.
	mockAPI.On("GetResource", mock.Anything, mock.Anything).Return(
		&cloudcontrol.GetResourceOutput{
			ResourceDescription: &cctypes.ResourceDescription{Properties: ptr.Of(`{}`)},
		}, nil,
	)
	// Actual update fails with a recoverable Throttling exception.
	mockAPI.On("UpdateResource", mock.Anything, mock.Anything).Return(
		(*cloudcontrol.UpdateResourceOutput)(nil),
		ccOpError(&cctypes.ThrottlingException{Message: aws.String("Rate exceeded")}),
	)

	result, err := client.UpdateResource(context.Background(), &resource.UpdateRequest{
		ResourceType:  "AWS::EC2::FlowLog",
		NativeID:      "fl-test",
		PatchDocument: ptr.Of(`[]`),
	})

	require.NoError(t, err)
	require.NotNil(t, result.ProgressResult)
	require.Equal(t, resource.OperationStatusFailure, result.ProgressResult.OperationStatus)
	require.Equal(t, resource.OperationErrorCodeThrottling, result.ProgressResult.ErrorCode)
	require.True(t, resource.IsRecoverable(result.ProgressResult.ErrorCode))
}

func TestUpdateResource_GetResourcePrecheckCloudControlError_ReturnsFailureProgress(t *testing.T) {
	mockAPI := new(mockCloudControlAPI)
	client := &Client{api: mockAPI}

	mockAPI.On("GetResource", mock.Anything, mock.Anything).Return(
		(*cloudcontrol.GetResourceOutput)(nil),
		ccOpError(&cctypes.ResourceNotFoundException{Message: aws.String("not found")}),
	)

	result, err := client.UpdateResource(context.Background(), &resource.UpdateRequest{
		ResourceType:  "AWS::EC2::FlowLog",
		NativeID:      "fl-missing",
		PatchDocument: ptr.Of(`[]`),
	})

	require.NoError(t, err)
	require.NotNil(t, result.ProgressResult)
	require.Equal(t, resource.OperationStatusFailure, result.ProgressResult.OperationStatus)
	require.Equal(t, resource.OperationErrorCodeNotFound, result.ProgressResult.ErrorCode)
	require.Equal(t, resource.OperationUpdate, result.ProgressResult.Operation)
	mockAPI.AssertNotCalled(t, "UpdateResource", mock.Anything, mock.Anything)
}

func TestDeleteResource_SyncCloudControlError_ReturnsFailureProgress(t *testing.T) {
	mockAPI := new(mockCloudControlAPI)
	client := &Client{api: mockAPI}

	mockAPI.On("DeleteResource", mock.Anything, mock.Anything).Return(
		(*cloudcontrol.DeleteResourceOutput)(nil),
		ccOpError(&cctypes.ThrottlingException{Message: aws.String("Rate exceeded")}),
	)

	result, err := client.DeleteResource(context.Background(), &resource.DeleteRequest{
		ResourceType: "AWS::EC2::FlowLog",
		NativeID:     "fl-test",
	})

	require.NoError(t, err)
	require.NotNil(t, result.ProgressResult)
	require.Equal(t, resource.OperationStatusFailure, result.ProgressResult.OperationStatus)
	require.Equal(t, resource.OperationErrorCodeThrottling, result.ProgressResult.ErrorCode)
	require.Equal(t, resource.OperationDelete, result.ProgressResult.Operation)
}

func TestCreateResource_Success_NilIdentifier_ReturnsError(t *testing.T) {
	mockAPI := new(mockCloudControlAPI)
	client := &Client{api: mockAPI}

	mockAPI.On("CreateResource", mock.Anything, mock.Anything).Return(
		&cloudcontrol.CreateResourceOutput{
			ProgressEvent: &cctypes.ProgressEvent{
				OperationStatus: cctypes.OperationStatusSuccess,
				RequestToken:    ptr.Of("req-token-789"),
				Identifier:      nil,
			},
		}, nil,
	)

	_, err := client.CreateResource(context.Background(), &resource.CreateRequest{
		ResourceType: "AWS::EC2::FlowLog",
		Properties:   json.RawMessage(`{"LogGroupName": "test"}`),
	})

	require.Error(t, err)
	require.Contains(t, err.Error(), "identifier")
}

func TestNormalizeCompositeIdentifier(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "simple identifier unchanged",
			input:    "vpc-12345",
			expected: "vpc-12345",
		},
		{
			name:     "ARN identifier unchanged",
			input:    "arn:aws:ecs:us-east-1:123456:service/cluster/svc",
			expected: "arn:aws:ecs:us-east-1:123456:service/cluster/svc",
		},
		{
			name:     "composite with ARN second part normalized",
			input:    "arn:aws:ecs:us-east-1:123456:service/my-cluster/my-svc|arn:aws:ecs:us-east-1:123456:cluster/my-cluster",
			expected: "arn:aws:ecs:us-east-1:123456:service/my-cluster/my-svc|my-cluster",
		},
		{
			name:     "composite already normalized",
			input:    "arn:aws:ecs:us-east-1:123456:service/my-cluster/my-svc|my-cluster",
			expected: "arn:aws:ecs:us-east-1:123456:service/my-cluster/my-svc|my-cluster",
		},
		{
			name:     "lambda event invoke config composite",
			input:    "arn:aws:lambda:us-east-1:123456:function:my-func|arn:aws:lambda:us-east-1:123456:function:my-func/$LATEST",
			expected: "arn:aws:lambda:us-east-1:123456:function:my-func|$LATEST",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := normalizeCompositeIdentifier(tt.input)
			if result != tt.expected {
				t.Errorf("normalizeCompositeIdentifier(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestFilterEmptyAddOps_ReplaceWithEmptyAfterStripping(t *testing.T) {
	// Simulates an EventInvokeConfig update where DestinationConfig has
	// provider-default empty OnSuccess/OnFailure. The replace operation's
	// value becomes empty after stripping and should be removed entirely,
	// otherwise CloudControl rejects it with:
	//   "required key [Destination] not found"
	patch := `[
		{"op":"replace","path":"/MaximumRetryAttempts","value":0},
		{"op":"replace","path":"/DestinationConfig","value":{"OnSuccess":{},"OnFailure":{}}}
	]`
	result, err := filterEmptyAddOps(patch)
	if err != nil {
		t.Fatalf("filterEmptyAddOps failed: %v", err)
	}

	var ops []map[string]any
	if err := json.Unmarshal([]byte(result), &ops); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}

	if len(ops) != 1 {
		t.Fatalf("expected 1 operation, got %d: %s", len(ops), result)
	}
	if ops[0]["path"] != "/MaximumRetryAttempts" {
		t.Errorf("expected remaining op to be MaximumRetryAttempts, got %v", ops[0]["path"])
	}
}

func TestFilterEmptyAddOps_ReplaceWithNonEmptyPreserved(t *testing.T) {
	// A replace with a non-empty value should be preserved
	patch := `[
		{"op":"replace","path":"/DestinationConfig","value":{"OnSuccess":{"Destination":"arn:aws:sqs:us-east-1:123:q"}}}
	]`
	result, err := filterEmptyAddOps(patch)
	if err != nil {
		t.Fatalf("filterEmptyAddOps failed: %v", err)
	}

	var ops []map[string]any
	if err := json.Unmarshal([]byte(result), &ops); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}

	if len(ops) != 1 {
		t.Fatalf("expected 1 operation, got %d: %s", len(ops), result)
	}
}

func TestStripEmptyCollectionsFromMap_NestedEmptyAfterRecursion(t *testing.T) {
	// Simulates DestinationConfig: {OnSuccess: {}, OnFailure: {}}
	// After recursive stripping, DestinationConfig should also be removed
	m := map[string]any{
		"MaximumRetryAttempts": float64(0),
		"DestinationConfig": map[string]any{
			"OnSuccess": map[string]any{},
			"OnFailure": map[string]any{},
		},
	}
	stripEmptyCollectionsFromMap(m)

	if _, exists := m["DestinationConfig"]; exists {
		t.Errorf("DestinationConfig should be stripped after recursive emptying, got %v", m)
	}
	if _, exists := m["MaximumRetryAttempts"]; !exists {
		t.Error("MaximumRetryAttempts should be preserved")
	}
}

func TestStripEmptyCollectionsFromMap_NestedNonEmpty(t *testing.T) {
	m := map[string]any{
		"DestinationConfig": map[string]any{
			"OnSuccess": map[string]any{
				"Destination": "arn:aws:sqs:us-east-1:123:my-queue",
			},
			"OnFailure": map[string]any{},
		},
	}
	stripEmptyCollectionsFromMap(m)

	dc, exists := m["DestinationConfig"].(map[string]any)
	if !exists {
		t.Fatal("DestinationConfig should be preserved when it has non-empty children")
	}
	if _, exists := dc["OnSuccess"]; !exists {
		t.Error("OnSuccess should be preserved")
	}
	if _, exists := dc["OnFailure"]; exists {
		t.Error("OnFailure should be stripped (empty)")
	}
}

func TestReadResource_StripsEmptyEventInvokeDestinationConfig(t *testing.T) {
	mockAPI := new(mockCloudControlAPI)
	client := &Client{api: mockAPI}

	// CloudControl injects DestinationConfig:{OnFailure:{},OnSuccess:{}} into
	// every EventInvokeConfig read, even when the caller never set it. Those
	// empty sub-objects carry no information (AWS requires Destination inside
	// OnFailure/OnSuccess), and absorbing them makes formae's required-field
	// validation spuriously fail.
	mockAPI.On("GetResource", mock.Anything, mock.Anything).Return(&cloudcontrol.GetResourceOutput{
		ResourceDescription: &cctypes.ResourceDescription{
			Identifier: ptr.Of("fn|$LATEST"),
			Properties: ptr.Of(`{"FunctionName":"fn","MaximumRetryAttempts":2,"DestinationConfig":{"OnSuccess":{},"OnFailure":{}}}`),
		},
		TypeName: ptr.Of("AWS::Lambda::EventInvokeConfig"),
	}, nil)

	result, err := client.ReadResource(context.Background(), &resource.ReadRequest{
		ResourceType: "AWS::Lambda::EventInvokeConfig",
		NativeID:     "fn|$LATEST",
	})
	require.NoError(t, err)
	require.NotContains(t, string(result.Properties), "DestinationConfig",
		"empty provider-injected DestinationConfig must be stripped on read")
	require.Contains(t, string(result.Properties), "MaximumRetryAttempts",
		"real properties must be preserved")
}

func TestReadResource_PreservesRealEventInvokeDestination(t *testing.T) {
	mockAPI := new(mockCloudControlAPI)
	client := &Client{api: mockAPI}

	// A genuine user-set destination must survive the strip; only the empty
	// sibling (OnSuccess:{}) should be removed.
	mockAPI.On("GetResource", mock.Anything, mock.Anything).Return(&cloudcontrol.GetResourceOutput{
		ResourceDescription: &cctypes.ResourceDescription{
			Identifier: ptr.Of("fn|$LATEST"),
			Properties: ptr.Of(`{"FunctionName":"fn","DestinationConfig":{"OnFailure":{"Destination":"arn:aws:sqs:us-east-1:123456789012:dlq"},"OnSuccess":{}}}`),
		},
		TypeName: ptr.Of("AWS::Lambda::EventInvokeConfig"),
	}, nil)

	result, err := client.ReadResource(context.Background(), &resource.ReadRequest{
		ResourceType: "AWS::Lambda::EventInvokeConfig",
		NativeID:     "fn|$LATEST",
	})
	require.NoError(t, err)
	require.Contains(t, string(result.Properties), "arn:aws:sqs:us-east-1:123456789012:dlq",
		"a genuine user-set destination must be preserved")
	require.NotContains(t, string(result.Properties), "OnSuccess",
		"the empty OnSuccess sibling should be stripped")
}

// captureSlog returns a context carrying a plugin.Logger that writes WARN+
// records into the returned buffer, so a test can drive StatusResource through
// the SDK context logger and inspect what it emitted. Returned buffer contains
// the rendered text output.
func captureSlog(t *testing.T) (context.Context, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	logger := plugin.NewPluginLogger(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	ctx := plugin.WithLogger(context.Background(), logger)
	return ctx, &buf
}

// When a Status poll comes back FAILED and none of the remap rules apply, the
// final ProgressEvent must be warn-logged with the fields needed to decide
// whether the error code should be remapped in a future patch (Operation,
// ErrorCode, StatusMessage, TypeName, RequestToken, Identifier). Without this
// diagnostic we can't tell ServiceTimeout from GeneralServiceException from
// ServiceInternalError when the next problem destroy hits prod.
func TestStatusResource_FailedProgress_LogsDiagnosticDetails(t *testing.T) {
	ctx, buf := captureSlog(t)

	mockAPI := new(mockCloudControlAPI)
	client := &Client{api: mockAPI}

	mockAPI.On("GetResourceRequestStatus", mock.Anything, mock.Anything).Return(
		&cloudcontrol.GetResourceRequestStatusOutput{
			ProgressEvent: &cctypes.ProgressEvent{
				Operation:       cctypes.OperationDelete,
				OperationStatus: cctypes.OperationStatusFailed,
				ErrorCode:       cctypes.HandlerErrorCodeServiceTimeout,
				StatusMessage:   ptr.Of("Resource DELETE operation timed out"),
				TypeName:        ptr.Of("AWS::ECS::Service"),
				RequestToken:    ptr.Of("req-token-svc-timeout"),
				Identifier:      ptr.Of("formae-cluster/formae-service"),
			},
		}, nil,
	)

	_, err := client.StatusResource(
		ctx,
		&resource.StatusRequest{RequestID: "req-token-svc-timeout"},
		func(_ context.Context, _ *resource.ReadRequest) (*resource.ReadResult, error) {
			t.Fatalf("readFunc must not be called on a FAILED status")
			return nil, nil
		},
	)
	require.NoError(t, err)

	out := buf.String()
	require.Contains(t, out, `level=WARN`, "must log at WARN level for prod surfacing")
	require.Contains(t, out, "ServiceTimeout", "must include ErrorCode — the smoking gun for future remap decisions")
	require.Contains(t, out, "Resource DELETE operation timed out", "must include StatusMessage")
	require.Contains(t, out, "AWS::ECS::Service", "must include TypeName")
	require.Contains(t, out, "DELETE", "must include Operation")
	require.Contains(t, out, "req-token-svc-timeout", "must include RequestToken for CloudTrail correlation")
	require.Contains(t, out, "formae-cluster/formae-service", "must include Identifier so we can find the resource")
}

// InProgress events fire on every Status poll during a long-running op (often
// many per minute). They MUST NOT trigger the diagnostic log or we'd drown
// real failure signals.
func TestStatusResource_InProgress_DoesNotLog(t *testing.T) {
	ctx, buf := captureSlog(t)

	mockAPI := new(mockCloudControlAPI)
	client := &Client{api: mockAPI}

	mockAPI.On("GetResourceRequestStatus", mock.Anything, mock.Anything).Return(
		&cloudcontrol.GetResourceRequestStatusOutput{
			ProgressEvent: &cctypes.ProgressEvent{
				Operation:       cctypes.OperationCreate,
				OperationStatus: cctypes.OperationStatusInProgress,
				TypeName:        ptr.Of("AWS::S3::Bucket"),
				RequestToken:    ptr.Of("req-in-progress"),
			},
		}, nil,
	)

	_, err := client.StatusResource(
		ctx,
		&resource.StatusRequest{RequestID: "req-in-progress"},
		func(_ context.Context, _ *resource.ReadRequest) (*resource.ReadResult, error) {
			return nil, nil
		},
	)
	require.NoError(t, err)
	require.Empty(t, buf.String(), "must not log for InProgress polls — they fire many times per op")
}

// NotStabilized is FAILED at the CCAPI layer but our code remaps it to
// InProgress (see ccx/client.go NotStabilized→InProgress). The diagnostic log
// must fire AFTER all remaps so remapped-to-InProgress cases stay silent.
func TestStatusResource_NotStabilizedRemap_DoesNotLog(t *testing.T) {
	ctx, buf := captureSlog(t)

	mockAPI := new(mockCloudControlAPI)
	client := &Client{api: mockAPI}

	mockAPI.On("GetResourceRequestStatus", mock.Anything, mock.Anything).Return(
		&cloudcontrol.GetResourceRequestStatusOutput{
			ProgressEvent: &cctypes.ProgressEvent{
				Operation:       cctypes.OperationUpdate,
				OperationStatus: cctypes.OperationStatusFailed,
				ErrorCode:       cctypes.HandlerErrorCodeNotStabilized,
				StatusMessage:   ptr.Of("Resource is still stabilizing"),
				TypeName:        ptr.Of("AWS::DynamoDB::Table"),
			},
		}, nil,
	)

	_, err := client.StatusResource(
		ctx,
		&resource.StatusRequest{RequestID: "req-not-stabilized"},
		func(_ context.Context, _ *resource.ReadRequest) (*resource.ReadResult, error) {
			return nil, nil
		},
	)
	require.NoError(t, err)
	require.Empty(t, buf.String(), "NotStabilized remaps to InProgress and must not warn-log")
}

// ---------------------------------------------------------------------------
// StatusResource: budgeted status call, NativeID fallback, and the bounded
// enrichment-pending window.
// ---------------------------------------------------------------------------

// statusEventBase is the reference completion time the enrichment-window tests
// measure their injected clock against.
var statusEventBase = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

// newStatusClient builds a Client whose clock the test drives and whose
// watched-call budget is short enough that a throttled read-back exhausts it
// without the suite spending real seconds on it.
func newStatusClient(mockAPI *mockCloudControlAPI, now func() time.Time) *Client {
	return &Client{api: mockAPI, watchedBudget: testWatchedBudget, now: now}
}

// stubStatusPoll makes every GetResourceRequestStatus return the same progress
// event. The event is returned by pointer, so a test can mutate it between
// polls to model a timestamp that moves.
func stubStatusPoll(mockAPI *mockCloudControlAPI, event *cctypes.ProgressEvent) {
	mockAPI.On("GetResourceRequestStatus", mock.Anything, mock.Anything).Return(
		&cloudcontrol.GetResourceRequestStatusOutput{ProgressEvent: event}, nil,
	)
}

// successEvent is a terminal CloudControl Success progress event for a create.
func successEvent(identifier *string, eventTime *time.Time) *cctypes.ProgressEvent {
	return &cctypes.ProgressEvent{
		Operation:       cctypes.OperationCreate,
		OperationStatus: cctypes.OperationStatusSuccess,
		TypeName:        ptr.Of("AWS::DynamoDB::Table"),
		RequestToken:    ptr.Of("req-enrichment"),
		Identifier:      identifier,
		EventTime:       eventTime,
	}
}

// recordingRead is a StatusResource readFunc that records the read requests it
// received and replays a fixed outcome, so a test can assert both what the
// read-back targeted and how StatusResource folded its result in.
type recordingRead struct {
	requests []*resource.ReadRequest
	result   *resource.ReadResult
	err      error
}

func (r *recordingRead) fn(_ context.Context, req *resource.ReadRequest) (*resource.ReadResult, error) {
	r.requests = append(r.requests, req)
	return r.result, r.err
}

// throttlingReadBack is a read-back that never stops throttling — the recoverable
// condition the enrichment retry loop rides until the watched-call budget stops
// it.
func throttlingReadBack() *recordingRead {
	return &recordingRead{err: ccOpError(&cctypes.ThrottlingException{Message: aws.String("Rate exceeded")})}
}

func trackedWindow(t *testing.T, c *Client, requestID string) (window, bool) {
	t.Helper()
	c.windowsMu.Lock()
	defer c.windowsMu.Unlock()
	w, ok := c.windows[requestID]
	return w, ok
}

// Inside the enrichment window the operation is still healthy: the mutation
// succeeded and only the read-back is outstanding, so the poll reports
// InProgress and the operator polls again — each poll a heartbeat.
func TestStatusResource_EnrichmentPending_WithinWindow_ReturnsInProgress(t *testing.T) {
	mockAPI := new(mockCloudControlAPI)
	clock, _ := fakeClock(statusEventBase.Add(30 * time.Second))
	client := newStatusClient(mockAPI, clock)
	stubStatusPoll(mockAPI, successEvent(ptr.Of("tbl-1"), ptr.Of(statusEventBase)))
	read := throttlingReadBack()

	result, err := client.StatusResource(context.Background(),
		&resource.StatusRequest{RequestID: "req-enrichment"}, read.fn)

	require.NoError(t, err)
	require.Equal(t, resource.OperationStatusInProgress, result.ProgressResult.OperationStatus,
		"a read-back that outran its budget 30s after completion is still worth another poll")
	require.Equal(t, "tbl-1", result.ProgressResult.NativeID)
	require.Equal(t, "req-enrichment", result.ProgressResult.RequestID)
	require.Nil(t, result.ProgressResult.ResourceProperties, "no properties were read, so none may be fabricated")
	require.Empty(t, string(result.ProgressResult.ErrorCode), "an InProgress conversion is not an error condition")
	require.NotEmpty(t, result.ProgressResult.StatusMessage, "the pending read-back must be visible to the operator")

	_, tracked := trackedWindow(t, client, "req-enrichment")
	require.True(t, tracked, "the backstop stamp must survive a non-terminal poll")
}

// The single most important property of this path: once the window elapses the
// poll returns SUCCESS, never a Failure. Only the success branch writes the
// inventory row, so a Failure here would leave AWS holding a resource formae has
// no record of — and the next apply would create a second one.
func TestStatusResource_EnrichmentWindowExpired_ReturnsSuccessNotFailure(t *testing.T) {
	mockAPI := new(mockCloudControlAPI)
	clock, _ := fakeClock(statusEventBase.Add(3 * time.Minute))
	client := newStatusClient(mockAPI, clock)
	stubStatusPoll(mockAPI, successEvent(ptr.Of("tbl-1"), ptr.Of(statusEventBase)))
	read := throttlingReadBack()

	result, err := client.StatusResource(context.Background(),
		&resource.StatusRequest{RequestID: "req-enrichment"}, read.fn)

	require.NoError(t, err)
	require.NotEqual(t, resource.OperationStatusFailure, result.ProgressResult.OperationStatus,
		"a Failure would drop the inventory row for a resource that actually exists")
	require.Equal(t, resource.OperationStatusSuccess, result.ProgressResult.OperationStatus)
	require.Equal(t, "tbl-1", result.ProgressResult.NativeID, "the inventory row is unusable without the native ID")
	require.Empty(t, string(result.ProgressResult.ErrorCode), "the underlying event is a Success event")
	require.Contains(t, result.ProgressResult.StatusMessage, "tbl-1")
	require.Contains(t, result.ProgressResult.StatusMessage, "enrichment window")

	_, tracked := trackedWindow(t, client, "req-enrichment")
	require.False(t, tracked, "a terminal return must leave no tracker entry behind")
}

// EventTime is the primary clock precisely because it is stateless: a plugin
// process that restarted mid-operation, and so has no backstop stamp at all,
// must reach the same verdict from the same event as the process that stamped it.
func TestStatusResource_EnrichmentWindowExpired_FreshClientReachesSameVerdict(t *testing.T) {
	mockAPI := new(mockCloudControlAPI)
	stubStatusPoll(mockAPI, successEvent(ptr.Of("tbl-1"), ptr.Of(statusEventBase)))

	// A first process observes the pending read-back and stamps its backstop.
	firstClock, _ := fakeClock(statusEventBase.Add(30 * time.Second))
	first := newStatusClient(mockAPI, firstClock)
	pending, err := first.StatusResource(context.Background(),
		&resource.StatusRequest{RequestID: "req-enrichment"}, throttlingReadBack().fn)
	require.NoError(t, err)
	require.Equal(t, resource.OperationStatusInProgress, pending.ProgressResult.OperationStatus)
	_, tracked := trackedWindow(t, first, "req-enrichment")
	require.True(t, tracked, "the first process must have stamped its backstop")

	// The plugin process restarts: a second client carries none of that state.
	freshClock, _ := fakeClock(statusEventBase.Add(3 * time.Minute))
	fresh := newStatusClient(mockAPI, freshClock)
	_, freshTracked := trackedWindow(t, fresh, "req-enrichment")
	require.False(t, freshTracked, "a restarted process starts with no stamp of its own")

	result, err := fresh.StatusResource(context.Background(),
		&resource.StatusRequest{RequestID: "req-enrichment"}, throttlingReadBack().fn)

	require.NoError(t, err)
	require.Equal(t, resource.OperationStatusSuccess, result.ProgressResult.OperationStatus,
		"the verdict must come from the event, not from process-local state")
	require.Equal(t, "tbl-1", result.ProgressResult.NativeID)
	require.Contains(t, result.ProgressResult.StatusMessage, "enrichment window")
}

// With no EventTime there is nothing stateless to bound the wait against, so
// the poll commits what it has rather than converting to a wait it cannot end.
func TestStatusResource_EnrichmentPending_NilEventTime_ReturnsSuccessWithNativeID(t *testing.T) {
	mockAPI := new(mockCloudControlAPI)
	clock, _ := fakeClock(statusEventBase)
	client := newStatusClient(mockAPI, clock)
	stubStatusPoll(mockAPI, successEvent(ptr.Of("tbl-1"), nil))

	result, err := client.StatusResource(context.Background(),
		&resource.StatusRequest{RequestID: "req-enrichment"}, throttlingReadBack().fn)

	require.NoError(t, err)
	require.Equal(t, resource.OperationStatusSuccess, result.ProgressResult.OperationStatus)
	require.Equal(t, "tbl-1", result.ProgressResult.NativeID)
	require.Contains(t, result.ProgressResult.StatusMessage, "no trustworthy completion time")
	require.NotContains(t, result.ProgressResult.StatusMessage, "enrichment window",
		"no window was ever entered, so the message must not tell an operator one elapsed")
}

// An EventTime far enough in the future to be nonsense cannot bound anything
// either — converting on it would keep the operation InProgress indefinitely.
func TestStatusResource_EnrichmentPending_FarFutureEventTime_ReturnsSuccessWithNativeID(t *testing.T) {
	mockAPI := new(mockCloudControlAPI)
	clock, _ := fakeClock(statusEventBase)
	client := newStatusClient(mockAPI, clock)
	stubStatusPoll(mockAPI, successEvent(ptr.Of("tbl-1"), ptr.Of(statusEventBase.Add(10*time.Minute))))

	result, err := client.StatusResource(context.Background(),
		&resource.StatusRequest{RequestID: "req-enrichment"}, throttlingReadBack().fn)

	require.NoError(t, err)
	require.Equal(t, resource.OperationStatusSuccess, result.ProgressResult.OperationStatus)
	require.Equal(t, "tbl-1", result.ProgressResult.NativeID)
	require.Contains(t, result.ProgressResult.StatusMessage, "no trustworthy completion time")
	require.NotContains(t, result.ProgressResult.StatusMessage, "enrichment window",
		"no window was ever entered, so the message must not tell an operator one elapsed")
}

// Modest disagreement between the AWS control plane's clock and this host's is
// ordinary; it must not cost the operation its enrichment window.
func TestStatusResource_EnrichmentPending_SmallNegativeSkew_ConvertsToInProgress(t *testing.T) {
	mockAPI := new(mockCloudControlAPI)
	clock, _ := fakeClock(statusEventBase)
	client := newStatusClient(mockAPI, clock)
	stubStatusPoll(mockAPI, successEvent(ptr.Of("tbl-1"), ptr.Of(statusEventBase.Add(10*time.Second))))

	result, err := client.StatusResource(context.Background(),
		&resource.StatusRequest{RequestID: "req-enrichment"}, throttlingReadBack().fn)

	require.NoError(t, err)
	require.Equal(t, resource.OperationStatusInProgress, result.ProgressResult.OperationStatus,
		"10s of clock skew is tolerable and must clamp to a just-completed event")
}

// A timestamp that stays ahead of this host's clock across polls must still end
// the wait: the operator cannot poll forever.
func TestStatusResource_EnrichmentPending_PersistentlyFutureEventTime_Terminates(t *testing.T) {
	mockAPI := new(mockCloudControlAPI)
	now := statusEventBase
	clock, advance := fakeClock(now)
	client := newStatusClient(mockAPI, clock)
	event := successEvent(ptr.Of("tbl-1"), ptr.Of(statusEventBase.Add(20*time.Second)))
	stubStatusPoll(mockAPI, event)

	var last *resource.StatusResult
	polls := 0
	for ; polls < 30; polls++ {
		result, err := client.StatusResource(context.Background(),
			&resource.StatusRequest{RequestID: "req-enrichment"}, throttlingReadBack().fn)
		require.NoError(t, err)
		last = result
		if result.ProgressResult.OperationStatus != resource.OperationStatusInProgress {
			break
		}
		advance(clock().Add(20 * time.Second))
	}

	require.Less(t, polls, 30, "an event stuck in the future must not produce an endless InProgress loop")
	require.Equal(t, resource.OperationStatusSuccess, last.ProgressResult.OperationStatus)
	require.Equal(t, "tbl-1", last.ProgressResult.NativeID)
}

// A provider timestamp that creeps forward on every poll would keep the
// EventTime clock perpetually young; the process-local backstop is what ends
// the wait in that case.
func TestStatusResource_EnrichmentPending_AdvancingEventTime_TerminatesViaBackstop(t *testing.T) {
	mockAPI := new(mockCloudControlAPI)
	clock, advance := fakeClock(statusEventBase)
	client := newStatusClient(mockAPI, clock)
	eventTime := statusEventBase
	event := successEvent(ptr.Of("tbl-1"), &eventTime)
	stubStatusPoll(mockAPI, event)

	var last *resource.StatusResult
	polls := 0
	for ; polls < 30; polls++ {
		result, err := client.StatusResource(context.Background(),
			&resource.StatusRequest{RequestID: "req-enrichment"}, throttlingReadBack().fn)
		require.NoError(t, err)
		last = result
		if result.ProgressResult.OperationStatus != resource.OperationStatusInProgress {
			break
		}
		advance(clock().Add(20 * time.Second))
		eventTime = clock() // the provider's timestamp creeps forward with us
	}

	require.Greater(t, polls, 0, "the first pending poll must still convert to InProgress")
	require.Less(t, polls, 30, "the backstop must end a wait the EventTime clock can no longer bound")
	require.Equal(t, resource.OperationStatusSuccess, last.ProgressResult.OperationStatus)
	require.Equal(t, "tbl-1", last.ProgressResult.NativeID)
}

// An implausibly old event is past the window like any other, and takes the
// same Success branch rather than becoming a failure.
func TestStatusResource_ImplausiblyOldEventTime_ReturnsSuccess(t *testing.T) {
	mockAPI := new(mockCloudControlAPI)
	clock, _ := fakeClock(statusEventBase)
	client := newStatusClient(mockAPI, clock)
	stubStatusPoll(mockAPI, successEvent(ptr.Of("tbl-1"), ptr.Of(statusEventBase.Add(-30*24*time.Hour))))

	result, err := client.StatusResource(context.Background(),
		&resource.StatusRequest{RequestID: "req-enrichment"}, throttlingReadBack().fn)

	require.NoError(t, err)
	require.NotEqual(t, resource.OperationStatusFailure, result.ProgressResult.OperationStatus)
	require.Equal(t, resource.OperationStatusSuccess, result.ProgressResult.OperationStatus)
	require.Equal(t, "tbl-1", result.ProgressResult.NativeID)
}

// CloudControl may omit the identifier from a terminal event. Success is the
// path that writes the inventory row, so the native ID the request already
// carries has to stand in — for the row and for the read-back it targets.
func TestStatusResource_NilIdentifier_EnrichedRead_FallsBackToRequestNativeID(t *testing.T) {
	mockAPI := new(mockCloudControlAPI)
	clock, _ := fakeClock(statusEventBase)
	client := newStatusClient(mockAPI, clock)
	stubStatusPoll(mockAPI, successEvent(nil, ptr.Of(statusEventBase)))
	read := &recordingRead{result: &resource.ReadResult{Properties: `{"TableName":"t"}`}}

	result, err := client.StatusResource(context.Background(),
		&resource.StatusRequest{RequestID: "req-enrichment", NativeID: "tbl-from-request"}, read.fn)

	require.NoError(t, err)
	require.Equal(t, resource.OperationStatusSuccess, result.ProgressResult.OperationStatus)
	require.Equal(t, "tbl-from-request", result.ProgressResult.NativeID)
	require.Len(t, read.requests, 1)
	require.Equal(t, "tbl-from-request", read.requests[0].NativeID,
		"a read-back against an empty identifier could never succeed")
	require.JSONEq(t, `{"TableName":"t"}`, string(result.ProgressResult.ResourceProperties))
}

func TestStatusResource_NilIdentifier_WindowExpired_FallsBackToRequestNativeID(t *testing.T) {
	mockAPI := new(mockCloudControlAPI)
	clock, _ := fakeClock(statusEventBase.Add(3 * time.Minute))
	client := newStatusClient(mockAPI, clock)
	stubStatusPoll(mockAPI, successEvent(nil, ptr.Of(statusEventBase)))
	read := throttlingReadBack()

	result, err := client.StatusResource(context.Background(),
		&resource.StatusRequest{RequestID: "req-enrichment", NativeID: "tbl-from-request"}, read.fn)

	require.NoError(t, err)
	require.Equal(t, resource.OperationStatusSuccess, result.ProgressResult.OperationStatus)
	require.Equal(t, "tbl-from-request", result.ProgressResult.NativeID)
	require.Contains(t, result.ProgressResult.StatusMessage, "tbl-from-request")
	require.NotEmpty(t, read.requests)
	require.Equal(t, "tbl-from-request", read.requests[0].NativeID)
}

// The status call is the one the watchdog observes most often; it must obey the
// same wall-clock budget as everything else inside a watched RPC.
func TestStatusResource_StatusCallBlocksPastBudget_ReturnsPromptly(t *testing.T) {
	mockAPI := new(mockCloudControlAPI)
	clock, _ := fakeClock(statusEventBase)
	client := newStatusClient(mockAPI, clock)
	mockAPI.On("GetResourceRequestStatus", mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			select {
			case <-args.Get(0).(context.Context).Done():
			case <-time.After(2 * time.Second):
			}
		}).
		Return((*cloudcontrol.GetResourceRequestStatusOutput)(nil), context.DeadlineExceeded)

	start := time.Now()
	result, err := client.StatusResource(context.Background(),
		&resource.StatusRequest{RequestID: "req-blocked", NativeID: "tbl-1"},
		func(context.Context, *resource.ReadRequest) (*resource.ReadResult, error) {
			t.Fatalf("readFunc must not be called when the status call never returned a progress event")
			return nil, nil
		})

	require.Less(t, time.Since(start), time.Second, "the status call must not outlast the watched-call budget")
	require.NoError(t, err)
	require.Equal(t, resource.OperationStatusInProgress, result.ProgressResult.OperationStatus,
		"a status call that ran out of budget observed nothing; the next poll asks again")
	require.Equal(t, "req-blocked", result.ProgressResult.RequestID)
}

// The budget bounds the watched RPC, not each AWS call inside it. If the status
// call and the read-back each derived their own, one poll could spend two
// budgets and the slack the budget was derived against would be gone — so the
// read-back must inherit the deadline the status call already ran under, and a
// slow status call must leave it correspondingly less time.
func TestStatusResource_SharedBudget_StatusCallAndReadBackShareOneDeadline(t *testing.T) {
	const budget = 400 * time.Millisecond
	mockAPI := new(mockCloudControlAPI)
	clock, _ := fakeClock(statusEventBase)
	client := &Client{api: mockAPI, watchedBudget: budget, now: clock}

	var statusDeadline, readDeadline time.Time
	var statusBounded, readBounded bool
	mockAPI.On("GetResourceRequestStatus", mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			statusDeadline, statusBounded = args.Get(0).(context.Context).Deadline()
			// Spend most of the RPC's budget before the read-back begins.
			time.Sleep(3 * budget / 4)
		}).
		Return(&cloudcontrol.GetResourceRequestStatusOutput{
			ProgressEvent: successEvent(ptr.Of("tbl-1"), ptr.Of(statusEventBase)),
		}, nil)

	start := time.Now()
	result, err := client.StatusResource(context.Background(),
		&resource.StatusRequest{RequestID: "req-shared-budget"},
		func(ctx context.Context, _ *resource.ReadRequest) (*resource.ReadResult, error) {
			readDeadline, readBounded = ctx.Deadline()
			<-ctx.Done()
			return nil, ctx.Err()
		})
	spent := time.Since(start)

	require.NoError(t, err)
	require.True(t, statusBounded, "the status call must run under the RPC's deadline")
	require.True(t, readBounded, "the read-back must run under the RPC's deadline")
	require.True(t, statusDeadline.Equal(readDeadline),
		"the read-back must inherit the RPC's deadline rather than start a fresh budget (status %s, read-back %s)",
		statusDeadline, readDeadline)
	require.Less(t, spent, budget+budget/3, "one watched RPC must not spend more than one budget")
	require.Equal(t, resource.OperationStatusInProgress, result.ProgressResult.OperationStatus,
		"the read-back ran out of the shared budget, so the poll defers it")
}

// A malformed response must not take the plugin process down with it. A
// response carrying no progress event leaves the request just as unobserved as
// a failed status call, so it is classified the same way: ask again, bounded by
// the same window.
func TestStatusResource_NilProgressEvent_ConvertsToInProgressWithoutPanicking(t *testing.T) {
	mockAPI := new(mockCloudControlAPI)
	clock, _ := fakeClock(statusEventBase)
	client := newStatusClient(mockAPI, clock)
	mockAPI.On("GetResourceRequestStatus", mock.Anything, mock.Anything).Return(
		&cloudcontrol.GetResourceRequestStatusOutput{}, nil,
	)

	result, err := client.StatusResource(context.Background(),
		&resource.StatusRequest{RequestID: "req-empty", NativeID: "tbl-1"}, noRead(t))

	require.NoError(t, err)
	require.Equal(t, resource.OperationStatusInProgress, result.ProgressResult.OperationStatus)
	require.Equal(t, resource.OperationCheckStatus, result.ProgressResult.Operation)
	require.Equal(t, "tbl-1", result.ProgressResult.NativeID)
}

// The same malformed response without a request token has no window to bound a
// retry against, so it keeps the pre-existing behaviour of surfacing an error.
func TestStatusResource_NilProgressEvent_EmptyRequestID_ReturnsError(t *testing.T) {
	mockAPI := new(mockCloudControlAPI)
	clock, _ := fakeClock(statusEventBase)
	client := newStatusClient(mockAPI, clock)
	mockAPI.On("GetResourceRequestStatus", mock.Anything, mock.Anything).Return(
		&cloudcontrol.GetResourceRequestStatusOutput{}, nil,
	)

	result, err := client.StatusResource(context.Background(),
		&resource.StatusRequest{}, noRead(t))

	require.Error(t, err)
	require.Nil(t, result)
}

// A terminal event with none of its optional fields populated must still
// return a well-formed result rather than dereferencing a nil.
func TestStatusResource_NilEventFields_ReturnsSuccessWithoutPanicking(t *testing.T) {
	mockAPI := new(mockCloudControlAPI)
	clock, _ := fakeClock(statusEventBase)
	client := newStatusClient(mockAPI, clock)
	stubStatusPoll(mockAPI, &cctypes.ProgressEvent{
		Operation:       cctypes.OperationCreate,
		OperationStatus: cctypes.OperationStatusSuccess,
	})
	read := &recordingRead{result: &resource.ReadResult{Properties: `{}`}}

	result, err := client.StatusResource(context.Background(),
		&resource.StatusRequest{RequestID: "req-bare"}, read.fn)

	require.NoError(t, err)
	require.Equal(t, resource.OperationStatusSuccess, result.ProgressResult.OperationStatus)
	require.Empty(t, result.ProgressResult.NativeID)
	require.Empty(t, read.requests, "there is no resource type to read back against")
}

// Any successful status observation clears the consecutive-outage stamp, so a
// later failure starts a fresh window instead of resuming an unrelated one.
func TestStatusResource_SuccessfulStatusCall_ClearsStatusErrorStamp(t *testing.T) {
	mockAPI := new(mockCloudControlAPI)
	clock, _ := fakeClock(statusEventBase)
	client := newStatusClient(mockAPI, clock)
	stubStatusPoll(mockAPI, &cctypes.ProgressEvent{
		Operation:       cctypes.OperationCreate,
		OperationStatus: cctypes.OperationStatusInProgress,
		TypeName:        ptr.Of("AWS::DynamoDB::Table"),
	})
	_, ok := client.stampStatusError(context.Background(), "req-flapping")
	require.True(t, ok)

	_, err := client.StatusResource(context.Background(),
		&resource.StatusRequest{RequestID: "req-flapping"},
		func(context.Context, *resource.ReadRequest) (*resource.ReadResult, error) {
			t.Fatalf("readFunc must not be called on an InProgress poll")
			return nil, nil
		})
	require.NoError(t, err)

	w, tracked := trackedWindow(t, client, "req-flapping")
	require.True(t, tracked, "an in-flight request keeps its entry")
	require.True(t, w.statusError.IsZero(), "a successful status call clears the outage stamp")
}

// A resolved request has nothing left for either stamp to bound.
func TestStatusResource_TerminalFailure_ForgetsRequestWindow(t *testing.T) {
	mockAPI := new(mockCloudControlAPI)
	clock, _ := fakeClock(statusEventBase)
	client := newStatusClient(mockAPI, clock)
	stubStatusPoll(mockAPI, &cctypes.ProgressEvent{
		Operation:       cctypes.OperationCreate,
		OperationStatus: cctypes.OperationStatusFailed,
		ErrorCode:       cctypes.HandlerErrorCodeInvalidRequest,
		TypeName:        ptr.Of("AWS::DynamoDB::Table"),
	})
	_, ok := client.stampStatusError(context.Background(), "req-doomed")
	require.True(t, ok)

	_, err := client.StatusResource(context.Background(),
		&resource.StatusRequest{RequestID: "req-doomed"},
		func(context.Context, *resource.ReadRequest) (*resource.ReadResult, error) {
			return nil, nil
		})
	require.NoError(t, err)

	_, tracked := trackedWindow(t, client, "req-doomed")
	require.False(t, tracked, "a terminal return must leave no tracker entry behind")
}

// Within the skew allowance an event that appears to be slightly in the future
// reads as just-completed, so the elapsed time the decision reports stays
// non-negative.
func TestEnrichmentDecision_SmallNegativeSkewClampsElapsedToZero(t *testing.T) {
	clock, _ := fakeClock(statusEventBase)
	client := &Client{now: clock}

	verdict, elapsed := client.enrichmentDecision(context.Background(), "req-skewed",
		ptr.Of(statusEventBase.Add(10*time.Second)))

	require.Equal(t, enrichmentPendingRetry, verdict)
	require.Equal(t, time.Duration(0), elapsed, "tolerable skew reads as a just-completed event")
}

// ---------------------------------------------------------------------------
// StatusResource: classifying a status poll that observed nothing.
// ---------------------------------------------------------------------------

// throttlingStatusPoll makes every GetResourceRequestStatus fail with a
// recoverable AWS error — the condition the status call's retry loop rides
// until the watched-call budget stops it, leaving the poll with nothing
// observed about the request.
func throttlingStatusPoll(mockAPI *mockCloudControlAPI) {
	mockAPI.On("GetResourceRequestStatus", mock.Anything, mock.Anything).Return(
		(*cloudcontrol.GetResourceRequestStatusOutput)(nil),
		ccOpError(&cctypes.ThrottlingException{Message: aws.String("Rate exceeded")}),
	)
}

// tokenNotFoundStatusPoll makes every GetResourceRequestStatus report that
// CloudControl no longer knows the request token.
func tokenNotFoundStatusPoll(mockAPI *mockCloudControlAPI) *mock.Call {
	return mockAPI.On("GetResourceRequestStatus", mock.Anything, mock.Anything).Return(
		(*cloudcontrol.GetResourceRequestStatusOutput)(nil),
		ccOpError(&cctypes.RequestTokenNotFoundException{Message: aws.String("Request token not found")}),
	)
}

// noRead is a readFunc for polls that must never reach the read-back, because
// no progress event was observed to enrich.
func noRead(t *testing.T) func(context.Context, *resource.ReadRequest) (*resource.ReadResult, error) {
	t.Helper()
	return func(context.Context, *resource.ReadRequest) (*resource.ReadResult, error) {
		t.Fatalf("readFunc must not be called on a poll that observed no progress event")
		return nil, nil
	}
}

// A status call that ran out of budget on a recoverable AWS error says nothing
// about the request, so the poll asks again against the same token. It must not
// be a Failure — not even with a recoverable code: the operator answers a
// recoverable Failure by re-invoking the original CRUD, which would run a
// second create for an operation that may already have succeeded.
func TestStatusResource_RecoverableStatusError_ConvertsToInProgress(t *testing.T) {
	mockAPI := new(mockCloudControlAPI)
	clock, _ := fakeClock(statusEventBase)
	client := newStatusClient(mockAPI, clock)
	throttlingStatusPoll(mockAPI)

	result, err := client.StatusResource(context.Background(),
		&resource.StatusRequest{RequestID: "req-throttled", NativeID: "tbl-1"}, noRead(t))

	require.NoError(t, err, "a status outage must reach the operator as a progress result, not as a bare error")
	require.NotEqual(t, resource.OperationStatusFailure, result.ProgressResult.OperationStatus,
		"a Failure — recoverable code or not — makes the operator re-invoke the CRUD it already ran")
	require.Equal(t, resource.OperationStatusInProgress, result.ProgressResult.OperationStatus)
	require.Equal(t, resource.OperationCheckStatus, result.ProgressResult.Operation,
		"the status call carries no CRUD verb, so report the operation actually being performed")
	require.Equal(t, "req-throttled", result.ProgressResult.RequestID,
		"the next poll must resume against the same token")
	require.Equal(t, "tbl-1", result.ProgressResult.NativeID)
	require.Empty(t, string(result.ProgressResult.ErrorCode), "an InProgress conversion is not an error condition")
	require.NotEmpty(t, result.ProgressResult.StatusMessage, "the outage must be visible to the operator")

	w, tracked := trackedWindow(t, client, "req-throttled")
	require.True(t, tracked, "the outage window must survive a non-terminal poll")
	require.False(t, w.statusError.IsZero(), "the first poll of an outage starts the window")
}

// An unknown request token is indeterminate: the mutation may or may not have
// applied, and nothing the plugin can observe distinguishes the two. It ends
// the operation with a code the operator will not retry — the shared error
// helper's NotFound is recoverable, so passing it through would re-invoke the
// CRUD on an outcome nobody knows.
func TestStatusResource_RequestTokenNotFound_ReturnsNonRecoverableFailure(t *testing.T) {
	mockAPI := new(mockCloudControlAPI)
	clock, _ := fakeClock(statusEventBase)
	client := newStatusClient(mockAPI, clock)
	tokenNotFoundStatusPoll(mockAPI)

	result, err := client.StatusResource(context.Background(),
		&resource.StatusRequest{RequestID: "req-gone", NativeID: "tbl-1"}, noRead(t))

	require.NoError(t, err)
	require.Equal(t, resource.OperationStatusFailure, result.ProgressResult.OperationStatus)
	require.Equal(t, resource.OperationErrorCodeUnforeseenError, result.ProgressResult.ErrorCode)
	require.False(t, resource.IsRecoverable(result.ProgressResult.ErrorCode),
		"a recoverable code would re-invoke a CRUD whose outcome is unknown")
	require.Equal(t, resource.OperationCheckStatus, result.ProgressResult.Operation)
	require.Equal(t, "req-gone", result.ProgressResult.RequestID)
	require.Equal(t, "tbl-1", result.ProgressResult.NativeID)
	require.Contains(t, result.ProgressResult.StatusMessage, "tbl-1",
		"the message must name the resource whose outcome is unknown")

	_, tracked := trackedWindow(t, client, "req-gone")
	require.False(t, tracked, "a terminal return must leave no tracker entry behind")
}

// The budget is consulted before the failure is classified, so a status call
// that crosses the deadline returns the token-not-found exception wrapped in
// the budget sentinel. An unknown token is terminal however it arrives —
// re-polling a token CloudControl has never heard of only defers the same
// answer.
func TestStatusResource_RequestTokenNotFound_WrappedInBudgetExhaustion_StaysTerminal(t *testing.T) {
	mockAPI := new(mockCloudControlAPI)
	clock, _ := fakeClock(statusEventBase)
	client := newStatusClient(mockAPI, clock)
	tokenNotFoundStatusPoll(mockAPI).Run(func(args mock.Arguments) {
		<-args.Get(0).(context.Context).Done()
	})

	result, err := client.StatusResource(context.Background(),
		&resource.StatusRequest{RequestID: "req-gone-slowly", NativeID: "tbl-1"}, noRead(t))

	require.NoError(t, err)
	require.Equal(t, resource.OperationStatusFailure, result.ProgressResult.OperationStatus,
		"an unknown request token is terminal even when the budget expired first")
	require.False(t, resource.IsRecoverable(result.ProgressResult.ErrorCode))
	require.Equal(t, resource.OperationErrorCodeUnforeseenError, result.ProgressResult.ErrorCode)
}

// The InProgress conversion does not consume an operator retry attempt, so the
// polling needs its own bound: past the window the operation ends rather than
// polling a resource nobody can observe forever.
func TestStatusResource_StatusOutageWindowElapsed_ReturnsNonRecoverableFailure(t *testing.T) {
	mockAPI := new(mockCloudControlAPI)
	clock, advance := fakeClock(statusEventBase)
	client := newStatusClient(mockAPI, clock)
	throttlingStatusPoll(mockAPI)
	request := &resource.StatusRequest{RequestID: "req-dark", NativeID: "tbl-1"}

	first, err := client.StatusResource(context.Background(), request, noRead(t))
	require.NoError(t, err)
	require.Equal(t, resource.OperationStatusInProgress, first.ProgressResult.OperationStatus)

	advance(statusEventBase.Add(statusOutageWindow))
	last, err := client.StatusResource(context.Background(), request, noRead(t))

	require.NoError(t, err)
	require.Equal(t, resource.OperationStatusFailure, last.ProgressResult.OperationStatus,
		"a consecutive outage past the window must end the operation")
	require.Equal(t, resource.OperationErrorCodeUnforeseenError, last.ProgressResult.ErrorCode)
	require.False(t, resource.IsRecoverable(last.ProgressResult.ErrorCode),
		"a recoverable code would re-invoke the CRUD on an outcome nobody observed")
	require.Equal(t, resource.OperationCheckStatus, last.ProgressResult.Operation)
	require.Equal(t, "tbl-1", last.ProgressResult.NativeID)
	require.Contains(t, last.ProgressResult.StatusMessage, "tbl-1")

	_, tracked := trackedWindow(t, client, "req-dark")
	require.False(t, tracked, "a terminal return must leave no tracker entry behind")
}

// The window bounds a *consecutive* outage: a poll that did observe the request
// proves the plugin can see it again, so the next outage starts a fresh window
// rather than resuming an unrelated one.
func TestStatusResource_SuccessfulPollBetweenOutages_RestartsWindow(t *testing.T) {
	mockAPI := new(mockCloudControlAPI)
	clock, advance := fakeClock(statusEventBase)
	client := newStatusClient(mockAPI, clock)
	throttled := ccOpError(&cctypes.ThrottlingException{Message: aws.String("Rate exceeded")})
	mockAPI.On("GetResourceRequestStatus", mock.Anything, mock.Anything).
		Return((*cloudcontrol.GetResourceRequestStatusOutput)(nil), throttled).Once()
	mockAPI.On("GetResourceRequestStatus", mock.Anything, mock.Anything).
		Return(&cloudcontrol.GetResourceRequestStatusOutput{ProgressEvent: &cctypes.ProgressEvent{
			Operation:       cctypes.OperationCreate,
			OperationStatus: cctypes.OperationStatusInProgress,
			TypeName:        ptr.Of("AWS::DynamoDB::Table"),
		}}, nil).Once()
	mockAPI.On("GetResourceRequestStatus", mock.Anything, mock.Anything).
		Return((*cloudcontrol.GetResourceRequestStatusOutput)(nil), throttled).Once()
	request := &resource.StatusRequest{RequestID: "req-flaky", NativeID: "tbl-1"}

	outage, err := client.StatusResource(context.Background(), request, noRead(t))
	require.NoError(t, err)
	require.Equal(t, resource.OperationStatusInProgress, outage.ProgressResult.OperationStatus)

	advance(statusEventBase.Add(time.Minute))
	observed, err := client.StatusResource(context.Background(), request,
		func(context.Context, *resource.ReadRequest) (*resource.ReadResult, error) {
			t.Fatalf("readFunc must not be called on an InProgress poll")
			return nil, nil
		})
	require.NoError(t, err)
	require.Equal(t, resource.OperationStatusInProgress, observed.ProgressResult.OperationStatus)

	resumed := statusEventBase.Add(2*time.Minute + 30*time.Second)
	advance(resumed)
	again, err := client.StatusResource(context.Background(), request, noRead(t))

	require.NoError(t, err)
	require.Equal(t, resource.OperationStatusInProgress, again.ProgressResult.OperationStatus,
		"a window that survived the successful poll would have gone terminal here")
	w, tracked := trackedWindow(t, client, "req-flaky")
	require.True(t, tracked)
	require.Equal(t, resumed, w.statusError, "the new outage is measured from its own first poll")
}

// Without a request token there is no key to bound the outage against, and an
// unbounded InProgress would poll forever — so this keeps the pre-existing
// behaviour of surfacing the raw error.
func TestStatusResource_RecoverableStatusError_EmptyRequestID_ReturnsBareError(t *testing.T) {
	mockAPI := new(mockCloudControlAPI)
	clock, _ := fakeClock(statusEventBase)
	client := newStatusClient(mockAPI, clock)
	throttlingStatusPoll(mockAPI)

	result, err := client.StatusResource(context.Background(),
		&resource.StatusRequest{NativeID: "tbl-1"}, noRead(t))

	require.Error(t, err)
	require.Nil(t, result)
	require.ErrorIs(t, err, errRetryBudgetExhausted)
	_, tracked := trackedWindow(t, client, "")
	require.False(t, tracked, "an empty request token must never be admitted to the tracker")
}

// A status error that is not recoverable and not an unknown token is a real
// error about the call itself; polling cannot improve on it, so it keeps
// today's behaviour.
func TestStatusResource_NonRecoverableStatusError_ReturnsBareError(t *testing.T) {
	mockAPI := new(mockCloudControlAPI)
	clock, _ := fakeClock(statusEventBase)
	client := newStatusClient(mockAPI, clock)
	mockAPI.On("GetResourceRequestStatus", mock.Anything, mock.Anything).Return(
		(*cloudcontrol.GetResourceRequestStatusOutput)(nil),
		ccOpError(&cctypes.InvalidRequestException{Message: aws.String("RequestToken is malformed")}),
	)

	result, err := client.StatusResource(context.Background(),
		&resource.StatusRequest{RequestID: "req-invalid", NativeID: "tbl-1"}, noRead(t))

	require.Error(t, err)
	require.Nil(t, result)
	require.NotErrorIs(t, err, errRetryBudgetExhausted)
	_, tracked := trackedWindow(t, client, "req-invalid")
	require.False(t, tracked, "a non-recoverable status error opens no outage window")
}
