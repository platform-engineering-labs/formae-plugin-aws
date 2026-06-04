// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package certificatemanager

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/service/acm"
)

// ACMClientInterface is the narrow surface of the ACM SDK client used by
// formae-plugin-aws. Defined explicitly (rather than aliased) so unit tests
// can mock just the methods we actually call.
type ACMClientInterface interface {
	DescribeCertificate(ctx context.Context, params *acm.DescribeCertificateInput, optFns ...func(*acm.Options)) (*acm.DescribeCertificateOutput, error)
}
