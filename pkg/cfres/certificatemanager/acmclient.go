// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package certificatemanager

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/service/acm"
)

// ACMClientInterface is the narrow surface of the ACM SDK client used by
// formae-plugin-aws. Defined explicitly (rather than aliased from the SDK)
// so unit tests can mock just the methods we actually call.
type ACMClientInterface interface {
	RequestCertificate(ctx context.Context, params *acm.RequestCertificateInput, optFns ...func(*acm.Options)) (*acm.RequestCertificateOutput, error)
	DescribeCertificate(ctx context.Context, params *acm.DescribeCertificateInput, optFns ...func(*acm.Options)) (*acm.DescribeCertificateOutput, error)
	DeleteCertificate(ctx context.Context, params *acm.DeleteCertificateInput, optFns ...func(*acm.Options)) (*acm.DeleteCertificateOutput, error)
	AddTagsToCertificate(ctx context.Context, params *acm.AddTagsToCertificateInput, optFns ...func(*acm.Options)) (*acm.AddTagsToCertificateOutput, error)
	RemoveTagsFromCertificate(ctx context.Context, params *acm.RemoveTagsFromCertificateInput, optFns ...func(*acm.Options)) (*acm.RemoveTagsFromCertificateOutput, error)
	ListTagsForCertificate(ctx context.Context, params *acm.ListTagsForCertificateInput, optFns ...func(*acm.Options)) (*acm.ListTagsForCertificateOutput, error)
	ListCertificates(ctx context.Context, params *acm.ListCertificatesInput, optFns ...func(*acm.Options)) (*acm.ListCertificatesOutput, error)
	UpdateCertificateOptions(ctx context.Context, params *acm.UpdateCertificateOptionsInput, optFns ...func(*acm.Options)) (*acm.UpdateCertificateOptionsOutput, error)
}
