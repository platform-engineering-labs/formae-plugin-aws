// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package cloudfront

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/service/cloudfront"
)

// CloudFrontClientInterface is the narrow surface of the CloudFront SDK
// client used by formae-plugin-aws. Defined explicitly (rather than
// aliased from the SDK) so unit tests can mock just the methods we
// actually call.
//
// Reads / mutations on Distribution and Function go through this
// interface; CCAPI handles Create/Delete/Read/List/Status for these
// types via the fall-through path in aws.go.
type CloudFrontClientInterface interface {
	GetDistributionConfig(ctx context.Context, params *cloudfront.GetDistributionConfigInput, optFns ...func(*cloudfront.Options)) (*cloudfront.GetDistributionConfigOutput, error)
	UpdateDistribution(ctx context.Context, params *cloudfront.UpdateDistributionInput, optFns ...func(*cloudfront.Options)) (*cloudfront.UpdateDistributionOutput, error)
	GetDistribution(ctx context.Context, params *cloudfront.GetDistributionInput, optFns ...func(*cloudfront.Options)) (*cloudfront.GetDistributionOutput, error)
	ListTagsForResource(ctx context.Context, params *cloudfront.ListTagsForResourceInput, optFns ...func(*cloudfront.Options)) (*cloudfront.ListTagsForResourceOutput, error)
	TagResource(ctx context.Context, params *cloudfront.TagResourceInput, optFns ...func(*cloudfront.Options)) (*cloudfront.TagResourceOutput, error)
	UntagResource(ctx context.Context, params *cloudfront.UntagResourceInput, optFns ...func(*cloudfront.Options)) (*cloudfront.UntagResourceOutput, error)

	DescribeFunction(ctx context.Context, params *cloudfront.DescribeFunctionInput, optFns ...func(*cloudfront.Options)) (*cloudfront.DescribeFunctionOutput, error)
	UpdateFunction(ctx context.Context, params *cloudfront.UpdateFunctionInput, optFns ...func(*cloudfront.Options)) (*cloudfront.UpdateFunctionOutput, error)
	PublishFunction(ctx context.Context, params *cloudfront.PublishFunctionInput, optFns ...func(*cloudfront.Options)) (*cloudfront.PublishFunctionOutput, error)
	GetFunction(ctx context.Context, params *cloudfront.GetFunctionInput, optFns ...func(*cloudfront.Options)) (*cloudfront.GetFunctionOutput, error)
}
