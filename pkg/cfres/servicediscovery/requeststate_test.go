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
	state, err := decodeRequestID(encodeRequestID(requestState{OperationID: "op-1", Deadline: deadline}))
	require.NoError(t, err)
	assert.Equal(t, "op-1", state.OperationID)
	assert.Equal(t, deadline, state.Deadline)
}

// Later phases add their own keys to the RequestID, so an unknown key must not
// make an otherwise valid RequestID undecodable.
func TestDecodeRequestIDIgnoresUnknownKeys(t *testing.T) {
	state, err := decodeRequestID("op=op-1;phase=delete;deadline=" + testNow.Format(time.RFC3339))
	require.NoError(t, err)
	assert.Equal(t, "op-1", state.OperationID)
	assert.Equal(t, testNow, state.Deadline)
}
