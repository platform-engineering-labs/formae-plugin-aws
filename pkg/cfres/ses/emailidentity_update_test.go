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
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/config"
)

const (
	testIdentity = "formae-conformance-abc123.example.com"
	testAccount  = "123456789012"
)

func newEmailIdentityForUpdate(sesClient SesV2ClientInterface) *EmailIdentity {
	return &EmailIdentity{
		cfg:              &config.Config{Region: "us-east-1"},
		sesClientFactory: func(*config.Config) (SesV2ClientInterface, error) { return sesClient, nil },
	}
}

func TestUpdate_AppliesChangedAttributesViaSesV2(t *testing.T) {
	ctx := context.Background()
	sesClient := &mockSesV2Client{}
	stsClient := &mockStsClient{}

	stsClient.On("GetCallerIdentity", ctx, mock.Anything).
		Return(&sts.GetCallerIdentityOutput{Account: aws.String(testAccount)}, nil)

	sesClient.On("PutEmailIdentityMailFromAttributes", ctx, mock.MatchedBy(func(in *sesv2.PutEmailIdentityMailFromAttributesInput) bool {
		return aws.ToString(in.EmailIdentity) == testIdentity &&
			in.BehaviorOnMxFailure == sesv2types.BehaviorOnMxFailureRejectMessage &&
			aws.ToString(in.MailFromDomain) == "mail."+testIdentity
	})).Return(&sesv2.PutEmailIdentityMailFromAttributesOutput{}, nil)

	sesClient.On("PutEmailIdentityDkimSigningAttributes", ctx, mock.MatchedBy(func(in *sesv2.PutEmailIdentityDkimSigningAttributesInput) bool {
		return aws.ToString(in.EmailIdentity) == testIdentity &&
			in.SigningAttributesOrigin == sesv2types.DkimSigningAttributesOriginAwsSes &&
			in.SigningAttributes != nil &&
			in.SigningAttributes.NextSigningKeyLength == sesv2types.DkimSigningKeyLengthRsa1024Bit
	})).Return(&sesv2.PutEmailIdentityDkimSigningAttributesOutput{}, nil)

	sesClient.On("PutEmailIdentityFeedbackAttributes", ctx, mock.MatchedBy(func(in *sesv2.PutEmailIdentityFeedbackAttributesInput) bool {
		return aws.ToString(in.EmailIdentity) == testIdentity && in.EmailForwardingEnabled == false
	})).Return(&sesv2.PutEmailIdentityFeedbackAttributesOutput{}, nil)

	wantArn := "arn:aws:ses:us-east-1:" + testAccount + ":identity/" + testIdentity
	sesClient.On("TagResource", ctx, mock.MatchedBy(func(in *sesv2.TagResourceInput) bool {
		return aws.ToString(in.ResourceArn) == wantArn && len(in.Tags) == 1 &&
			aws.ToString(in.Tags[0].Key) == "Environment" && aws.ToString(in.Tags[0].Value) == "updated"
	})).Return(&sesv2.TagResourceOutput{}, nil)

	prior := `{"EmailIdentity":"` + testIdentity + `","MailFromAttributes":{"BehaviorOnMxFailure":"USE_DEFAULT_VALUE","MailFromDomain":"mail.` + testIdentity + `"},"DkimSigningAttributes":{"NextSigningKeyLength":"RSA_2048_BIT"},"FeedbackAttributes":{"EmailForwardingEnabled":true},"Tags":[{"Key":"Environment","Value":"test"}]}`
	desired := `{"EmailIdentity":"` + testIdentity + `","MailFromAttributes":{"BehaviorOnMxFailure":"REJECT_MESSAGE","MailFromDomain":"mail.` + testIdentity + `"},"DkimSigningAttributes":{"NextSigningKeyLength":"RSA_1024_BIT"},"FeedbackAttributes":{"EmailForwardingEnabled":false},"Tags":[{"Key":"Environment","Value":"updated"}]}`

	e := newEmailIdentityForUpdate(sesClient)
	res, err := e.updateWithClient(ctx, sesClient, stsClient, &resource.UpdateRequest{
		NativeID:          testIdentity,
		ResourceType:      "AWS::SES::EmailIdentity",
		PriorProperties:   []byte(prior),
		DesiredProperties: []byte(desired),
	})

	assert.NoError(t, err)
	assert.NotNil(t, res)
	assert.Equal(t, resource.OperationStatusSuccess, res.ProgressResult.OperationStatus)
	assert.Equal(t, testIdentity, res.ProgressResult.NativeID)
	sesClient.AssertExpectations(t)
	stsClient.AssertExpectations(t)
}

