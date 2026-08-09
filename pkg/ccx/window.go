// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package ccx

import (
	"context"
	"time"

	"github.com/platform-engineering-labs/formae/pkg/plugin"
)

// windowRetentionTTL bounds how long an idle request-window entry may sit in
// the tracker before the sweep reclaims it. It exists only to bound entries
// that were genuinely abandoned -- an operator process that stopped polling
// an operation entirely (crashed, or moved on) without ever reaching a
// terminal state that would have called forgetRequest. It is an order of
// magnitude above the ~2m window later stages bound their own conditions by,
// specifically so it never fires while an operation is still being actively
// polled: every stamp/clear call on an entry refreshes its lastSeen.
const windowRetentionTTL = 15 * time.Minute

// maxTrackedWindows caps the tracker's admission, not its eviction. Real
// in-flight cardinality for a single plugin process is in the hundreds; 10k
// is generous headroom while still bounding worst-case memory. At the cap,
// new RequestIDs are refused admission and the caller falls back to today's
// (unbounded) behaviour -- existing entries are never touched to make room.
// Evicting the oldest entry would evict precisely the entries closest to
// their own deadline, quietly unbounding the operation the tracker exists to
// bound.
const maxTrackedWindows = 10_000

// window holds the process-local stamps for a single CloudControl request,
// keyed by RequestID (the request token CloudControl returns from Create/
// Update and echoes back on every StatusResource poll).
//
// Both stamps bound a *consecutive* observation outage for the one plugin
// process polling the request, not the operation's total wall-clock time:
//
//   - enrichmentPending is the first poll at which CloudControl reported the
//     underlying CRUD as Success but the post-success Read to enrich the
//     result hadn't returned properties yet. It backs up
//     ProgressEvent.EventTime, which is the primary, restart-surviving
//     clock; this stamp only shortens that window, guarding against an
//     EventTime that (against expectation) creeps forward on every poll and
//     would otherwise keep the window perpetually young.
//   - statusError is the first poll of a run of consecutive
//     GetResourceRequestStatus failures, where there is no ProgressEvent and
//     therefore no EventTime to bound the wait against at all.
//
// A plugin-process restart restarts both windows from zero: nothing here
// survives past the process, and the tracker assumes one plugin process is
// the one polling a given request to completion.
type window struct {
	enrichmentPending time.Time
	statusError       time.Time
	lastSeen          time.Time
}

// stampEnrichmentPending records the first poll at which enrichment for
// requestID was observed pending, and returns that first timestamp unchanged
// on every later call for the same requestID -- callers compare it against
// the current time to decide whether the enrichment-pending window has
// elapsed. ok is false when requestID is empty or the tracker is at its
// admission cap; callers must fall back to today's unbounded behaviour
// rather than converting when ok is false.
func (c *Client) stampEnrichmentPending(ctx context.Context, requestID string) (time.Time, bool) {
	return c.stamp(ctx, requestID, func(w *window) *time.Time { return &w.enrichmentPending })
}

// stampStatusError records the first poll of a run of consecutive
// GetResourceRequestStatus failures for requestID. See stampEnrichmentPending
// for the shared admission and stability rules. Call clearStatusError on any
// successful status call to restart the window on the next failure.
func (c *Client) stampStatusError(ctx context.Context, requestID string) (time.Time, bool) {
	return c.stamp(ctx, requestID, func(w *window) *time.Time { return &w.statusError })
}

// clearEnrichmentPending clears requestID's enrichment stamp once enrichment
// is no longer pending -- the post-success Read finally returned properties,
// or the enrichment window expired and the caller fell back to Success. It
// leaves any status-error stamp for the same requestID untouched.
func (c *Client) clearEnrichmentPending(requestID string) {
	c.clearField(requestID, func(w *window) *time.Time { return &w.enrichmentPending })
}

