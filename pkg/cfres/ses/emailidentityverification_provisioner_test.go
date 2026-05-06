// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package ses

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	sesv2types "github.com/aws/aws-sdk-go-v2/service/sesv2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/config"
)

func newTestProvisioner(t *testing.T, client SesV2ClientInterface, fixedNow time.Time, timeout time.Duration) *EmailIdentityVerification {
	t.Helper()
	return &EmailIdentityVerification{
		cfg:              &config.Config{},
		sesClientFactory: func(_ *config.Config) (SesV2ClientInterface, error) { return client, nil },
		now:              func() time.Time { return fixedNow },
		timeout:          timeout,
	}
}

func TestEmailIdentityVerification_Create_PersistsDeadline(t *testing.T) {
	ctx := context.Background()
	client := &mockSesV2Client{}
	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	prov := newTestProvisioner(t, client, now, 30*time.Minute)

	props, _ := json.Marshal(map[string]any{"Identity": "example.com"})
	res, err := prov.Create(ctx, &resource.CreateRequest{Properties: props})
	require.NoError(t, err)
	require.NotNil(t, res.ProgressResult)
	assert.Equal(t, resource.OperationStatusInProgress, res.ProgressResult.OperationStatus)
	assert.Equal(t, "example.com", res.ProgressResult.NativeID)

	// RequestID encodes the deadline; decode round-trips.
	identity, deadline, err := decodeRequestID(res.ProgressResult.RequestID)
	require.NoError(t, err)
	assert.Equal(t, "example.com", identity)
	assert.Equal(t, now.Add(30*time.Minute).UTC(), deadline.UTC())

	var state stateProperties
	require.NoError(t, json.Unmarshal(res.ProgressResult.ResourceProperties, &state))
	assert.Equal(t, "example.com", state.Identity)
}

func TestEmailIdentityVerification_Create_RejectsMissingIdentity(t *testing.T) {
	ctx := context.Background()
	prov := newTestProvisioner(t, &mockSesV2Client{}, time.Now(), time.Hour)
	props, _ := json.Marshal(map[string]any{})
	_, err := prov.Create(ctx, &resource.CreateRequest{Properties: props})
	assert.Error(t, err)
}

func TestEmailIdentityVerification_Status_TransitionsToSuccess(t *testing.T) {
	ctx := context.Background()
	client := &mockSesV2Client{}
	client.On("GetEmailIdentity", ctx, mock.Anything).Return(&sesv2.GetEmailIdentityOutput{
		IdentityType:             sesv2types.IdentityTypeDomain,
		VerificationStatus:       sesv2types.VerificationStatusSuccess,
		VerifiedForSendingStatus: true,
	}, nil)
	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	prov := newTestProvisioner(t, client, now, time.Hour)

	requestID := encodeRequestID("example.com", now.Add(15*time.Minute))
	res, err := prov.Status(ctx, &resource.StatusRequest{
		RequestID: requestID,
		NativeID:  "example.com",
	})
	require.NoError(t, err)
	require.NotNil(t, res.ProgressResult)
	assert.Equal(t, resource.OperationStatusSuccess, res.ProgressResult.OperationStatus)
	assert.Equal(t, "example.com", res.ProgressResult.NativeID)

	var got stateProperties
	require.NoError(t, json.Unmarshal(res.ProgressResult.ResourceProperties, &got))
	assert.Equal(t, "SUCCESS", got.VerificationStatus)
	assert.True(t, got.DkimVerified)
	assert.NotEmpty(t, got.VerifiedAt)
}

func TestEmailIdentityVerification_Update_Rejected(t *testing.T) {
	prov := newTestProvisioner(t, &mockSesV2Client{}, time.Now(), time.Hour)
	_, err := prov.Update(context.Background(), &resource.UpdateRequest{})
	assert.Error(t, err)
}

func TestEmailIdentityVerification_Delete_NoOpSuccess(t *testing.T) {
	prov := newTestProvisioner(t, &mockSesV2Client{}, time.Now(), time.Hour)
	res, err := prov.Delete(context.Background(), &resource.DeleteRequest{NativeID: "example.com"})
	require.NoError(t, err)
	require.NotNil(t, res.ProgressResult)
	assert.Equal(t, resource.OperationStatusSuccess, res.ProgressResult.OperationStatus)
	assert.Equal(t, "example.com", res.ProgressResult.NativeID)
}
