// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package ses

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/service/sesv2"
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
