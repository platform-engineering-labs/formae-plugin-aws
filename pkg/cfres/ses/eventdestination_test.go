// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package ses

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	sesv2types "github.com/aws/aws-sdk-go-v2/service/sesv2/types"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/config"
)

func newEventDestinationTestProvisioner(client SesV2ClientInterface) *EventDestination {
	return &EventDestination{
		cfg:              &config.Config{},
		sesClientFactory: func(_ *config.Config) (SesV2ClientInterface, error) { return client, nil },
	}
}

func TestEventDestination_Update_SDKCall_ConvertsCFNShapeToSDKInput(t *testing.T) {
	client := &mockSesV2Client{}
	prov := newEventDestinationTestProvisioner(client)

	desired := []byte(`{
		"ConfigurationSetName": "my-cs",
		"EventDestination": {
			"Name": "bounces",
			"Enabled": false,
			"MatchingEventTypes": ["BOUNCE", "COMPLAINT"],
			"CloudWatchDestination": {
				"DimensionConfigurations": [
					{"DimensionName": "ses:caller-identity", "DefaultDimensionValue": "default", "DimensionValueSource": "MESSAGE_TAG"}
				]
			}
		}
	}`)

	client.On("UpdateConfigurationSetEventDestination", mock.Anything, mock.MatchedBy(func(input *sesv2.UpdateConfigurationSetEventDestinationInput) bool {
		if input.ConfigurationSetName == nil || *input.ConfigurationSetName != "my-cs" {
			return false
		}
		if input.EventDestinationName == nil || *input.EventDestinationName != "bounces" {
			return false
		}
		ed := input.EventDestination
		if ed == nil || ed.Enabled != false {
			return false
		}
		if len(ed.MatchingEventTypes) != 2 || ed.MatchingEventTypes[0] != sesv2types.EventType("BOUNCE") {
			return false
		}
		if ed.CloudWatchDestination == nil || len(ed.CloudWatchDestination.DimensionConfigurations) != 1 {
			return false
		}
		dc := ed.CloudWatchDestination.DimensionConfigurations[0]
		if dc.DimensionName == nil || *dc.DimensionName != "ses:caller-identity" {
			return false
		}
		if dc.DimensionValueSource != sesv2types.DimensionValueSource("MESSAGE_TAG") {
			return false
		}
		return true
	})).Return(&sesv2.UpdateConfigurationSetEventDestinationOutput{}, nil)

	// Mock the post-update Read.
	client.On("GetConfigurationSetEventDestinations", mock.Anything, mock.Anything).Return(
		&sesv2.GetConfigurationSetEventDestinationsOutput{
			EventDestinations: []sesv2types.EventDestination{
				{
					Name:               stringPtr("bounces"),
					Enabled:            false,
					MatchingEventTypes: []sesv2types.EventType{"BOUNCE", "COMPLAINT"},
				},
			},
		}, nil,
	)

	result, err := prov.Update(context.Background(), &resource.UpdateRequest{
		ResourceType:      "AWS::SES::ConfigurationSetEventDestination",
		NativeID:          "my-cs|bounces",
		DesiredProperties: json.RawMessage(desired),
	})

	require.NoError(t, err)
	require.NotNil(t, result.ProgressResult)
	require.Equal(t, resource.OperationStatusSuccess, result.ProgressResult.OperationStatus)
	require.Equal(t, "my-cs|bounces", result.ProgressResult.NativeID)
	require.NotEmpty(t, result.ProgressResult.ResourceProperties, "post-Update Read must populate ResourceProperties")
	client.AssertExpectations(t)
}

func TestEventDestination_Update_BareNativeID_ReturnsError(t *testing.T) {
	prov := newEventDestinationTestProvisioner(&mockSesV2Client{})

	_, err := prov.Update(context.Background(), &resource.UpdateRequest{
		ResourceType:      "AWS::SES::ConfigurationSetEventDestination",
		NativeID:          "bounces",
		DesiredProperties: json.RawMessage(`{"EventDestination": {"Enabled": false}}`),
	})

	require.Error(t, err)
	require.Contains(t, err.Error(), "composite")
}

func TestEventDestination_Update_PlaceholderNativeID_ReturnsError(t *testing.T) {
	// "csName|" placeholder is set by Create when CCAPI returns no ID yet —
	// it should never reach Update (Update operates on an existing resource).
	prov := newEventDestinationTestProvisioner(&mockSesV2Client{})

	_, err := prov.Update(context.Background(), &resource.UpdateRequest{
		ResourceType:      "AWS::SES::ConfigurationSetEventDestination",
		NativeID:          "my-cs|",
		DesiredProperties: json.RawMessage(`{"EventDestination": {"Enabled": false}}`),
	})

	require.Error(t, err)
}

