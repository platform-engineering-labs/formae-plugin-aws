// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build integration

package ses

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	sesv2types "github.com/aws/aws-sdk-go-v2/service/sesv2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/config"
)

func TestEmailIdentity_Integration_CreateReadDelete(t *testing.T) {
	region := os.Getenv("AWS_REGION")
	if region == "" {
		t.Skip("AWS_REGION not set; skipping integration test")
	}
	domain := os.Getenv("FORMAE_INTEGRATION_TEST_DOMAIN")
	if domain == "" {
		t.Skip("FORMAE_INTEGRATION_TEST_DOMAIN not set; skipping")
	}

	ctx := context.Background()
	cfg := config.Config{Region: region}

	prov := &EmailIdentity{cfg: &cfg, sesClientFactory: defaultSesV2ClientFactory}

	// 1. Create via CCAPI delegation.
	createProps, _ := json.Marshal(map[string]any{"EmailIdentity": domain})
	cr, err := prov.Create(ctx, &resource.CreateRequest{ResourceType: "AWS::SES::EmailIdentity", Properties: createProps})
	require.NoError(t, err)
	require.NotNil(t, cr.ProgressResult)

	// 2. Read with enrichment.
	rr, err := prov.Read(ctx, &resource.ReadRequest{
		ResourceType: "AWS::SES::EmailIdentity",
		NativeID:     domain,
	})
	require.NoError(t, err)

	var props map[string]any
	require.NoError(t, json.Unmarshal([]byte(rr.Properties), &props))
	records, ok := props["RequiredDnsRecords"].([]any)
	assert.True(t, ok, "RequiredDnsRecords present in enriched payload")
	assert.Len(t, records, 3, "domain identity emits 3 DKIM CNAMEs")
	assert.Contains(t, []string{"PENDING", "SUCCESS", "TEMPORARY_FAILURE", "NOT_STARTED"}, props["VerificationStatus"])

	// 3. Verify cleanup via SDK.
	awsCfg, err := cfg.ToAwsConfig(ctx)
	require.NoError(t, err)
	sdk := sesv2.NewFromConfig(awsCfg)
	_, err = sdk.DeleteEmailIdentity(ctx, &sesv2.DeleteEmailIdentityInput{EmailIdentity: aws.String(domain)})
	require.NoError(t, err)

	// 4. Confirm 404.
	_, err = sdk.GetEmailIdentity(ctx, &sesv2.GetEmailIdentityInput{EmailIdentity: aws.String(domain)})
	require.Error(t, err, "identity should be gone")
	var nf *sesv2types.NotFoundException
	assert.ErrorAs(t, err, &nf)
}
