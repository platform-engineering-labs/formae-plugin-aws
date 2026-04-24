// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package lambda

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awslambda "github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/config"
)

func TestEventInvokeConfig_Update_CallsSDKWithParsedNativeIDAndMutableFields(t *testing.T) {
	ctx := context.Background()
	client := &mockEventInvokeConfigClient{}

	nativeID := "my-function|$LATEST"
	desired := json.RawMessage(`{
        "FunctionName": "my-function",
        "Qualifier": "$LATEST",
        "MaximumRetryAttempts": 0,
        "MaximumEventAgeInSeconds": 60
    }`)

	client.On("UpdateFunctionEventInvokeConfig", ctx, mock.MatchedBy(func(input *awslambda.UpdateFunctionEventInvokeConfigInput) bool {
		return aws.ToString(input.FunctionName) == "my-function" &&
			aws.ToString(input.Qualifier) == "$LATEST" &&
			input.MaximumRetryAttempts != nil && *input.MaximumRetryAttempts == 0 &&
			input.MaximumEventAgeInSeconds != nil && *input.MaximumEventAgeInSeconds == 60
	})).Return(&awslambda.UpdateFunctionEventInvokeConfigOutput{
		FunctionArn:              aws.String("arn:aws:lambda:us-east-1:123:function:my-function:$LATEST"),
		MaximumRetryAttempts:     aws.Int32(0),
		MaximumEventAgeInSeconds: aws.Int32(60),
	}, nil)

	eic := &EventInvokeConfig{cfg: &config.Config{}}
	result, err := eic.updateWithClient(ctx, client, &resource.UpdateRequest{
		ResourceType:      "AWS::Lambda::EventInvokeConfig",
		NativeID:          nativeID,
		DesiredProperties: desired,
	})

	assert.NoError(t, err)
	assert.Equal(t, resource.OperationStatusSuccess, result.ProgressResult.OperationStatus)
	assert.Equal(t, nativeID, result.ProgressResult.NativeID)

	var props map[string]any
	assert.NoError(t, json.Unmarshal(result.ProgressResult.ResourceProperties, &props))
	assert.EqualValues(t, 0, props["MaximumRetryAttempts"])
	assert.EqualValues(t, 60, props["MaximumEventAgeInSeconds"])
	client.AssertExpectations(t)
}

func TestEventInvokeConfig_Update_PassesDestinationConfigThrough(t *testing.T) {
	// When the caller supplies a DestinationConfig we must forward it —
	// omitting it would silently clear any destination the user had set.
	ctx := context.Background()
	client := &mockEventInvokeConfigClient{}

	desired := json.RawMessage(`{
        "FunctionName": "my-function",
        "Qualifier": "$LATEST",
        "MaximumRetryAttempts": 1,
        "MaximumEventAgeInSeconds": 120,
        "DestinationConfig": {
            "OnFailure": {"Destination": "arn:aws:sqs:us-east-1:123:dlq"}
        }
    }`)

	client.On("UpdateFunctionEventInvokeConfig", ctx, mock.MatchedBy(func(input *awslambda.UpdateFunctionEventInvokeConfigInput) bool {
		if input.DestinationConfig == nil {
			return false
		}
		if input.DestinationConfig.OnFailure == nil {
			return false
		}
		return aws.ToString(input.DestinationConfig.OnFailure.Destination) == "arn:aws:sqs:us-east-1:123:dlq"
	})).Return(&awslambda.UpdateFunctionEventInvokeConfigOutput{
		FunctionArn: aws.String("arn:aws:lambda:us-east-1:123:function:my-function:$LATEST"),
		DestinationConfig: &lambdatypes.DestinationConfig{
			OnFailure: &lambdatypes.OnFailure{Destination: aws.String("arn:aws:sqs:us-east-1:123:dlq")},
		},
	}, nil)

	eic := &EventInvokeConfig{cfg: &config.Config{}}
	_, err := eic.updateWithClient(ctx, client, &resource.UpdateRequest{
		ResourceType:      "AWS::Lambda::EventInvokeConfig",
		NativeID:          "my-function|$LATEST",
		DesiredProperties: desired,
	})

	assert.NoError(t, err)
	client.AssertExpectations(t)
}

