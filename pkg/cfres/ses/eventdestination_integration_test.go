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
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/ccx"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/config"
)

func TestEventDestination_Integration_CCAPISwapType(t *testing.T) {
	region := os.Getenv("AWS_REGION")
	if region == "" {
		t.Skip("AWS_REGION not set")
	}
	ctx := context.Background()
	cfg := &config.Config{Region: region}
	awsCfg, err := cfg.ToAwsConfig(ctx)
	require.NoError(t, err)

	csName := "formae-conformance-cs-ed-" + randomSuffix(t)
	edName := "ed-test"

	// Pre-create the parent ConfigurationSet via SDK to keep the test focused.
	sesSdk := sesv2.NewFromConfig(awsCfg)
	_, err = sesSdk.CreateConfigurationSet(ctx, &sesv2.CreateConfigurationSetInput{
		ConfigurationSetName: aws.String(csName),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = sesSdk.DeleteConfigurationSet(context.Background(), &sesv2.DeleteConfigurationSetInput{
			ConfigurationSetName: aws.String(csName),
		})
	})

	// Pre-create an SNS topic for the destination wiring.
	snsSdk := sns.NewFromConfig(awsCfg)
	topicResp, err := snsSdk.CreateTopic(ctx, &sns.CreateTopicInput{Name: aws.String(csName + "-bounces")})
	require.NoError(t, err)
	topicArn := *topicResp.TopicArn
	t.Cleanup(func() {
		_, _ = snsSdk.DeleteTopic(context.Background(), &sns.DeleteTopicInput{TopicArn: aws.String(topicArn)})
	})

	ccxClient, err := ccx.NewClient(cfg)
	require.NoError(t, err)

	createProps, _ := json.Marshal(map[string]any{
		"EventDestinationName": edName,
		"ConfigurationSetName": csName,
		"EventDestination": map[string]any{
			"Enabled":            true,
			"MatchingEventTypes": []string{"BOUNCE", "COMPLAINT"},
			"SnsDestination":     map[string]any{"TopicARN": topicArn},
		},
	})
	_, err = ccxClient.CreateResource(ctx, &resource.CreateRequest{
		ResourceType: "AWS::SES::EventDestination",
		Properties:   createProps,
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = sesSdk.DeleteConfigurationSetEventDestination(context.Background(), &sesv2.DeleteConfigurationSetEventDestinationInput{
			ConfigurationSetName: aws.String(csName),
			EventDestinationName: aws.String(edName),
		})
	})

	// Update: keep SNS but flip enabled false. ccx.UpdateResource takes a
	// JSON-Patch document, not a full property bag.
	patchDoc := `[{"op":"replace","path":"/EventDestination/Enabled","value":false}]`
	nativeID := csName + "|" + edName
	_, err = ccxClient.UpdateResource(ctx, &resource.UpdateRequest{
		ResourceType:  "AWS::SES::EventDestination",
		NativeID:      nativeID,
		PatchDocument: &patchDoc,
	})
	require.NoError(t, err)

	rr, err := ccxClient.ReadResource(ctx, &resource.ReadRequest{
		ResourceType: "AWS::SES::EventDestination",
		NativeID:     nativeID,
	})
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(rr.Properties), &got))
	dest := got["EventDestination"].(map[string]any)
	assert.Equal(t, false, dest["Enabled"])
}
