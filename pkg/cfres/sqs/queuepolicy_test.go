// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package sqs

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/config"
)

func TestQueuePolicy_Create_Success(t *testing.T) {
	ctx := context.Background()
	client := &mockSQSClient{}

	queueURL := "https://sqs.us-east-1.amazonaws.com/123456789012/my-queue"
	client.On("SetQueueAttributes", ctx, mock.MatchedBy(func(input *sqs.SetQueueAttributesInput) bool {
		return input.QueueUrl != nil && *input.QueueUrl == queueURL &&
			input.Attributes[string(sqstypes.QueueAttributeNamePolicy)] != ""
	})).Return(&sqs.SetQueueAttributesOutput{}, nil)

	qp := &QueuePolicy{cfg: &config.Config{}}
	props := map[string]any{
		"Queues": []any{queueURL},
		"PolicyDocument": map[string]any{
			"Version": "2012-10-17",
			"Statement": []any{
				map[string]any{
					"Effect":    "Allow",
					"Principal": "*",
					"Action":    "sqs:SendMessage",
					"Resource":  "*",
				},
			},
		},
	}
	propsJSON, _ := json.Marshal(props)

	result, err := qp.createWithClient(ctx, client, &resource.CreateRequest{
		ResourceType: "AWS::SQS::QueuePolicy",
		Properties:   propsJSON,
	})

	assert.NoError(t, err)
	assert.Equal(t, resource.OperationStatusSuccess, result.ProgressResult.OperationStatus)
	assert.Equal(t, queueURL, result.ProgressResult.NativeID)
	client.AssertExpectations(t)
}

func TestQueuePolicy_Create_MultipleQueues(t *testing.T) {
	ctx := context.Background()
	client := &mockSQSClient{}

	queue1 := "https://sqs.us-east-1.amazonaws.com/123456789012/queue-1"
	queue2 := "https://sqs.us-east-1.amazonaws.com/123456789012/queue-2"

	client.On("SetQueueAttributes", ctx, mock.MatchedBy(func(input *sqs.SetQueueAttributesInput) bool {
		return input.QueueUrl != nil && *input.QueueUrl == queue1
	})).Return(&sqs.SetQueueAttributesOutput{}, nil)

	client.On("SetQueueAttributes", ctx, mock.MatchedBy(func(input *sqs.SetQueueAttributesInput) bool {
		return input.QueueUrl != nil && *input.QueueUrl == queue2
	})).Return(&sqs.SetQueueAttributesOutput{}, nil)

	qp := &QueuePolicy{cfg: &config.Config{}}
	props := map[string]any{
		"Queues":         []any{queue1, queue2},
		"PolicyDocument": map[string]any{"Version": "2012-10-17"},
	}
	propsJSON, _ := json.Marshal(props)

	result, err := qp.createWithClient(ctx, client, &resource.CreateRequest{
		ResourceType: "AWS::SQS::QueuePolicy",
		Properties:   propsJSON,
	})

	assert.NoError(t, err)
	assert.Equal(t, queue1+"|"+queue2, result.ProgressResult.NativeID)
	client.AssertExpectations(t)
}

func TestQueuePolicy_Create_MissingQueues(t *testing.T) {
	ctx := context.Background()
	client := &mockSQSClient{}

	qp := &QueuePolicy{cfg: &config.Config{}}
	result, err := qp.createWithClient(ctx, client, &resource.CreateRequest{
		ResourceType: "AWS::SQS::QueuePolicy",
		Properties:   json.RawMessage(`{"PolicyDocument": {}}`),
	})

	assert.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "queues is required")
}

func TestQueuePolicy_Read_Success(t *testing.T) {
	ctx := context.Background()
	client := &mockSQSClient{}

	queueURL := "https://sqs.us-east-1.amazonaws.com/123456789012/my-queue"
	policyJSON := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":"*","Action":"sqs:SendMessage","Resource":"*"}]}`

	client.On("GetQueueAttributes", ctx, mock.MatchedBy(func(input *sqs.GetQueueAttributesInput) bool {
		return input.QueueUrl != nil && *input.QueueUrl == queueURL
	})).Return(&sqs.GetQueueAttributesOutput{
		Attributes: map[string]string{
			string(sqstypes.QueueAttributeNamePolicy): policyJSON,
		},
	}, nil)

	qp := &QueuePolicy{cfg: &config.Config{}}
	result, err := qp.readWithClient(ctx, client, &resource.ReadRequest{
		NativeID:     queueURL,
		ResourceType: "AWS::SQS::QueuePolicy",
	})

	assert.NoError(t, err)
	assert.Empty(t, result.ErrorCode)

	var props map[string]any
	_ = json.Unmarshal([]byte(result.Properties), &props)
	assert.NotNil(t, props["PolicyDocument"])
	assert.NotNil(t, props["Queues"])
	client.AssertExpectations(t)
}

