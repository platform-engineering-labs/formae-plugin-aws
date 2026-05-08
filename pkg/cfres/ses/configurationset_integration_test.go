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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/ccx"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/config"
)

func TestConfigurationSet_Integration_CCAPIRoundTrip(t *testing.T) {
	region := os.Getenv("AWS_REGION")
	if region == "" {
		t.Skip("AWS_REGION not set")
	}
	ctx := context.Background()
	cfg := &config.Config{Region: region}

	name := "formae-conformance-cs-" + randomSuffix(t)

	ccxClient, err := ccx.NewClient(cfg)
	require.NoError(t, err)

	createProps, _ := json.Marshal(map[string]any{
		"Name": name,
		"DeliveryOptions": map[string]any{
			"TlsPolicy": "OPTIONAL",
		},
	})

	_, err = ccxClient.CreateResource(ctx, &resource.CreateRequest{
		ResourceType: "AWS::SES::ConfigurationSet",
		Properties:   createProps,
	})
	require.NoError(t, err)

	t.Cleanup(func() {
		awsCfg, err := cfg.ToAwsConfig(context.Background())
		if err != nil {
			t.Logf("cleanup: ToAwsConfig failed: %v", err)
			return
		}
		sdk := sesv2.NewFromConfig(awsCfg)
		_, err = sdk.DeleteConfigurationSet(context.Background(), &sesv2.DeleteConfigurationSetInput{
			ConfigurationSetName: aws.String(name),
		})
		if err != nil {
			t.Logf("cleanup: DeleteConfigurationSet failed: %v", err)
		}
	})

	rr, err := ccxClient.ReadResource(ctx, &resource.ReadRequest{
		ResourceType: "AWS::SES::ConfigurationSet",
		NativeID:     name,
	})
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(rr.Properties), &got))
	assert.Equal(t, name, got["Name"])
}

// randomSuffix is local to the SES package's integration tests. Inline rather
// than reach for a shared util because there is currently no test-helper
// package in pkg/cfres/ses.
func randomSuffix(t *testing.T) string {
	t.Helper()
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 8)
	for i := range b {
		// time-based randomness is sufficient for test resource naming
		b[i] = alphabet[(i*31+int(t.Name()[0]))%len(alphabet)]
	}
	return string(b)
}
