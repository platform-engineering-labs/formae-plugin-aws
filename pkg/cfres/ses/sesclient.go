// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package ses

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

// SesV2ClientInterface is the narrow surface of the SESv2 SDK client used by
// formae-plugin-aws. Defined explicitly (rather than aliased from the SDK) so
// unit tests can mock just the methods we actually call.
type SesV2ClientInterface interface {
	GetEmailIdentity(ctx context.Context, params *sesv2.GetEmailIdentityInput, optFns ...func(*sesv2.Options)) (*sesv2.GetEmailIdentityOutput, error)
	GetConfigurationSetEventDestinations(ctx context.Context, params *sesv2.GetConfigurationSetEventDestinationsInput, optFns ...func(*sesv2.Options)) (*sesv2.GetConfigurationSetEventDestinationsOutput, error)
	UpdateConfigurationSetEventDestination(ctx context.Context, params *sesv2.UpdateConfigurationSetEventDestinationInput, optFns ...func(*sesv2.Options)) (*sesv2.UpdateConfigurationSetEventDestinationOutput, error)
	DeleteConfigurationSetEventDestination(ctx context.Context, params *sesv2.DeleteConfigurationSetEventDestinationInput, optFns ...func(*sesv2.Options)) (*sesv2.DeleteConfigurationSetEventDestinationOutput, error)
	ListConfigurationSets(ctx context.Context, params *sesv2.ListConfigurationSetsInput, optFns ...func(*sesv2.Options)) (*sesv2.ListConfigurationSetsOutput, error)

	// EmailIdentity Update applies each attribute group via its own SESv2 call,
	// bypassing CloudControl's async update handler (which fails intermittently
	// with GeneralServiceException "security token invalid" in the SES path).
	PutEmailIdentityMailFromAttributes(ctx context.Context, params *sesv2.PutEmailIdentityMailFromAttributesInput, optFns ...func(*sesv2.Options)) (*sesv2.PutEmailIdentityMailFromAttributesOutput, error)
	PutEmailIdentityDkimSigningAttributes(ctx context.Context, params *sesv2.PutEmailIdentityDkimSigningAttributesInput, optFns ...func(*sesv2.Options)) (*sesv2.PutEmailIdentityDkimSigningAttributesOutput, error)
	PutEmailIdentityFeedbackAttributes(ctx context.Context, params *sesv2.PutEmailIdentityFeedbackAttributesInput, optFns ...func(*sesv2.Options)) (*sesv2.PutEmailIdentityFeedbackAttributesOutput, error)
	PutEmailIdentityConfigurationSetAttributes(ctx context.Context, params *sesv2.PutEmailIdentityConfigurationSetAttributesInput, optFns ...func(*sesv2.Options)) (*sesv2.PutEmailIdentityConfigurationSetAttributesOutput, error)
	TagResource(ctx context.Context, params *sesv2.TagResourceInput, optFns ...func(*sesv2.Options)) (*sesv2.TagResourceOutput, error)
	UntagResource(ctx context.Context, params *sesv2.UntagResourceInput, optFns ...func(*sesv2.Options)) (*sesv2.UntagResourceOutput, error)
}

// stsClientInterface is the narrow STS surface used to resolve the account ID
// for building the EmailIdentity ARN that SESv2 Tag/Untag calls require.
type stsClientInterface interface {
	GetCallerIdentity(ctx context.Context, params *sts.GetCallerIdentityInput, optFns ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error)
}