func TestQueuePolicy_Read_NoPolicy(t *testing.T) {
	ctx := context.Background()
	client := &mockSQSClient{}

	queueURL := "https://sqs.us-east-1.amazonaws.com/123456789012/my-queue"

	client.On("GetQueueAttributes", ctx, mock.Anything).Return(&sqs.GetQueueAttributesOutput{
		Attributes: map[string]string{},
	}, nil)

	qp := &QueuePolicy{cfg: &config.Config{}}
	result, err := qp.readWithClient(ctx, client, &resource.ReadRequest{
		NativeID:     queueURL,
		ResourceType: "AWS::SQS::QueuePolicy",
	})

	assert.NoError(t, err)
	assert.Equal(t, resource.OperationErrorCodeNotFound, result.ErrorCode)
	client.AssertExpectations(t)
}

func TestQueuePolicy_Delete_Success(t *testing.T) {
	ctx := context.Background()
	client := &mockSQSClient{}

	queueURL := "https://sqs.us-east-1.amazonaws.com/123456789012/my-queue"

	client.On("SetQueueAttributes", ctx, mock.MatchedBy(func(input *sqs.SetQueueAttributesInput) bool {
		return input.QueueUrl != nil && *input.QueueUrl == queueURL &&
			input.Attributes[string(sqstypes.QueueAttributeNamePolicy)] == ""
	})).Return(&sqs.SetQueueAttributesOutput{}, nil)

	qp := &QueuePolicy{cfg: &config.Config{}}
	result, err := qp.deleteWithClient(ctx, client, &resource.DeleteRequest{
		NativeID:     queueURL,
		ResourceType: "AWS::SQS::QueuePolicy",
	})

	assert.NoError(t, err)
	assert.Equal(t, resource.OperationStatusSuccess, result.ProgressResult.OperationStatus)
	client.AssertExpectations(t)
}

func TestQueuePolicy_Delete_QueueNotFound(t *testing.T) {
	ctx := context.Background()
	client := &mockSQSClient{}

	queueURL := "https://sqs.us-east-1.amazonaws.com/123456789012/deleted-queue"

	client.On("SetQueueAttributes", ctx, mock.Anything).Return(
		(*sqs.SetQueueAttributesOutput)(nil),
		fmt.Errorf("wrapped: %w", &sqstypes.QueueDoesNotExist{Message: strPtr("not found")}),
	)

	qp := &QueuePolicy{cfg: &config.Config{}}
	result, err := qp.deleteWithClient(ctx, client, &resource.DeleteRequest{
		NativeID:     queueURL,
		ResourceType: "AWS::SQS::QueuePolicy",
	})

	assert.NoError(t, err)
	assert.Equal(t, resource.OperationStatusSuccess, result.ProgressResult.OperationStatus)
	client.AssertExpectations(t)
}

func TestQueuePolicy_Update_Success(t *testing.T) {
	ctx := context.Background()
	client := &mockSQSClient{}

	queueURL := "https://sqs.us-east-1.amazonaws.com/123456789012/my-queue"
	newPolicyJSON := `{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Principal":"*","Action":"sqs:*","Resource":"*"}]}`

	// SetQueueAttributes for the update
	client.On("SetQueueAttributes", ctx, mock.MatchedBy(func(input *sqs.SetQueueAttributesInput) bool {
		return input.QueueUrl != nil && *input.QueueUrl == queueURL
	})).Return(&sqs.SetQueueAttributesOutput{}, nil)

	// Post-update Read
	client.On("GetQueueAttributes", ctx, mock.Anything).Return(&sqs.GetQueueAttributesOutput{
		Attributes: map[string]string{
			string(sqstypes.QueueAttributeNamePolicy): newPolicyJSON,
		},
	}, nil)

	qp := &QueuePolicy{cfg: &config.Config{}}
	desired := map[string]any{
		"Queues":         []any{queueURL},
		"PolicyDocument": map[string]any{"Version": "2012-10-17", "Statement": []any{map[string]any{"Effect": "Deny", "Principal": "*", "Action": "sqs:*", "Resource": "*"}}},
	}
	desiredJSON, _ := json.Marshal(desired)

	result, err := qp.updateWithClient(ctx, client, &resource.UpdateRequest{
		NativeID:          queueURL,
		ResourceType:      "AWS::SQS::QueuePolicy",
		DesiredProperties: desiredJSON,
	})

	assert.NoError(t, err)
	assert.Equal(t, resource.OperationStatusSuccess, result.ProgressResult.OperationStatus)
	assert.Equal(t, queueURL, result.ProgressResult.NativeID)
	client.AssertExpectations(t)
}

func strPtr(s string) *string {
	return &s
}