func TestUpdate_DkimUnchanged_SkipsDkimPut(t *testing.T) {
	ctx := context.Background()
	sesClient := &mockSesV2Client{}
	stsClient := &mockStsClient{}

	// Only feedback changes; DKIM key length identical in prior/desired.
	sesClient.On("PutEmailIdentityFeedbackAttributes", ctx, mock.Anything).
		Return(&sesv2.PutEmailIdentityFeedbackAttributesOutput{}, nil)

	prior := `{"EmailIdentity":"` + testIdentity + `","DkimSigningAttributes":{"NextSigningKeyLength":"RSA_2048_BIT"},"FeedbackAttributes":{"EmailForwardingEnabled":true}}`
	desired := `{"EmailIdentity":"` + testIdentity + `","DkimSigningAttributes":{"NextSigningKeyLength":"RSA_2048_BIT"},"FeedbackAttributes":{"EmailForwardingEnabled":false}}`

	e := newEmailIdentityForUpdate(sesClient)
	res, err := e.updateWithClient(ctx, sesClient, stsClient, &resource.UpdateRequest{
		NativeID:          testIdentity,
		ResourceType:      "AWS::SES::EmailIdentity",
		PriorProperties:   []byte(prior),
		DesiredProperties: []byte(desired),
	})

	assert.NoError(t, err)
	assert.Equal(t, resource.OperationStatusSuccess, res.ProgressResult.OperationStatus)
	sesClient.AssertNotCalled(t, "PutEmailIdentityDkimSigningAttributes", mock.Anything, mock.Anything)
	// No tag changes → no STS call needed.
	stsClient.AssertNotCalled(t, "GetCallerIdentity", mock.Anything, mock.Anything)
	sesClient.AssertExpectations(t)
}

func TestUpdate_RemovedTag_IsUntagged(t *testing.T) {
	ctx := context.Background()
	sesClient := &mockSesV2Client{}
	stsClient := &mockStsClient{}

	stsClient.On("GetCallerIdentity", ctx, mock.Anything).
		Return(&sts.GetCallerIdentityOutput{Account: aws.String(testAccount)}, nil)

	wantArn := "arn:aws:ses:us-east-1:" + testAccount + ":identity/" + testIdentity
	sesClient.On("UntagResource", ctx, mock.MatchedBy(func(in *sesv2.UntagResourceInput) bool {
		return aws.ToString(in.ResourceArn) == wantArn && len(in.TagKeys) == 1 && in.TagKeys[0] == "Team"
	})).Return(&sesv2.UntagResourceOutput{}, nil)

	prior := `{"EmailIdentity":"` + testIdentity + `","Tags":[{"Key":"Environment","Value":"test"},{"Key":"Team","Value":"infra"}]}`
	desired := `{"EmailIdentity":"` + testIdentity + `","Tags":[{"Key":"Environment","Value":"test"}]}`

	e := newEmailIdentityForUpdate(sesClient)
	res, err := e.updateWithClient(ctx, sesClient, stsClient, &resource.UpdateRequest{
		NativeID:          testIdentity,
		ResourceType:      "AWS::SES::EmailIdentity",
		PriorProperties:   []byte(prior),
		DesiredProperties: []byte(desired),
	})

	assert.NoError(t, err)
	assert.Equal(t, resource.OperationStatusSuccess, res.ProgressResult.OperationStatus)
	// Environment tag unchanged → not re-tagged.
	sesClient.AssertNotCalled(t, "TagResource", mock.Anything, mock.Anything)
	sesClient.AssertExpectations(t)
	stsClient.AssertExpectations(t)
}
