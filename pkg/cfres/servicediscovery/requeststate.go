// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package servicediscovery

import (
	"fmt"
	"strings"
	"time"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
)

// The phases a namespace's progress can be in. The unnamed default phase is the
// poll of the operation behind a create or an update.
const (
	// phaseDelete polls the operation behind a delete. It is told apart from the
	// default phase because a namespace that has just been deleted cannot be read
	// back, and because a delete the operation reports as blocked by the
	// resources still in the namespace is retried rather than failed.
	phaseDelete = "delete"
	// phaseRetryDelete re-issues a delete Cloud Map rejected because the
	// namespace still holds resources. That rejection carries no operation id, so
	// there is nothing to poll: re-issuing the delete is what the poll does.
	phaseRetryDelete = "retry-delete"
)

// requestState is what the RequestID carries from an asynchronous Cloud Map
// operation to the Status polls that follow it. Phase names what the polls are
// waiting on, and Deadline bounds the wait. OperationID is empty when there is
// no operation to poll, either because the namespace was adopted without one or
// because the phase does not poll an operation at all.
//
// The encoding is a `key=value;…` list, and decoding ignores keys it does not
// know, so keys can be added without invalidating a RequestID already in flight.
type requestState struct {
	Phase       string
	OperationID string
	Deadline    time.Time
}

func encodeRequestID(state requestState) string {
	fields := make([]string, 0, 3)
	if state.Phase != "" {
		fields = append(fields, "phase="+state.Phase)
	}
	if state.OperationID != "" {
		fields = append(fields, "op="+state.OperationID)
	}
	fields = append(fields, "deadline="+state.Deadline.UTC().Format(time.RFC3339))
	return strings.Join(fields, ";")
}

func decodeRequestID(requestID string) (requestState, error) {
	var state requestState
	var haveDeadline bool
	for _, field := range strings.Split(requestID, ";") {
		if field == "" {
			continue
		}
		key, value, ok := strings.Cut(field, "=")
		if !ok {
			return requestState{}, fmt.Errorf("servicediscovery: invalid RequestID %q", requestID)
		}
		switch key {
		case "phase":
			state.Phase = value
		case "op":
			state.OperationID = value
		case "deadline":
			deadline, err := time.Parse(time.RFC3339, value)
			if err != nil {
				return requestState{}, fmt.Errorf("servicediscovery: invalid deadline in RequestID %q: %w", requestID, err)
			}
			state.Deadline = deadline
			haveDeadline = true
		}
	}
	if !haveDeadline {
		return requestState{}, fmt.Errorf("servicediscovery: invalid RequestID %q: no deadline", requestID)
	}
	return state, nil
}

// statusResult reports progress for a status poll, carrying the request's own
// NativeID and RequestID back so the next poll resumes from the same state.
func statusResult(request *resource.StatusRequest, status resource.OperationStatus, message string) *resource.StatusResult {
	return statusResultInPhase(request, status, request.RequestID, message)
}

// statusResultInPhase reports progress under a RequestID other than the one the
// poll arrived with, moving the resource on to the phase that RequestID names.
func statusResultInPhase(
	request *resource.StatusRequest,
	status resource.OperationStatus,
	requestID string,
	message string,
) *resource.StatusResult {
	return &resource.StatusResult{
		ProgressResult: &resource.ProgressResult{
			OperationStatus: status,
			NativeID:        request.NativeID,
			RequestID:       requestID,
			StatusMessage:   message,
		},
	}
}
