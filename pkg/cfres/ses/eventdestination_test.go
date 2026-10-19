// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package ses

import (
	"context"
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

func TestEventDestination_Delete_SDKCall_SplitsCompositeIdentifier(t *testing.T) {
	client := &mockSesV2Client{}
	prov := newEventDestinationTestProvisioner(client)

	client.On("DeleteConfigurationSetEventDestination", mock.Anything, mock.MatchedBy(func(input *sesv2.DeleteConfigurationSetEventDestinationInput) bool {
		return input.ConfigurationSetName != nil && *input.ConfigurationSetName == "my-cs" &&
			input.EventDestinationName != nil && *input.EventDestinationName == "bounces"
	})).Return(&sesv2.DeleteConfigurationSetEventDestinationOutput{}, nil)

	result, err := prov.Delete(context.Background(), &resource.DeleteRequest{
		ResourceType: "AWS::SES::ConfigurationSetEventDestination",
		NativeID:     "bounces|my-cs",
	})

	require.NoError(t, err)
	require.NotNil(t, result.ProgressResult)
	require.Equal(t, resource.OperationStatusSuccess, result.ProgressResult.OperationStatus)
	require.Equal(t, "bounces|my-cs", result.ProgressResult.NativeID)
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

func TestEventDestination_Delete_MissingConfigurationSetSegment_ReturnsError(t *testing.T) {
	prov := newEventDestinationTestProvisioner(&mockSesV2Client{})

	_, err := prov.Delete(context.Background(), &resource.DeleteRequest{
		ResourceType: "AWS::SES::ConfigurationSetEventDestination",
		NativeID:     "bounces|",
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
		NativeID:     "bounces|my-cs",
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
		NativeID:     "bounces|my-cs",
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
	require.ElementsMatch(t, []string{"bounces|cs-a", "complaints|cs-a", "deliveries|cs-c"}, result.NativeIDs,
		"List must emit the composite identifier CloudControl reports, so discovery can Read what it finds")
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
	require.Equal(t, []string{"bounces|cs-good"}, result.NativeIDs)
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
