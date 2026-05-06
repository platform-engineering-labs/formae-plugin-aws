// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package ses

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	sesv2types "github.com/aws/aws-sdk-go-v2/service/sesv2/types"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
)

// sesVerificationTimeout caps how long EmailIdentityVerification will poll
// before declaring failure. Build with -tags=conformance to use a 60s value
// suitable for CI conformance runs (Task 17 introduces that override).
//
//nolint:unused // wired in Task 15 (Provisioner glue)
const sesVerificationTimeout = 30 * time.Minute

//nolint:unused // wired in Task 15 (Provisioner glue)
type verificationOutcome struct {
	OperationStatus          resource.OperationStatus
	Message                  string
	VerificationStatusString string
	DkimVerified             bool
	VerifiedAt               string // ISO-8601 when SUCCESS
}

// evaluateVerification is the pure decision function under test. Given the
// SES identity's name plus the polling deadline, it returns the outcome
// (InProgress / Success / Failure) without any side effects beyond the SES
// API call. The Provisioner glue (Task 15) wires this together with deadline
// persistence.
//
//nolint:unused // wired in Task 15 (Provisioner glue)
func evaluateVerification(
	ctx context.Context,
	client SesV2ClientInterface,
	emailIdentity string,
	deadline time.Time,
	now time.Time,
) (verificationOutcome, error) {
	resp, err := client.GetEmailIdentity(ctx, &sesv2.GetEmailIdentityInput{
		EmailIdentity: &emailIdentity,
	})
	if err != nil {
		var nf *sesv2types.NotFoundException
		if errors.As(err, &nf) {
			return verificationOutcome{
				OperationStatus: resource.OperationStatusFailure,
				Message:         fmt.Sprintf("EmailIdentity %q not found; the underlying identity was destroyed mid-poll", emailIdentity),
			}, nil
		}
		return verificationOutcome{}, err
	}

	switch resp.VerificationStatus {
	case sesv2types.VerificationStatusSuccess:
		return verificationOutcome{
			OperationStatus:          resource.OperationStatusSuccess,
			VerificationStatusString: "SUCCESS",
			DkimVerified:             resp.VerifiedForSendingStatus,
			VerifiedAt:               now.UTC().Format(time.RFC3339),
		}, nil
	case sesv2types.VerificationStatusFailed:
		return verificationOutcome{
			OperationStatus: resource.OperationStatusFailure,
			Message:         fmt.Sprintf("SES reported VerificationStatus=FAILED for identity %q", emailIdentity),
		}, nil
	case sesv2types.VerificationStatusPending,
		sesv2types.VerificationStatusTemporaryFailure,
		sesv2types.VerificationStatusNotStarted:
		if now.After(deadline) {
			return verificationOutcome{
				OperationStatus: resource.OperationStatusFailure,
				Message: fmt.Sprintf(
					"timeout waiting for SES VerificationStatus=SUCCESS on identity %q (deadline %s); re-apply once DNS or human action completes",
					emailIdentity, deadline.UTC().Format(time.RFC3339)),
			}, nil
		}
		return verificationOutcome{
			OperationStatus:          resource.OperationStatusInProgress,
			VerificationStatusString: string(resp.VerificationStatus),
			DkimVerified:             resp.VerifiedForSendingStatus,
		}, nil
	default:
		return verificationOutcome{
			OperationStatus: resource.OperationStatusFailure,
			Message:         fmt.Sprintf("unknown VerificationStatus %q for identity %q", resp.VerificationStatus, emailIdentity),
		}, nil
	}
}
