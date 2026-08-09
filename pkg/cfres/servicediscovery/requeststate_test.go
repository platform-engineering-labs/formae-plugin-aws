// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package servicediscovery

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRequestIDRoundTrips(t *testing.T) {
	deadline := testNow.Add(namespaceOperationTimeout)
	for name, state := range map[string]requestState{
		"operation":                  {OperationID: "op-1", Deadline: deadline},
		"phase and operation":        {Phase: phaseDelete, OperationID: "op-del", Deadline: deadline},
		"phase without an operation": {Phase: phaseRetryDelete, Deadline: deadline},
	} {
		t.Run(name, func(t *testing.T) {
			decoded, err := decodeRequestID(encodeRequestID(state))
			require.NoError(t, err)
			assert.Equal(t, state, decoded)
		})
	}
}

// A RequestID minted before the phases existed is still in flight when the
// plugin carrying them takes over, and the operation it names is still the one
// to poll.
func TestDecodeRequestIDWithoutAPhase(t *testing.T) {
	state, err := decodeRequestID("op=op-1;deadline=" + testNow.Format(time.RFC3339))
	require.NoError(t, err)
	assert.Equal(t, requestState{OperationID: "op-1", Deadline: testNow}, state)
}

// Later work adds its own keys to the RequestID, so an unknown key must not make
// an otherwise valid RequestID undecodable.
func TestDecodeRequestIDIgnoresUnknownKeys(t *testing.T) {
	state, err := decodeRequestID("op=op-1;attempt=3;deadline=" + testNow.Format(time.RFC3339))
	require.NoError(t, err)
	assert.Equal(t, "op-1", state.OperationID)
	assert.Equal(t, testNow, state.Deadline)
}

func TestDecodeRequestIDRejectsARequestIDWithoutADeadline(t *testing.T) {
	_, err := decodeRequestID("phase=" + phaseRetryDelete)
	require.Error(t, err)
}
