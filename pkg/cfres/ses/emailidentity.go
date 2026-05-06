// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package ses

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	sesv2types "github.com/aws/aws-sdk-go-v2/service/sesv2/types"
)

// DnsRecord is the Go representation of the schema/pkl/ses/ses.pkl DnsRecord
// value-object. JSON tag names match what CloudControl emits via the
// Properties payload, which the agent serializes into the Resource.
type DnsRecord struct {
	Type           string   `json:"Type"`
	Name           string   `json:"Name"`
	Values         []string `json:"Values"`
	RecommendedTtl int      `json:"RecommendedTtl"`
	Priority       *int     `json:"Priority,omitempty"`
}

// synthesizeFromIdentity calls SESv2 GetEmailIdentity and converts the
// response into the formae-side outputs (requiredDnsRecords, verification
// status, dkim verified). region is the AWS region the identity lives in,
// used to construct MAIL FROM MX targets.
//
//nolint:unused // wired into the Provisioner Read path in a follow-up commit.
func synthesizeFromIdentity(
	ctx context.Context,
	client SesV2ClientInterface,
	emailIdentity string,
	region string,
) (records []DnsRecord, status sesv2types.VerificationStatus, dkimVerified bool, err error) {
	resp, err := client.GetEmailIdentity(ctx, &sesv2.GetEmailIdentityInput{
		EmailIdentity: &emailIdentity,
	})
	if err != nil {
		return nil, "", false, err
	}

	status = resp.VerificationStatus
	dkimVerified = resp.VerifiedForSendingStatus

	// Email-address identities never get DKIM CNAMEs.
	if resp.IdentityType != sesv2types.IdentityTypeDomain {
		return nil, status, dkimVerified, nil
	}

	if resp.DkimAttributes != nil {
		for _, tok := range resp.DkimAttributes.Tokens {
			records = append(records, DnsRecord{
				Type:           "CNAME",
				Name:           tok + "._domainkey." + emailIdentity,
				Values:         []string{tok + ".dkim.amazonses.com"},
				RecommendedTtl: 300,
			})
		}
	}

	if resp.MailFromAttributes != nil && resp.MailFromAttributes.MailFromDomain != nil &&
		*resp.MailFromAttributes.MailFromDomain != "" {
		mailFrom := *resp.MailFromAttributes.MailFromDomain
		mxPriority := 10
		records = append(records, DnsRecord{
			Type:           "MX",
			Name:           mailFrom,
			Values:         []string{"feedback-smtp." + region + ".amazonses.com"},
			RecommendedTtl: 300,
			Priority:       &mxPriority,
		})
		records = append(records, DnsRecord{
			Type:           "TXT",
			Name:           mailFrom,
			Values:         []string{"v=spf1 include:amazonses.com ~all"},
			RecommendedTtl: 300,
		})
	}
	return records, status, dkimVerified, nil
}
