// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build integration

// Tests for the polling helper the integration test's waits are built on. They
// touch no cloud resource, but live under the same build tag as the helper.

package servicediscovery

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWaitFor_RetriesATransientProbeError(t *testing.T) {
	calls := 0
	err := waitFor(context.Background(), time.Second, time.Millisecond,
		func(context.Context) (bool, error) {
			calls++
			if calls == 1 {
				return false, errors.New("throttled")
			}
			return true, nil
		})

	require.NoError(t, err)
	assert.Equal(t, 2, calls, "the probe should be called again after a transient error")
}

func TestWaitFor_ReportsAPersistentProbeErrorOnceTheCeilingRunsOut(t *testing.T) {
	err := waitFor(context.Background(), 10*time.Millisecond, time.Millisecond,
		func(context.Context) (bool, error) {
			return false, errors.New("access denied")
		})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "timed out")
	assert.Contains(t, err.Error(), "access denied")
}

func TestWaitFor_ReportsATerminalProbeErrorWithoutRetrying(t *testing.T) {
	calls := 0
	err := waitFor(context.Background(), time.Minute, time.Millisecond,
		func(context.Context) (bool, error) {
			calls++
			return false, &terminalError{err: errors.New("operation failed")}
		})

	require.Error(t, err)
	assert.Equal(t, "operation failed", err.Error())
	assert.Equal(t, 1, calls, "a terminal error should not be retried")
}

func TestWaitFor_StopsWhenTheContextIsDone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := waitFor(ctx, time.Minute, time.Millisecond,
		func(context.Context) (bool, error) { return false, nil })

	require.ErrorIs(t, err, context.Canceled)
}
