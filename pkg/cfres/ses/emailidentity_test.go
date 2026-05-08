// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package ses

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	sesv2types "github.com/aws/aws-sdk-go-v2/service/sesv2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

func TestSynthesizeRequiredDnsRecords_EasyDkimOnly_ReturnsThreeCnames(t *testing.T) {
	ctx := context.Background()
	client := &mockSesV2Client{}

	tokens := []string{"abc123token", "def456token", "ghi789token"}

	client.On("GetEmailIdentity", ctx, mock.MatchedBy(func(in *sesv2.GetEmailIdentityInput) bool {
		return in.EmailIdentity != nil && *in.EmailIdentity == "example.com"
	})).Return(&sesv2.GetEmailIdentityOutput{
		IdentityType: sesv2types.IdentityTypeDomain,
		DkimAttributes: &sesv2types.DkimAttributes{
			Status: sesv2types.DkimStatusPending,
			Tokens: tokens,
		},
		MailFromAttributes:       nil,
		VerificationStatus:       sesv2types.VerificationStatusPending,
		VerifiedForSendingStatus: false,
	}, nil)

	records, status, dkim, err := synthesizeFromIdentity(ctx, client, "example.com", "us-east-1")

	assert.NoError(t, err)
	assert.Equal(t, "PENDING", string(status))
	assert.False(t, dkim)
	assert.Len(t, records, 3)
	for i, tok := range tokens {
		assert.Equal(t, "CNAME", records[i].Type)
		assert.Equal(t, tok+"._domainkey.example.com", records[i].Name)
		assert.Equal(t, []string{tok + ".dkim.amazonses.com"}, records[i].Values)
		assert.Equal(t, 300, records[i].RecommendedTtl)
	}
	client.AssertExpectations(t)
}

func TestSynthesizeRequiredDnsRecords_MailFromIncludesMxAndSpf(t *testing.T) {
	ctx := context.Background()
	client := &mockSesV2Client{}

	mailFrom := "mail.example.com"

	client.On("GetEmailIdentity", ctx, mock.Anything).Return(&sesv2.GetEmailIdentityOutput{
		IdentityType: sesv2types.IdentityTypeDomain,
		DkimAttributes: &sesv2types.DkimAttributes{
			Tokens: []string{"a", "b", "c"},
		},
		MailFromAttributes: &sesv2types.MailFromAttributes{
			MailFromDomain:       aws.String(mailFrom),
			BehaviorOnMxFailure:  sesv2types.BehaviorOnMxFailureUseDefaultValue,
			MailFromDomainStatus: sesv2types.MailFromDomainStatusPending,
		},
		VerificationStatus:       sesv2types.VerificationStatusPending,
		VerifiedForSendingStatus: false,
	}, nil)

	records, _, _, err := synthesizeFromIdentity(ctx, client, "example.com", "eu-central-1")
	assert.NoError(t, err)
	assert.Len(t, records, 5, "3 DKIM CNAMEs + 1 MX + 1 SPF TXT = 5")

	// Find the MX record.
	var mx, spf *DnsRecord
	for i := range records {
		switch records[i].Type {
		case "MX":
			mx = &records[i]
		case "TXT":
			spf = &records[i]
		}
	}
	assert.NotNil(t, mx, "MX record present")
	assert.Equal(t, mailFrom, mx.Name)
	assert.Equal(t, []string{"10 feedback-smtp.eu-central-1.amazonses.com"}, mx.Values)
	assert.NotNil(t, mx.Priority)
	assert.Equal(t, 10, *mx.Priority)

	assert.NotNil(t, spf, "SPF TXT record present")
	assert.Equal(t, mailFrom, spf.Name)
	assert.Equal(t, []string{"v=spf1 include:amazonses.com ~all"}, spf.Values)
}

func TestSynthesizeRequiredDnsRecords_EmailAddressIdentity_NoRecords(t *testing.T) {
	ctx := context.Background()
	client := &mockSesV2Client{}

	client.On("GetEmailIdentity", ctx, mock.Anything).Return(&sesv2.GetEmailIdentityOutput{
		IdentityType:             sesv2types.IdentityTypeEmailAddress,
		DkimAttributes:           nil,
		MailFromAttributes:       nil,
		VerificationStatus:       sesv2types.VerificationStatusPending,
		VerifiedForSendingStatus: false,
	}, nil)

	records, status, dkim, err := synthesizeFromIdentity(ctx, client, "alice@example.com", "us-east-1")
	assert.NoError(t, err)
	assert.Empty(t, records, "email-address identities have no DNS records")
	assert.Equal(t, "PENDING", string(status))
	assert.False(t, dkim)
}

func TestSynthesizeRequiredDnsRecords_VerifiedIdentity_PassesThroughStatus(t *testing.T) {
	ctx := context.Background()
	client := &mockSesV2Client{}

	client.On("GetEmailIdentity", ctx, mock.Anything).Return(&sesv2.GetEmailIdentityOutput{
		IdentityType: sesv2types.IdentityTypeDomain,
		DkimAttributes: &sesv2types.DkimAttributes{
			Tokens: []string{"x", "y", "z"},
			Status: sesv2types.DkimStatusSuccess,
		},
		VerificationStatus:       sesv2types.VerificationStatusSuccess,
		VerifiedForSendingStatus: true,
	}, nil)

	_, status, dkim, err := synthesizeFromIdentity(ctx, client, "example.com", "us-east-1")
	assert.NoError(t, err)
	assert.Equal(t, "SUCCESS", string(status))
	assert.True(t, dkim)
}