func TestEventDestination_Update_DesiredMissingEventDestination_ReturnsError(t *testing.T) {
	prov := newEventDestinationTestProvisioner(&mockSesV2Client{})

	_, err := prov.Update(context.Background(), &resource.UpdateRequest{
		ResourceType:      "AWS::SES::ConfigurationSetEventDestination",
		NativeID:          "my-cs|bounces",
		DesiredProperties: json.RawMessage(`{"ConfigurationSetName": "my-cs"}`),
	})

	require.Error(t, err)
	require.Contains(t, err.Error(), "EventDestination")
}

func TestEventDestination_Update_SDKError_Propagates(t *testing.T) {
	client := &mockSesV2Client{}
	prov := newEventDestinationTestProvisioner(client)

	client.On("UpdateConfigurationSetEventDestination", mock.Anything, mock.Anything).Return(
		(*sesv2.UpdateConfigurationSetEventDestinationOutput)(nil), errors.New("AccessDenied"),
	)

	_, err := prov.Update(context.Background(), &resource.UpdateRequest{
		ResourceType:      "AWS::SES::ConfigurationSetEventDestination",
		NativeID:          "my-cs|bounces",
		DesiredProperties: json.RawMessage(`{"EventDestination": {"Enabled": false}}`),
	})

	require.Error(t, err)
	require.Contains(t, err.Error(), "UpdateConfigurationSetEventDestination")
}

func TestEventDestination_Update_SuccessButReadFails_StillReturnsSuccess(t *testing.T) {
	// If the SDK Update succeeded, the operation is done from the AWS
	// perspective — a failing post-update Read shouldn't flip Update to
	// Failure. The agent will pick up the new state on its next sync.
	client := &mockSesV2Client{}
	prov := newEventDestinationTestProvisioner(client)

	client.On("UpdateConfigurationSetEventDestination", mock.Anything, mock.Anything).Return(
		&sesv2.UpdateConfigurationSetEventDestinationOutput{}, nil,
	)
	client.On("GetConfigurationSetEventDestinations", mock.Anything, mock.Anything).Return(
		(*sesv2.GetConfigurationSetEventDestinationsOutput)(nil), errors.New("transient"),
	)

	result, err := prov.Update(context.Background(), &resource.UpdateRequest{
		ResourceType:      "AWS::SES::ConfigurationSetEventDestination",
		NativeID:          "my-cs|bounces",
		DesiredProperties: json.RawMessage(`{"EventDestination": {"Enabled": false}}`),
	})

	require.NoError(t, err)
	require.NotNil(t, result.ProgressResult)
	require.Equal(t, resource.OperationStatusSuccess, result.ProgressResult.OperationStatus)
	require.Empty(t, result.ProgressResult.ResourceProperties)
}

func TestParseEventDestinationFromDesired_AllDestinationTypes(t *testing.T) {
	// Exercise every destination shape so refactors to the CFN→SDK mapping
	// don't silently drop a destination type.
	desired := []byte(`{
		"EventDestination": {
			"Enabled": true,
			"MatchingEventTypes": ["SEND"],
			"SnsDestination": {"TopicARN": "arn:aws:sns:us-east-1:1:t"},
			"KinesisFirehoseDestination": {"IAMRoleARN": "arn:aws:iam::1:role/r", "DeliveryStreamARN": "arn:aws:firehose:us-east-1:1:deliverystream/s"},
			"EventBridgeDestination": {"EventBusArn": "arn:aws:events:us-east-1:1:event-bus/default"},
			"PinpointDestination": {"ApplicationArn": "arn:aws:mobiletargeting:us-east-1:1:apps/x"}
		}
	}`)

	sdkED, err := parseEventDestinationFromDesired(desired)
	require.NoError(t, err)
	require.NotNil(t, sdkED)
	require.True(t, sdkED.Enabled)
	require.NotNil(t, sdkED.SnsDestination)
	require.Equal(t, "arn:aws:sns:us-east-1:1:t", *sdkED.SnsDestination.TopicArn)
	require.NotNil(t, sdkED.KinesisFirehoseDestination)
	require.Equal(t, "arn:aws:iam::1:role/r", *sdkED.KinesisFirehoseDestination.IamRoleArn)
	require.Equal(t, "arn:aws:firehose:us-east-1:1:deliverystream/s", *sdkED.KinesisFirehoseDestination.DeliveryStreamArn)
	require.NotNil(t, sdkED.EventBridgeDestination)
	require.Equal(t, "arn:aws:events:us-east-1:1:event-bus/default", *sdkED.EventBridgeDestination.EventBusArn)
	require.NotNil(t, sdkED.PinpointDestination)
	require.Equal(t, "arn:aws:mobiletargeting:us-east-1:1:apps/x", *sdkED.PinpointDestination.ApplicationArn)
}

