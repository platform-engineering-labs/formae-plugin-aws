// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package ses

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	sesv2types "github.com/aws/aws-sdk-go-v2/service/sesv2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
)

func TestVerificationStatusEvaluator_Pending_ReturnsInProgress(t *testing.T) {
	ctx := context.Background()
	client := &mockSesV2Client{}
	client.On("GetEmailIdentity", ctx, mock.Anything).Return(&sesv2.GetEmailIdentityOutput{
		IdentityType:             sesv2types.IdentityTypeDomain,
		VerificationStatus:       sesv2types.VerificationStatusPending,
		VerifiedForSendingStatus: false,
	}, nil)

	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	deadline := now.Add(15 * time.Minute)

	res, err := evaluateVerification(ctx, client, "example.com", deadline, now)
	assert.NoError(t, err)
	assert.Equal(t, resource.OperationStatusInProgress, res.OperationStatus)
}

func TestVerificationStatusEvaluator_Success_ReturnsSuccess(t *testing.T) {
	ctx := context.Background()
	client := &mockSesV2Client{}
	client.On("GetEmailIdentity", ctx, mock.Anything).Return(&sesv2.GetEmailIdentityOutput{
		IdentityType:             sesv2types.IdentityTypeDomain,
		VerificationStatus:       sesv2types.VerificationStatusSuccess,
		VerifiedForSendingStatus: true,
	}, nil)

	now := time.Now().UTC()
	res, err := evaluateVerification(ctx, client, "example.com", now.Add(time.Hour), now)
	assert.NoError(t, err)
	assert.Equal(t, resource.OperationStatusSuccess, res.OperationStatus)
	assert.Equal(t, "SUCCESS", res.VerificationStatusString)
	assert.True(t, res.DkimVerified)
	assert.NotEmpty(t, res.VerifiedAt)
}

func TestVerificationStatusEvaluator_Failed_ReturnsFailed(t *testing.T) {
	ctx := context.Background()
	client := &mockSesV2Client{}
	client.On("GetEmailIdentity", ctx, mock.Anything).Return(&sesv2.GetEmailIdentityOutput{
		IdentityType:             sesv2types.IdentityTypeDomain,
		VerificationStatus:       sesv2types.VerificationStatusFailed,
		VerifiedForSendingStatus: false,
	}, nil)

	now := time.Now().UTC()
	res, err := evaluateVerification(ctx, client, "example.com", now.Add(time.Hour), now)
	assert.NoError(t, err)
	assert.Equal(t, resource.OperationStatusFailure, res.OperationStatus)
	assert.Contains(t, res.Message, "FAILED")
}

func TestVerificationStatusEvaluator_PendingPastDeadline_ReturnsFailed(t *testing.T) {
	ctx := context.Background()
	client := &mockSesV2Client{}
	client.On("GetEmailIdentity", ctx, mock.Anything).Return(&sesv2.GetEmailIdentityOutput{
		IdentityType:             sesv2types.IdentityTypeDomain,
		VerificationStatus:       sesv2types.VerificationStatusPending,
		VerifiedForSendingStatus: false,
	}, nil)

	now := time.Date(2026, 5, 6, 13, 0, 0, 0, time.UTC)
	deadline := now.Add(-time.Minute) // already past

	res, err := evaluateVerification(ctx, client, "example.com", deadline, now)
	assert.NoError(t, err)
	assert.Equal(t, resource.OperationStatusFailure, res.OperationStatus)
	assert.Contains(t, res.Message, "timeout")
	assert.Contains(t, res.Message, "example.com")
}

func TestVerificationStatusEvaluator_NotFound_ReturnsFailed(t *testing.T) {
	ctx := context.Background()
	client := &mockSesV2Client{}
	nf := &sesv2types.NotFoundException{Message: aws.String("identity gone")}
	client.On("GetEmailIdentity", ctx, mock.Anything).Return(nil, nf)

	now := time.Now().UTC()
	res, err := evaluateVerification(ctx, client, "example.com", now.Add(time.Hour), now)
	assert.NoError(t, err)
	assert.Equal(t, resource.OperationStatusFailure, res.OperationStatus)
	assert.Contains(t, res.Message, "not found")
}

func TestVerificationStatusEvaluator_OtherError_Bubbles(t *testing.T) {
	ctx := context.Background()
	client := &mockSesV2Client{}
	client.On("GetEmailIdentity", ctx, mock.Anything).Return(nil, errors.New("network down"))

	now := time.Now().UTC()
	_, err := evaluateVerification(ctx, client, "example.com", now.Add(time.Hour), now)
	assert.Error(t, err)
}
