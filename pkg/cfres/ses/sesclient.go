// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package ses

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/service/sesv2"
)

// SesV2ClientInterface is the narrow surface of the SESv2 SDK client used by
// formae-plugin-aws. Defined explicitly (rather than aliased from the SDK) so
// unit tests can mock just the methods we actually call.
type SesV2ClientInterface interface {
	GetEmailIdentity(ctx context.Context, params *sesv2.GetEmailIdentityInput, optFns ...func(*sesv2.Options)) (*sesv2.GetEmailIdentityOutput, error)
	GetConfigurationSetEventDestinations(ctx context.Context, params *sesv2.GetConfigurationSetEventDestinationsInput, optFns ...func(*sesv2.Options)) (*sesv2.GetConfigurationSetEventDestinationsOutput, error)
}