func TestEventDestination_Delete_SDKCall_SplitsCompositeIdentifier(t *testing.T) {
	client := &mockSesV2Client{}
	prov := newEventDestinationTestProvisioner(client)

	client.On("DeleteConfigurationSetEventDestination", mock.Anything, mock.MatchedBy(func(input *sesv2.DeleteConfigurationSetEventDestinationInput) bool {
		return input.ConfigurationSetName != nil && *input.ConfigurationSetName == "my-cs" &&
			input.EventDestinationName != nil && *input.EventDestinationName == "bounces"
	})).Return(&sesv2.DeleteConfigurationSetEventDestinationOutput{}, nil)

	result, err := prov.Delete(context.Background(), &resource.DeleteRequest{
		ResourceType: "AWS::SES::ConfigurationSetEventDestination",
		NativeID:     "my-cs|bounces",
	})

	require.NoError(t, err)
	require.NotNil(t, result.ProgressResult)
	require.Equal(t, resource.OperationStatusSuccess, result.ProgressResult.OperationStatus)
	require.Equal(t, "my-cs|bounces", result.ProgressResult.NativeID)
	client.AssertExpectations(t)
}

func TestEventDestination_Delete_BareNativeID_ReturnsError(t *testing.T) {
	prov := newEventDestinationTestProvisioner(&mockSesV2Client{})

	_, err := prov.Delete(context.Background(), &resource.DeleteRequest{
		ResourceType: "AWS::SES::ConfigurationSetEventDestination",
		NativeID:     "bounces",
	})

	require.Error(t, err)
	require.Contains(t, err.Error(), "composite")
}

func TestEventDestination_Delete_PlaceholderNativeID_ReturnsError(t *testing.T) {
	prov := newEventDestinationTestProvisioner(&mockSesV2Client{})

	_, err := prov.Delete(context.Background(), &resource.DeleteRequest{
		ResourceType: "AWS::SES::ConfigurationSetEventDestination",
		NativeID:     "my-cs|",
	})

	require.Error(t, err)
}

func TestEventDestination_Delete_NotFoundException_ReturnsSuccess(t *testing.T) {
	// Idempotent delete: AWS NotFound becomes a successful no-op so a
	// destroy that runs twice (e.g., a retried changeset step) doesn't
	// fail the second time.
	client := &mockSesV2Client{}
	prov := newEventDestinationTestProvisioner(client)

	client.On("DeleteConfigurationSetEventDestination", mock.Anything, mock.Anything).Return(
		(*sesv2.DeleteConfigurationSetEventDestinationOutput)(nil),
		&sesv2types.NotFoundException{Message: stringPtr("Event destination not found")},
	)

	result, err := prov.Delete(context.Background(), &resource.DeleteRequest{
		ResourceType: "AWS::SES::ConfigurationSetEventDestination",
		NativeID:     "my-cs|bounces",
	})

	require.NoError(t, err)
	require.NotNil(t, result.ProgressResult)
	require.Equal(t, resource.OperationStatusSuccess, result.ProgressResult.OperationStatus)
	require.Equal(t, resource.OperationErrorCodeNotFound, result.ProgressResult.ErrorCode)
}

func TestEventDestination_Delete_OtherSDKError_Propagates(t *testing.T) {
	client := &mockSesV2Client{}
	prov := newEventDestinationTestProvisioner(client)

	client.On("DeleteConfigurationSetEventDestination", mock.Anything, mock.Anything).Return(
		(*sesv2.DeleteConfigurationSetEventDestinationOutput)(nil), errors.New("AccessDenied"),
	)

	_, err := prov.Delete(context.Background(), &resource.DeleteRequest{
		ResourceType: "AWS::SES::ConfigurationSetEventDestination",
		NativeID:     "my-cs|bounces",
	})

	require.Error(t, err)
	require.Contains(t, err.Error(), "DeleteConfigurationSetEventDestination")
}

