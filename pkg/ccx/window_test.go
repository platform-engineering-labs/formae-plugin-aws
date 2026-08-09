// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package ccx

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fakeClock returns a Client.now func backed by a mutable time.Time the test
// controls directly, so window-tracker tests can move the clock without
// sleeping.
func fakeClock(start time.Time) (func() time.Time, func(time.Time)) {
	current := start
	return func() time.Time { return current }, func(t time.Time) { current = t }
}

func TestStampEnrichmentPending_FirstCallRecordsStamp_StableAcrossRepeats(t *testing.T) {
	clock, advance := fakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	c := &Client{now: clock}
	ctx := context.Background()

	first, ok := c.stampEnrichmentPending(ctx, "req-1")
	require.True(t, ok)
	require.Equal(t, clock(), first)

	advance(clock().Add(30 * time.Second))
	second, ok := c.stampEnrichmentPending(ctx, "req-1")
	require.True(t, ok)
	require.Equal(t, first, second, "stamp must not move on repeated calls")
}

func TestStampStatusError_FirstCallRecordsStamp_StableAcrossRepeats(t *testing.T) {
	clock, advance := fakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	c := &Client{now: clock}
	ctx := context.Background()

	first, ok := c.stampStatusError(ctx, "req-1")
	require.True(t, ok)
	require.Equal(t, clock(), first)

	advance(clock().Add(30 * time.Second))
	second, ok := c.stampStatusError(ctx, "req-1")
	require.True(t, ok)
	require.Equal(t, first, second, "stamp must not move on repeated calls")
}

func TestForgetRequest_RemovesBothStamps(t *testing.T) {
	clock, advance := fakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	c := &Client{now: clock}
	ctx := context.Background()

	_, ok := c.stampEnrichmentPending(ctx, "req-1")
	require.True(t, ok)
	_, ok = c.stampStatusError(ctx, "req-1")
	require.True(t, ok)

	c.forgetRequest("req-1")

	advance(clock().Add(time.Hour))
	firstAfterClear, ok := c.stampEnrichmentPending(ctx, "req-1")
	require.True(t, ok)
	require.Equal(t, clock(), firstAfterClear, "forgetRequest must remove the enrichment stamp entirely")

	secondAfterClear, ok := c.stampStatusError(ctx, "req-1")
	require.True(t, ok)
	require.Equal(t, clock(), secondAfterClear, "forgetRequest must remove the status-error stamp entirely")
}

func TestStamps_AreIndependentPerCondition(t *testing.T) {
	clock, advance := fakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	c := &Client{now: clock}
	ctx := context.Background()

	enrichmentFirst, ok := c.stampEnrichmentPending(ctx, "req-1")
	require.True(t, ok)

	advance(clock().Add(45 * time.Second))
	statusFirst, ok := c.stampStatusError(ctx, "req-1")
	require.True(t, ok)

	require.NotEqual(t, enrichmentFirst, statusFirst, "the two stamps must be recorded independently")

	// Repeated calls still read back their own condition's stamp unchanged.
	advance(clock().Add(45 * time.Second))
	enrichmentAgain, ok := c.stampEnrichmentPending(ctx, "req-1")
	require.True(t, ok)
	require.Equal(t, enrichmentFirst, enrichmentAgain)

	statusAgain, ok := c.stampStatusError(ctx, "req-1")
	require.True(t, ok)
	require.Equal(t, statusFirst, statusAgain)
}

func TestClearEnrichmentPending_LeavesStatusErrorStampIntact(t *testing.T) {
	clock, advance := fakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	c := &Client{now: clock}
	ctx := context.Background()

	_, ok := c.stampEnrichmentPending(ctx, "req-1")
	require.True(t, ok)

	advance(clock().Add(time.Minute))
	statusFirst, ok := c.stampStatusError(ctx, "req-1")
	require.True(t, ok)

	advance(clock().Add(time.Minute))
	c.clearEnrichmentPending("req-1")

	// The enrichment condition was cleared, so the next occurrence is a
	// fresh first -- it must read back at the current (later) clock, not
	// the original stamp.
	advance(clock().Add(time.Minute))
	newEnrichment, ok := c.stampEnrichmentPending(ctx, "req-1")
	require.True(t, ok)
	require.Equal(t, clock(), newEnrichment)
	require.NotEqual(t, statusFirst, newEnrichment)

	// The status-error stamp must be untouched by clearing the other
	// condition -- it must still read back at its own, earlier first-seen
	// time, not the clock at the moment of this call.
	statusAgain, ok := c.stampStatusError(ctx, "req-1")
	require.True(t, ok)
	require.Equal(t, statusFirst, statusAgain)
	require.NotEqual(t, clock(), statusAgain)
}