func TestEventInvokeConfig_Update_OmitsDestinationConfigWhenAllSubObjectsAreEmpty(t *testing.T) {
	// CloudControl returns empty OnSuccess:{} / OnFailure:{} sub-objects
	// on Read even when the caller never set them. If we forward those
	// empties to UpdateFunctionEventInvokeConfig the Lambda API rejects
	// with an InvalidParameterValueException: destination is required.
	// Strip empty sub-objects before sending.
	ctx := context.Background()
	client := &mockEventInvokeConfigClient{}

	desired := json.RawMessage(`{
        "FunctionName": "my-function",
        "Qualifier": "$LATEST",
        "MaximumRetryAttempts": 0,
        "MaximumEventAgeInSeconds": 60,
        "DestinationConfig": {"OnFailure": {}, "OnSuccess": {}}
    }`)

	client.On("UpdateFunctionEventInvokeConfig", ctx, mock.MatchedBy(func(input *awslambda.UpdateFunctionEventInvokeConfigInput) bool {
		return input.DestinationConfig == nil
	})).Return(&awslambda.UpdateFunctionEventInvokeConfigOutput{
		FunctionArn: aws.String("arn:aws:lambda:us-east-1:123:function:my-function:$LATEST"),
	}, nil)

	eic := &EventInvokeConfig{cfg: &config.Config{}}
	_, err := eic.updateWithClient(ctx, client, &resource.UpdateRequest{
		ResourceType:      "AWS::Lambda::EventInvokeConfig",
		NativeID:          "my-function|$LATEST",
		DesiredProperties: desired,
	})

	assert.NoError(t, err)
	client.AssertExpectations(t)
}

func TestEventInvokeConfig_Update_ErrorsWhenNativeIDIsNotComposite(t *testing.T) {
	ctx := context.Background()
	client := &mockEventInvokeConfigClient{}

	eic := &EventInvokeConfig{cfg: &config.Config{}}
	_, err := eic.updateWithClient(ctx, client, &resource.UpdateRequest{
		ResourceType:      "AWS::Lambda::EventInvokeConfig",
		NativeID:          "not-composite",
		DesiredProperties: json.RawMessage(`{}`),
	})

	assert.Error(t, err)
	client.AssertNotCalled(t, "UpdateFunctionEventInvokeConfig")
}

func TestEventInvokeConfig_Update_ErrorsWhenDesiredPropertiesMissing(t *testing.T) {
	ctx := context.Background()
	client := &mockEventInvokeConfigClient{}

	eic := &EventInvokeConfig{cfg: &config.Config{}}
	_, err := eic.updateWithClient(ctx, client, &resource.UpdateRequest{
		ResourceType: "AWS::Lambda::EventInvokeConfig",
		NativeID:     "my-function|$LATEST",
	})

	assert.Error(t, err)
	client.AssertNotCalled(t, "UpdateFunctionEventInvokeConfig")
}

func TestEventInvokeConfig_Update_PropagatesAWSError(t *testing.T) {
	ctx := context.Background()
	client := &mockEventInvokeConfigClient{}

	client.On("UpdateFunctionEventInvokeConfig", ctx, mock.Anything).Return(
		(*awslambda.UpdateFunctionEventInvokeConfigOutput)(nil),
		errors.New("throttled"),
	)

	eic := &EventInvokeConfig{cfg: &config.Config{}}
	_, err := eic.updateWithClient(ctx, client, &resource.UpdateRequest{
		ResourceType:      "AWS::Lambda::EventInvokeConfig",
		NativeID:          "my-function|$LATEST",
		DesiredProperties: json.RawMessage(`{"MaximumRetryAttempts": 0, "MaximumEventAgeInSeconds": 60}`),
	})

	assert.Error(t, err)
}