// clearStatusError clears requestID's status-error stamp on any successful
// GetResourceRequestStatus call, so a later failure starts a fresh window
// rather than resuming one from a prior, unrelated failure streak. It leaves
// any enrichment-pending stamp for the same requestID untouched.
func (c *Client) clearStatusError(requestID string) {
	c.clearField(requestID, func(w *window) *time.Time { return &w.statusError })
}

// forgetRequest removes requestID's entire entry -- both stamps -- on the
// request's terminal return (Success, window-expired Success, or terminal
// Failure). Once a RequestID resolves there is nothing left for either stamp
// to bound. This is deliberately named apart from clearEnrichmentPending and
// clearStatusError: calling this on anything short of a terminal return would
// wipe out the other condition's stamp too, silently undoing the guarantee
// those two exist to keep independent (see the enrichment-pending backstop
// note on the window type above).
func (c *Client) forgetRequest(requestID string) {
	if requestID == "" {
		return
	}
	c.windowsMu.Lock()
	defer c.windowsMu.Unlock()
	delete(c.windows, requestID)
}

// stamp is the shared admission path for both stamps: sweep first, then
// either read back an existing entry's stamp unchanged or admit a new entry
// (subject to the cap). field selects which of the window's two stamps this
// call is stamping.
func (c *Client) stamp(ctx context.Context, requestID string, field func(*window) *time.Time) (time.Time, bool) {
	if requestID == "" {
		// Callers must not convert without a RequestID: an empty key would
		// let unrelated requests collide into a single shared window.
		return time.Time{}, false
	}

	now := c.clock()

	c.windowsMu.Lock()
	c.sweepLocked(now)

	w, exists := c.windows[requestID]
	if !exists && len(c.windows) >= maxTrackedWindows {
		c.windowsMu.Unlock()
		// Logged outside the lock: the context logger writes to stdout,
		// which can block on a full pipe, and every other in-flight tracker
		// call would stall behind windowsMu while it did.
		plugin.LoggerFromContext(ctx).Error("ccx: request-window tracker at admission cap, refusing new window",
			"requestID", requestID,
			"cap", maxTrackedWindows)
		return time.Time{}, false
	}

	stamp := field(&w)
	if stamp.IsZero() {
		*stamp = now
	}
	w.lastSeen = now

	if c.windows == nil {
		c.windows = make(map[string]window)
	}
	c.windows[requestID] = w
	c.windowsMu.Unlock()

	return *stamp, true
}

// clearField zeroes a single stamp on an existing entry, leaving the other
// stamp and the entry itself intact. Clearing on an entry that doesn't exist,
// or an empty requestID, is a no-op -- there's nothing to clear.
func (c *Client) clearField(requestID string, field func(*window) *time.Time) {
	if requestID == "" {
		return
	}

	c.windowsMu.Lock()
	defer c.windowsMu.Unlock()

	w, exists := c.windows[requestID]
	if !exists {
		return
	}

	*field(&w) = time.Time{}
	w.lastSeen = c.clock()
	c.windows[requestID] = w
}

// sweepLocked evicts entries that have had no stamp or clear activity for
// windowRetentionTTL. Callers must hold windowsMu. It runs opportunistically
// at the start of every admission attempt rather than on a background timer,
// since the tracker has no lifecycle of its own beyond the calls its
// consumers already make.
//
// It only ever removes entries that were silently abandoned: an entry still
// being actively polled has its lastSeen refreshed on every stamp or clear
// call and so never becomes eligible, regardless of how far past its own
// (much shorter) 2m window it is. Reaching that shorter window is the later
// tasks' job to detect and act on by calling forgetRequest -- this sweep is
// strictly a backstop for a RequestID whose operator disappeared before
// doing so.
func (c *Client) sweepLocked(now time.Time) {
	for id, w := range c.windows {
		if now.Sub(w.lastSeen) >= windowRetentionTTL {
			delete(c.windows, id)
		}
	}
}

// clock returns the current time via the injected now func, falling back to
// time.Now when the Client is its zero value -- existing unit tests build
// &Client{api: mockAPI} directly, without going through NewClient.
func (c *Client) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}
