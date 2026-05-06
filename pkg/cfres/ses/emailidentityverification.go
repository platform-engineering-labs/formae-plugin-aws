// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package ses

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	sesv2types "github.com/aws/aws-sdk-go-v2/service/sesv2/types"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres/prov"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres/registry"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/config"
)

// sesVerificationTimeout caps how long EmailIdentityVerification will poll
// before declaring failure. Build with -tags=conformance to use a 60s value
// suitable for CI conformance runs (Task 17 introduces that override).
const sesVerificationTimeout = 30 * time.Minute

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

// EmailIdentityVerification is a formae-internal synchronization gate. It
// owns no AWS resource — Create begins polling, Status/Read evaluate
// VerificationStatus, Update is rejected, Delete is a no-op.
type EmailIdentityVerification struct {
	cfg              *config.Config
	sesClientFactory func(cfg *config.Config) (SesV2ClientInterface, error)
	now              func() time.Time
	timeout          time.Duration
}

var _ prov.Provisioner = &EmailIdentityVerification{}

func init() {
	registry.Register("AWS::SES::EmailIdentityVerification",
		[]resource.Operation{
			resource.OperationRead,
			resource.OperationCreate,
			resource.OperationUpdate,
			resource.OperationCheckStatus,
			resource.OperationDelete,
		},
		func(cfg *config.Config) prov.Provisioner {
			return &EmailIdentityVerification{
				cfg:              cfg,
				sesClientFactory: defaultSesV2ClientFactory,
				now:              func() time.Time { return time.Now().UTC() },
				timeout:          sesVerificationTimeout,
			}
		})
}

// stateProperties is the JSON shape persisted in resource Properties for
// this gate. It mirrors the PKL schema fields plus the resolved Identity.
type stateProperties struct {
	Identity           string `json:"Identity"`
	VerificationStatus string `json:"VerificationStatus,omitempty"`
	DkimVerified       bool   `json:"DkimVerified,omitempty"`
	VerifiedAt         string `json:"VerifiedAt,omitempty"`
}

// encodeRequestID stores the polling deadline alongside the identity inside
// the RequestID returned by Create. StatusRequest carries no Properties
// field, so this is the only place we can stash deadline state.
func encodeRequestID(identity string, deadline time.Time) string {
	return identity + "|" + deadline.UTC().Format(time.RFC3339)
}

// decodeRequestID parses an encoded RequestID back into identity + deadline.
// If the RequestID is bare (no separator), it falls back to identity-only
// with a zero deadline (which evaluateVerification treats as expired for any
// non-success status).
func decodeRequestID(requestID string) (identity string, deadline time.Time, err error) {
	idx := strings.Index(requestID, "|")
	if idx < 0 {
		return requestID, time.Time{}, nil
	}
	identity = requestID[:idx]
	deadline, err = time.Parse(time.RFC3339, requestID[idx+1:])
	if err != nil {
		return "", time.Time{}, fmt.Errorf("EmailIdentityVerification: invalid deadline in RequestID: %w", err)
	}
	return identity, deadline, nil
}

func (e *EmailIdentityVerification) Create(ctx context.Context, request *resource.CreateRequest) (*resource.CreateResult, error) {
	var input stateProperties
	if err := json.Unmarshal(request.Properties, &input); err != nil {
		return nil, fmt.Errorf("EmailIdentityVerification: invalid Properties: %w", err)
	}
	if input.Identity == "" {
		return nil, errors.New("EmailIdentityVerification: identity required")
	}

	now := e.now()
	deadline := now.Add(e.timeout)
	state := stateProperties{Identity: input.Identity}
	stateJSON, err := json.Marshal(state)
	if err != nil {
		return nil, err
	}

	return &resource.CreateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationCreate,
			OperationStatus:    resource.OperationStatusInProgress,
			NativeID:           input.Identity,
			RequestID:          encodeRequestID(input.Identity, deadline),
			ResourceProperties: stateJSON,
		},
	}, nil
}

func (e *EmailIdentityVerification) Status(ctx context.Context, request *resource.StatusRequest) (*resource.StatusResult, error) {
	identity, deadline, err := decodeRequestID(request.RequestID)
	if err != nil {
		return nil, err
	}
	if identity == "" {
		identity = request.NativeID
	}
	if identity == "" {
		return nil, errors.New("EmailIdentityVerification: Status requires identity in RequestID or NativeID")
	}

	client, err := e.sesClientFactory(e.cfg)
	if err != nil {
		return nil, err
	}

	outcome, err := evaluateVerification(ctx, client, identity, deadline, e.now())
	if err != nil {
		slog.Error("EmailIdentityVerification: evaluateVerification failed", "error", err, "identity", identity)
		return nil, err
	}

	state := stateProperties{
		Identity:           identity,
		VerificationStatus: outcome.VerificationStatusString,
		DkimVerified:       outcome.DkimVerified,
		VerifiedAt:         outcome.VerifiedAt,
	}
	stateJSON, _ := json.Marshal(state)

	return &resource.StatusResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationCreate,
			OperationStatus:    outcome.OperationStatus,
			NativeID:           identity,
			RequestID:          request.RequestID,
			StatusMessage:      outcome.Message,
			ResourceProperties: stateJSON,
		},
	}, nil
}

func (e *EmailIdentityVerification) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	client, err := e.sesClientFactory(e.cfg)
	if err != nil {
		// Best-effort: return the persisted properties unchanged.
		slog.Warn("EmailIdentityVerification: SDK client unavailable; returning identity-only Properties", "error", err)
		state := stateProperties{Identity: request.NativeID}
		js, _ := json.Marshal(state)
		return &resource.ReadResult{ResourceType: request.ResourceType, Properties: string(js)}, nil
	}
	now := e.now()
	// On Read we don't enforce the polling deadline (the gate already passed,
	// or this is inventory sync). Pass a deadline far in the future so the
	// "in progress past deadline" branch never fires.
	deadline := now.Add(time.Hour)
	outcome, err := evaluateVerification(ctx, client, request.NativeID, deadline, now)
	if err != nil {
		slog.Warn("EmailIdentityVerification: GetEmailIdentity failed; returning identity-only Properties",
			"error", err, "identity", request.NativeID)
		state := stateProperties{Identity: request.NativeID}
		js, _ := json.Marshal(state)
		return &resource.ReadResult{ResourceType: request.ResourceType, Properties: string(js)}, nil
	}
	state := stateProperties{
		Identity:           request.NativeID,
		VerificationStatus: outcome.VerificationStatusString,
		DkimVerified:       outcome.DkimVerified,
		VerifiedAt:         outcome.VerifiedAt,
	}
	js, _ := json.Marshal(state)
	return &resource.ReadResult{ResourceType: request.ResourceType, Properties: string(js)}, nil
}

func (e *EmailIdentityVerification) Update(ctx context.Context, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	return nil, errors.New("EmailIdentityVerification: identity is createOnly; recreate the resource to gate a different identity")
}

func (e *EmailIdentityVerification) Delete(ctx context.Context, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	return &resource.DeleteResult{
		ProgressResult: &resource.ProgressResult{
			Operation:       resource.OperationDelete,
			OperationStatus: resource.OperationStatusSuccess,
			NativeID:        request.NativeID,
		},
	}, nil
}

func (e *EmailIdentityVerification) List(ctx context.Context, request *resource.ListRequest) (*resource.ListResult, error) {
	return nil, errors.New("list not supported for EmailIdentityVerification (discoverable=false)")
}
