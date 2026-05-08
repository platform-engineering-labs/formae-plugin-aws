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
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/config"
)

// TestEmailIdentityVerification_Integration_TimesOutOnUnverified exercises the
// timeout failure path against real SES. The success path can't be tested in
// CI without DNS plumbing — that's covered by manual hub-bootstrap.
//
// The test creates an unverified SES identity, instantiates the gate
// provisioner with a 5s timeout, calls Create, sleeps 6s, then calls Status
// and asserts the result transitions to OperationStatusFailure with a
// "timeout" StatusMessage.
func TestEmailIdentityVerification_Integration_TimesOutOnUnverified(t *testing.T) {
	region := os.Getenv("AWS_REGION")
	if region == "" {
		t.Skip("AWS_REGION not set; skipping integration test")
	}
	domain := os.Getenv("FORMAE_INTEGRATION_TEST_DOMAIN")
	if domain == "" {
		t.Skip("FORMAE_INTEGRATION_TEST_DOMAIN not set; skipping")
	}

	ctx := context.Background()
	cfg := &config.Config{Region: region}
	awsCfg, err := cfg.ToAwsConfig(ctx)
	require.NoError(t, err)
	sdk := sesv2.NewFromConfig(awsCfg)

	// Register the unverified identity so the gate has something to poll.
	_, err = sdk.CreateEmailIdentity(ctx, &sesv2.CreateEmailIdentityInput{
		EmailIdentity: aws.String(domain),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = sdk.DeleteEmailIdentity(context.Background(), &sesv2.DeleteEmailIdentityInput{
			EmailIdentity: aws.String(domain),
		})
	})

	prov := &EmailIdentityVerification{
		cfg:              cfg,
		sesClientFactory: defaultSesV2ClientFactory,
		now:              func() time.Time { return time.Now().UTC() },
		timeout:          5 * time.Second,
	}

	createProps, _ := json.Marshal(map[string]any{"Identity": domain})
	cr, err := prov.Create(ctx, &resource.CreateRequest{
		ResourceType: "AWS::SES::EmailIdentityVerification",
		Properties:   createProps,
	})
	require.NoError(t, err)
	require.NotNil(t, cr.ProgressResult)
	assert.Equal(t, resource.OperationStatusInProgress, cr.ProgressResult.OperationStatus)
	require.NotEmpty(t, cr.ProgressResult.RequestID, "RequestID encodes deadline; required for Status")

	// Wait past the deadline, then poll Status. The gate decodes the deadline
	// from RequestID and should report failure with a "timeout" message.
	time.Sleep(6 * time.Second)

	sr, err := prov.Status(ctx, &resource.StatusRequest{
		RequestID: cr.ProgressResult.RequestID,
		NativeID:  cr.ProgressResult.NativeID,
	})
	require.NoError(t, err)
	require.NotNil(t, sr.ProgressResult)
	assert.Equal(t, resource.OperationStatusFailure, sr.ProgressResult.OperationStatus)
	assert.Contains(t, sr.ProgressResult.StatusMessage, "timeout")
}
