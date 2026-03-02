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
