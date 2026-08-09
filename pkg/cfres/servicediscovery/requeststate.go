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

// requestState is what the RequestID carries from an asynchronous Cloud Map
// operation to the Status polls that follow it. OperationID is empty when the
// namespace was resolved without an operation to poll; Status then confirms the
// namespace itself instead of an operation.
//
// The encoding is a `key=value;…` list, and decoding ignores keys it does not
// know, so keys can be added without invalidating a RequestID already in flight.
type requestState struct {
	OperationID string
	Deadline    time.Time
}

func encodeRequestID(state requestState) string {
	return fmt.Sprintf("op=%s;deadline=%s", state.OperationID, state.Deadline.UTC().Format(time.RFC3339))
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
	return &resource.StatusResult{
		ProgressResult: &resource.ProgressResult{
			OperationStatus: status,
			NativeID:        request.NativeID,
			RequestID:       request.RequestID,
			StatusMessage:   message,
		},
	}
}
