// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package ccx

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/cloudcontrol"
	cctypes "github.com/aws/aws-sdk-go-v2/service/cloudcontrol/types"
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
		NativeID:     nativeID,
		ResourceType: resourceType,
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
		NativeID:     nativeID,
		ResourceType: resourceType,
		PatchDocument: ptr.Of(`[{"op":"replace","path":"/PolicyDocument","value":{}}]`),
	})

	require.NoError(t, err)
	require.Equal(t, resource.OperationStatusInProgress, result.ProgressResult.OperationStatus)
	// For in-progress, ResourceProperties should NOT be populated — StatusResource handles that
	require.Nil(t, result.ProgressResult.ResourceProperties)
	// GetResource should only be called once (existence check), not twice (no post-update Read)
	mockAPI.AssertNumberOfCalls(t, "GetResource", 1)
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