func TestClearStatusError_LeavesEnrichmentPendingStampIntact(t *testing.T) {
	clock, advance := fakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	c := &Client{now: clock}
	ctx := context.Background()

	enrichmentFirst, ok := c.stampEnrichmentPending(ctx, "req-1")
	require.True(t, ok)

	advance(clock().Add(time.Minute))
	_, ok = c.stampStatusError(ctx, "req-1")
	require.True(t, ok)

	advance(clock().Add(time.Minute))
	c.clearStatusError("req-1")

	// The status-error condition was cleared, so the next occurrence is a
	// fresh first -- it must read back at the current (later) clock.
	advance(clock().Add(time.Minute))
	newStatus, ok := c.stampStatusError(ctx, "req-1")
	require.True(t, ok)
	require.Equal(t, clock(), newStatus)
	require.NotEqual(t, enrichmentFirst, newStatus)

	// The enrichment-pending stamp must be untouched by clearing the other
	// condition -- it must still read back at its own, earlier first-seen
	// time, not the clock at the moment of this call.
	enrichmentAgain, ok := c.stampEnrichmentPending(ctx, "req-1")
	require.True(t, ok)
	require.Equal(t, enrichmentFirst, enrichmentAgain)
	require.NotEqual(t, clock(), enrichmentAgain)
}

func TestStamp_EmptyRequestID_NeverAdmitted(t *testing.T) {
	c := &Client{}
	ctx := context.Background()

	_, ok := c.stampEnrichmentPending(ctx, "")
	require.False(t, ok)

	_, ok = c.stampStatusError(ctx, "")
	require.False(t, ok)

	require.Empty(t, c.windows, "an empty RequestID must never create a map entry")
}

func TestStamp_AtCap_RefusesNewAdmission_ExistingWindowsKeepDeadlines(t *testing.T) {
	clock, advance := fakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	c := &Client{now: clock}
	ctx := context.Background()

	originalStamp, ok := c.stampEnrichmentPending(ctx, "req-0")
	require.True(t, ok)
	for i := 1; i < maxTrackedWindows; i++ {
		_, ok := c.stampEnrichmentPending(ctx, fmt.Sprintf("req-%d", i))
		require.True(t, ok, "admission must succeed under the cap")
	}
	require.Len(t, c.windows, maxTrackedWindows)

	// Move the clock forward before touching req-0 again, so a stamp that
	// got reset to "now" is distinguishable from one that kept its original
	// deadline.
	advance(clock().Add(time.Minute))

	existing, ok := c.stampEnrichmentPending(ctx, "req-0")
	require.True(t, ok, "an existing entry must keep being served even while the map is at capacity")
	require.Equal(t, originalStamp, existing, "an existing entry's deadline must not be reset by touching it while the map is at capacity")
	require.NotEqual(t, clock(), existing, "the deadline must still be the original stamp, not the current clock")

	_, ok = c.stampEnrichmentPending(ctx, "brand-new-request")
	require.False(t, ok, "a new RequestID must be refused once the tracker is at its admission cap")
	require.Len(t, c.windows, maxTrackedWindows, "a refused admission must not grow the map")

	retained, stillThere := c.windows["req-0"]
	require.True(t, stillThere, "existing windows must not be disturbed by a refused admission")
	require.Equal(t, originalStamp, retained.enrichmentPending, "the retained entry's deadline must survive a refused admission unchanged")
}

func TestActiveEntry_SurvivesSweep_DeadlineIntact(t *testing.T) {
	clock, advance := fakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	c := &Client{now: clock}
	ctx := context.Background()

	first, ok := c.stampEnrichmentPending(ctx, "req-1")
	require.True(t, ok)

	// Past the 2m enrichment window a later task would convert on, but well
	// under the 15m sweep retention TTL, and the entry is still being polled
	// (a stamp call keeps arriving), so a sweep pass must not touch it.
	advance(clock().Add(3 * time.Minute))
	_, ok = c.stampEnrichmentPending(ctx, "other-request")
	require.True(t, ok)

	stillFirst, ok := c.stampEnrichmentPending(ctx, "req-1")
	require.True(t, ok)
	require.Equal(t, first, stillFirst, "an active window's deadline must survive a sweep pass unchanged")
}

func TestStaleEntry_PastRetentionTTL_IsSwept(t *testing.T) {
	clock, advance := fakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	c := &Client{now: clock}
	ctx := context.Background()

	_, ok := c.stampEnrichmentPending(ctx, "req-1")
	require.True(t, ok)

	// req-1 receives no further activity at all -- the operator abandoned it.
	// Advance well past the retention TTL and drive a sweep pass via a call
	// for a different RequestID.
	advance(clock().Add(windowRetentionTTL + time.Minute))
	_, ok = c.stampEnrichmentPending(ctx, "unrelated-trigger")
	require.True(t, ok)

	afterSweep, ok := c.stampEnrichmentPending(ctx, "req-1")
	require.True(t, ok)
	require.Equal(t, clock(), afterSweep, "a stale entry must be evicted by the retention-TTL sweep, so the next call sees it as new")
}

func TestConcurrentAccess_IsRaceClean(t *testing.T) {
	c := &Client{}
	ctx := context.Background()

	var wg sync.WaitGroup
	for g := 0; g < 20; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				id := fmt.Sprintf("req-%d-%d", g, i%5)
				c.stampEnrichmentPending(ctx, id)
				c.stampStatusError(ctx, id)
				c.clearEnrichmentPending(id)
				c.clearStatusError(id)
				c.forgetRequest(id)
			}
		}(g)
	}
	wg.Wait()
}

func TestClock_FallsBackToTimeNow_OnZeroValueClient(t *testing.T) {
	c := &Client{}
	before := time.Now()
	got := c.clock()
	after := time.Now()

	require.False(t, got.Before(before))
	require.False(t, got.After(after))
}
