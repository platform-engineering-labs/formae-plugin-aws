// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package ses

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/stretchr/testify/mock"
)

type mockSesV2Client struct {
	mock.Mock
}

func (m *mockSesV2Client) GetEmailIdentity(ctx context.Context, input *sesv2.GetEmailIdentityInput, optFns ...func(*sesv2.Options)) (*sesv2.GetEmailIdentityOutput, error) {
	args := m.Called(ctx, input)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*sesv2.GetEmailIdentityOutput), args.Error(1)
}

func (m *mockSesV2Client) GetConfigurationSetEventDestinations(ctx context.Context, input *sesv2.GetConfigurationSetEventDestinationsInput, optFns ...func(*sesv2.Options)) (*sesv2.GetConfigurationSetEventDestinationsOutput, error) {
	args := m.Called(ctx, input)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*sesv2.GetConfigurationSetEventDestinationsOutput), args.Error(1)
}

func (m *mockSesV2Client) UpdateConfigurationSetEventDestination(ctx context.Context, input *sesv2.UpdateConfigurationSetEventDestinationInput, optFns ...func(*sesv2.Options)) (*sesv2.UpdateConfigurationSetEventDestinationOutput, error) {
	args := m.Called(ctx, input)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*sesv2.UpdateConfigurationSetEventDestinationOutput), args.Error(1)
}

func (m *mockSesV2Client) DeleteConfigurationSetEventDestination(ctx context.Context, input *sesv2.DeleteConfigurationSetEventDestinationInput, optFns ...func(*sesv2.Options)) (*sesv2.DeleteConfigurationSetEventDestinationOutput, error) {
	args := m.Called(ctx, input)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*sesv2.DeleteConfigurationSetEventDestinationOutput), args.Error(1)
}

func (m *mockSesV2Client) ListConfigurationSets(ctx context.Context, input *sesv2.ListConfigurationSetsInput, optFns ...func(*sesv2.Options)) (*sesv2.ListConfigurationSetsOutput, error) {
	args := m.Called(ctx, input)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*sesv2.ListConfigurationSetsOutput), args.Error(1)
}

func (m *mockSesV2Client) PutEmailIdentityMailFromAttributes(ctx context.Context, input *sesv2.PutEmailIdentityMailFromAttributesInput, optFns ...func(*sesv2.Options)) (*sesv2.PutEmailIdentityMailFromAttributesOutput, error) {
	args := m.Called(ctx, input)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*sesv2.PutEmailIdentityMailFromAttributesOutput), args.Error(1)
}

func (m *mockSesV2Client) PutEmailIdentityDkimSigningAttributes(ctx context.Context, input *sesv2.PutEmailIdentityDkimSigningAttributesInput, optFns ...func(*sesv2.Options)) (*sesv2.PutEmailIdentityDkimSigningAttributesOutput, error) {
	args := m.Called(ctx, input)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*sesv2.PutEmailIdentityDkimSigningAttributesOutput), args.Error(1)
}

func (m *mockSesV2Client) PutEmailIdentityFeedbackAttributes(ctx context.Context, input *sesv2.PutEmailIdentityFeedbackAttributesInput, optFns ...func(*sesv2.Options)) (*sesv2.PutEmailIdentityFeedbackAttributesOutput, error) {
	args := m.Called(ctx, input)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*sesv2.PutEmailIdentityFeedbackAttributesOutput), args.Error(1)
}

func (m *mockSesV2Client) PutEmailIdentityConfigurationSetAttributes(ctx context.Context, input *sesv2.PutEmailIdentityConfigurationSetAttributesInput, optFns ...func(*sesv2.Options)) (*sesv2.PutEmailIdentityConfigurationSetAttributesOutput, error) {
	args := m.Called(ctx, input)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*sesv2.PutEmailIdentityConfigurationSetAttributesOutput), args.Error(1)
}

func (m *mockSesV2Client) TagResource(ctx context.Context, input *sesv2.TagResourceInput, optFns ...func(*sesv2.Options)) (*sesv2.TagResourceOutput, error) {
	args := m.Called(ctx, input)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*sesv2.TagResourceOutput), args.Error(1)
}

func (m *mockSesV2Client) UntagResource(ctx context.Context, input *sesv2.UntagResourceInput, optFns ...func(*sesv2.Options)) (*sesv2.UntagResourceOutput, error) {
	args := m.Called(ctx, input)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*sesv2.UntagResourceOutput), args.Error(1)
}

type mockStsClient struct {
	mock.Mock
}

func (m *mockStsClient) GetCallerIdentity(ctx context.Context, input *sts.GetCallerIdentityInput, optFns ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error) {
	args := m.Called(ctx, input)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*sts.GetCallerIdentityOutput), args.Error(1)
}
