// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package ses

import (
	"context"
	"testing"

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