func TestEventDestination_List_WalksAllConfigurationSets_EmitsComposites(t *testing.T) {
	client := &mockSesV2Client{}
	prov := newEventDestinationTestProvisioner(client)

	// Page 1: two CSes
	client.On("ListConfigurationSets", mock.Anything, mock.MatchedBy(func(in *sesv2.ListConfigurationSetsInput) bool {
		return in.NextToken == nil
	})).Return(&sesv2.ListConfigurationSetsOutput{
		ConfigurationSets: []string{"cs-a", "cs-b"},
		NextToken:         stringPtr("page2"),
	}, nil)

	// Page 2: one CS, no further pages
	client.On("ListConfigurationSets", mock.Anything, mock.MatchedBy(func(in *sesv2.ListConfigurationSetsInput) bool {
		return in.NextToken != nil && *in.NextToken == "page2"
	})).Return(&sesv2.ListConfigurationSetsOutput{
		ConfigurationSets: []string{"cs-c"},
		NextToken:         nil,
	}, nil)

	client.On("GetConfigurationSetEventDestinations", mock.Anything, mock.MatchedBy(func(in *sesv2.GetConfigurationSetEventDestinationsInput) bool {
		return in.ConfigurationSetName != nil && *in.ConfigurationSetName == "cs-a"
	})).Return(&sesv2.GetConfigurationSetEventDestinationsOutput{
		EventDestinations: []sesv2types.EventDestination{{Name: stringPtr("bounces")}, {Name: stringPtr("complaints")}},
	}, nil)
	client.On("GetConfigurationSetEventDestinations", mock.Anything, mock.MatchedBy(func(in *sesv2.GetConfigurationSetEventDestinationsInput) bool {
		return in.ConfigurationSetName != nil && *in.ConfigurationSetName == "cs-b"
	})).Return(&sesv2.GetConfigurationSetEventDestinationsOutput{
		EventDestinations: []sesv2types.EventDestination{}, // no destinations
	}, nil)
	client.On("GetConfigurationSetEventDestinations", mock.Anything, mock.MatchedBy(func(in *sesv2.GetConfigurationSetEventDestinationsInput) bool {
		return in.ConfigurationSetName != nil && *in.ConfigurationSetName == "cs-c"
	})).Return(&sesv2.GetConfigurationSetEventDestinationsOutput{
		EventDestinations: []sesv2types.EventDestination{{Name: stringPtr("deliveries")}},
	}, nil)

	result, err := prov.List(context.Background(), &resource.ListRequest{
		ResourceType: "AWS::SES::ConfigurationSetEventDestination",
	})

	require.NoError(t, err)
	require.NotNil(t, result)
	require.ElementsMatch(t, []string{"cs-a|bounces", "cs-a|complaints", "cs-c|deliveries"}, result.NativeIDs,
		"List must emit composite IDs so subsequent Read/Update/Delete have the parent CS context")
}

func TestEventDestination_List_SkipsCSWhereGetFails(t *testing.T) {
	// A CS that we can't read shouldn't fail the entire discovery scan —
	// just skip it. AWS commonly returns AccessDenied for CSes we don't own.
	client := &mockSesV2Client{}
	prov := newEventDestinationTestProvisioner(client)

	client.On("ListConfigurationSets", mock.Anything, mock.Anything).Return(&sesv2.ListConfigurationSetsOutput{
		ConfigurationSets: []string{"cs-good", "cs-broken"},
	}, nil)
	client.On("GetConfigurationSetEventDestinations", mock.Anything, mock.MatchedBy(func(in *sesv2.GetConfigurationSetEventDestinationsInput) bool {
		return in.ConfigurationSetName != nil && *in.ConfigurationSetName == "cs-good"
	})).Return(&sesv2.GetConfigurationSetEventDestinationsOutput{
		EventDestinations: []sesv2types.EventDestination{{Name: stringPtr("bounces")}},
	}, nil)
	client.On("GetConfigurationSetEventDestinations", mock.Anything, mock.MatchedBy(func(in *sesv2.GetConfigurationSetEventDestinationsInput) bool {
		return in.ConfigurationSetName != nil && *in.ConfigurationSetName == "cs-broken"
	})).Return((*sesv2.GetConfigurationSetEventDestinationsOutput)(nil), errors.New("AccessDenied"))

	result, err := prov.List(context.Background(), &resource.ListRequest{
		ResourceType: "AWS::SES::ConfigurationSetEventDestination",
	})

	require.NoError(t, err)
	require.Equal(t, []string{"cs-good|bounces"}, result.NativeIDs)
}

func TestEventDestination_List_ListConfigurationSetsError_Propagates(t *testing.T) {
	// If we can't even start listing CSes, fail loudly — that's not a
	// recoverable per-CS hiccup.
	client := &mockSesV2Client{}
	prov := newEventDestinationTestProvisioner(client)

	client.On("ListConfigurationSets", mock.Anything, mock.Anything).Return(
		(*sesv2.ListConfigurationSetsOutput)(nil), errors.New("throttled"),
	)

	_, err := prov.List(context.Background(), &resource.ListRequest{
		ResourceType: "AWS::SES::ConfigurationSetEventDestination",
	})

	require.Error(t, err)
	require.Contains(t, err.Error(), "ListConfigurationSets")
}

func stringPtr(s string) *string { return &s }
