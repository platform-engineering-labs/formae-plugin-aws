// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package ccx

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/platform-engineering-labs/formae/pkg/plugin"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
)

// Recoverable-error retry budget for ccx-layer calls that don't benefit from
// the PluginOperator's higher-level retry loop. The two call sites today are:
//
//   - StatusResource / populateResourceProperties post-success Reads. When
//     CloudControl reports a CRUD operation as Success, we Read to enrich the
//     result. The PluginOperator never sees the Read's ErrorCode because the
//     CRUD status already returned Success; without retrying here the agent
//     persists a stale snapshot.
//
//   - Discovery's ListResources. Discovery has no PluginOperator wrapping
//     so a transient AWS throttle or HandlerFailureException returns directly
//     to the scan loop, which simply drops the resource type for the tick.
//     The next scan only runs on the periodic schedule, so the
//     conformance-test wait window typically times out before the next
//     attempt.
//
// Both surfaces need an in-process exponential-backoff loop with a budget
// long enough to absorb AWS's typical 30-60s recovery window.
//
// The budget is sized for sustained CloudControl throttling during the
// conformance matrix, where many resource types poll status concurrently:
// 10 attempts over the 1s..30s backoff give roughly a 3-minute window, enough
// for status reads to ride out the throttling bursts that previously exhausted
// a 6-attempt budget and failed otherwise-healthy applies.
const (
	defaultRetryMaxAttempts = 10
	defaultRetryBaseDelay   = 1 * time.Second
	defaultRetryMaxDelay    = 30 * time.Second
)

// retryOpts allows callers to override the default attempt limits for tests or
// for call sites with different tolerance for latency.
type retryOpts struct {
	MaxAttempts int
	BaseDelay   time.Duration
	MaxDelay    time.Duration

	// Budget bounds the whole loop by wall clock instead of by attempt count,
	// counting the time spent inside each attempt as well as the backoff
	// sleeps. It exists for calls made inside a plugin RPC that the agent's
	// missing-in-action watchdog observes: while such an RPC is blocked the
	// operator cannot iterate, so no progress is reported and a healthy
	// operation is killed. Call sites that must return well inside that window
	// set a Budget; leaving it zero keeps the attempt-bounded behaviour for
	// unwatched call sites such as discovery.
	//
	// Because context deadlines are cooperative, the budget is approximate:
	// it bounds when the loop stops asking for more work, not the instant an
	// in-flight call unwinds.
	Budget time.Duration
}

// errRetryBudgetExhausted marks a retry loop that stopped because its
// wall-clock Budget ran out, rather than because the operation reached a
// terminal outcome or ran out of attempts. Callers match it with errors.Is to
// tell "still in progress, ask again later" apart from a real failure; the
// last observed error is wrapped alongside it so the cause survives.
var errRetryBudgetExhausted = errors.New("retry budget exhausted")

// budgetExhaustedError builds the error a budget exit returns.
func budgetExhaustedError(lastErr error) error {
	if lastErr == nil {
		return errRetryBudgetExhausted
	}
	return fmt.Errorf("%w: %w", errRetryBudgetExhausted, lastErr)
}

// withRetryBudget derives the context each attempt runs under. The deadline is
// applied to the attempts themselves, not just to the sleeps between them: an
// attempt is an AWS SDK call with its own internal retries and unbounded
// service latency, so bounding only the sleeps under-counts wall clock.
//
// The returned deadline is zero when unbudgeted, which disables every budget
// check in the retry loops and leaves their behaviour exactly as it was.
func withRetryBudget(ctx context.Context, budget time.Duration) (context.Context, time.Time, context.CancelFunc) {
	if budget <= 0 {
		return ctx, time.Time{}, func() {}
	}
	deadline := time.Now().Add(budget)
	callCtx, cancel := context.WithDeadline(ctx, deadline)
	return callCtx, deadline, cancel
}

// budgetSpent reports whether the wall-clock budget has no room left for
// `lookahead` more waiting — and that it is our budget, rather than the
// caller's own cancellation or deadline, that ran out. A lookahead of zero asks
// whether the budget has already elapsed; a lookahead of one backoff delay asks
// whether sleeping for it would cross the deadline.
//
// Exhaustion is decided from the budget's own clock rather than from the last
// observed error on purpose: when the deadline fires mid-call the in-flight AWS
// call returns context.DeadlineExceeded, which isRecoverable deliberately
// rejects, so an "was the last failure recoverable?" test would miss the most
// important case — a first attempt that blocked for the entire budget.
//
// Parent cancellation and a parent deadline propagate to the caller unchanged
// and must never be reported as budget exhaustion.
func budgetSpent(ctx context.Context, deadline time.Time, lookahead time.Duration) bool {
	if deadline.IsZero() || ctx.Err() != nil {
		return false
	}
	return !time.Now().Add(lookahead).Before(deadline)
}

func (o retryOpts) withDefaults() retryOpts {
	if o.MaxAttempts <= 0 {
		o.MaxAttempts = defaultRetryMaxAttempts
	}
	if o.BaseDelay <= 0 {
		o.BaseDelay = defaultRetryBaseDelay
	}
	if o.MaxDelay <= 0 {
		o.MaxDelay = defaultRetryMaxDelay
	}
	return o
}

// retryRead repeats `fn` with exponential-backoff-plus-jitter while the
// returned ReadResult carries a recoverable CCAPI ErrorCode (or `fn`
// returns a transient Go error). It exits on success (Properties
// populated, ErrorCode empty), on non-recoverable failure, on context
// cancellation, once the attempt budget is exhausted, or — when
// opts.Budget is set — once the wall-clock budget is spent, in which case
// the error wraps errRetryBudgetExhausted. The last observed result is
// returned in all exit paths so the caller can inspect it.
func retryRead(
	ctx context.Context,
	opts retryOpts,
	logHint string,
	fn func(context.Context) (*resource.ReadResult, error),
) (*resource.ReadResult, error) {
	opts = opts.withDefaults()

	callCtx, deadline, cancel := withRetryBudget(ctx, opts.Budget)
	defer cancel()

	var last *resource.ReadResult
	var lastErr error
	budgetExit := false
	for attempt := 1; attempt <= opts.MaxAttempts; attempt++ {
		res, err := fn(callCtx)
		last = res
		lastErr = err

		if err == nil && res != nil && res.ErrorCode == "" && res.Properties != "" {
			return res, nil
		}

		// The budget is consulted before the failure is classified, so that an
		// attempt which consumed the whole window is reported as exhaustion
		// rather than as the non-recoverable context error it surfaces as.
		if budgetSpent(ctx, deadline, 0) {
			budgetExit = true
			break
		}

		switch {
		case err != nil && !isRecoverable(err, ""):
			return res, err
		case err == nil && res != nil && res.ErrorCode != "" && !isRecoverable(nil, string(res.ErrorCode)):
			// Non-recoverable CCAPI error code (e.g. NotFound) — surface
			// without further retries.
			return res, nil
		}

		if attempt == opts.MaxAttempts {
			break
		}

		delay := backoffDelay(attempt, opts.BaseDelay, opts.MaxDelay)

		// Declining to sleep past the deadline is a budget exit too: there is
		// time left on the clock, just not enough to wait out the next backoff
		// and still make another attempt. It has to raise the sentinel like any
		// other budget exit — otherwise a run of fast failures would fall
		// through to the ordinary return and read as a sparse success.
		if budgetSpent(ctx, deadline, delay) {
			budgetExit = true
			break
		}

		errCode := ""
		if res != nil {
			errCode = string(res.ErrorCode)
		}
		plugin.LoggerFromContext(ctx).Info("ccx: retrying read on recoverable error",
			"hint", logHint,
			"attempt", attempt,
			"maxAttempts", opts.MaxAttempts,
			"delay", delay,
			"err", err,
			"errorCode", errCode)

		select {
		case <-callCtx.Done():
			if err := ctx.Err(); err != nil {
				return last, err
			}
			budgetExit = true
		case <-time.After(delay):
		}
		if budgetExit {
			break
		}
	}

	if budgetExit {
		plugin.LoggerFromContext(ctx).Info("ccx: retry budget exhausted on read",
			"hint", logHint,
			"budget", opts.Budget,
			"err", lastErr)
		return last, budgetExhaustedError(lastErr)
	}
	if lastErr != nil {
		return last, lastErr
	}
	return last, nil
}

// retryCallable repeats `fn` with exponential-backoff-plus-jitter while
// the returned error matches AWS's recoverable surface (Throttling,
// HandlerFailure, internal service errors). It exits on success, on
// non-recoverable error, on context cancellation, once the attempt budget
// is exhausted, or — when opts.Budget is set — once the wall-clock budget
// is spent, in which case the error wraps errRetryBudgetExhausted. The
// last observed result is returned.
func retryCallable[T any](
	ctx context.Context,
	opts retryOpts,
	logHint string,
	fn func(context.Context) (T, error),
) (T, error) {
	opts = opts.withDefaults()

	callCtx, deadline, cancel := withRetryBudget(ctx, opts.Budget)
	defer cancel()

	var last T
	var lastErr error
	budgetExit := false
	for attempt := 1; attempt <= opts.MaxAttempts; attempt++ {
		v, err := fn(callCtx)
		last = v
		lastErr = err
		if err == nil {
			return v, nil
		}
		// Consulted before the error is classified: see retryRead.
		if budgetSpent(ctx, deadline, 0) {
			budgetExit = true
			break
		}
		if !isRecoverable(err, "") {
			return v, err
		}
		if attempt == opts.MaxAttempts {
			break
		}
		delay := backoffDelay(attempt, opts.BaseDelay, opts.MaxDelay)
		if budgetSpent(ctx, deadline, delay) {
			budgetExit = true
			break
		}
		plugin.LoggerFromContext(ctx).Info("ccx: retrying call on recoverable error",
			"hint", logHint,
			"attempt", attempt,
			"maxAttempts", opts.MaxAttempts,
			"delay", delay,
			"err", err)
		select {
		case <-callCtx.Done():
			if cerr := ctx.Err(); cerr != nil {
				return last, cerr
			}
			budgetExit = true
		case <-time.After(delay):
		}
		if budgetExit {
			break
		}
	}

	if budgetExit {
		plugin.LoggerFromContext(ctx).Info("ccx: retry budget exhausted on call",
			"hint", logHint,
			"budget", opts.Budget,
			"err", lastErr)
		return last, budgetExhaustedError(lastErr)
	}
	return last, lastErr
}

// isRecoverable returns true when either the supplied Go error or the
// supplied CCAPI ErrorCode string indicates a transient condition that
// should be retried at the ccx layer.
//
// The SDK's built-in Retryer already handles many of these for in-flight
// requests; this layer catches the cases where the SDK exhausts its own
// budget and surfaces the wrapped error, plus the post-Read ErrorCode
// path (which never went through the SDK Retryer because the Read
// itself succeeded — CCAPI just returned a typed error inside the
// response).
func isRecoverable(err error, errorCode string) bool {
	if errorCode != "" {
		switch resource.OperationErrorCode(errorCode) {
		case resource.OperationErrorCodeThrottling,
			resource.OperationErrorCodeNotStabilized,
			resource.OperationErrorCodeServiceInternalError,
			resource.OperationErrorCodeServiceTimeout,
			resource.OperationErrorCodeNetworkFailure,
			resource.OperationErrorCodeInternalFailure,
			resource.OperationErrorCodeGeneralServiceException:
			return true
		}
	}
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	// String-match the wrapped SDK error surface. Once the SDK's standard
	// retryer exhausts, the error is wrapped multiple times and the
	// underlying CCAPI typed error isn't preserved as an Is/As target.
	msg := err.Error()
	for _, marker := range []string{
		"ThrottlingException",
		"Throttling",
		"HandlerFailureException",
		"HandlerInternalFailureException",
		"InternalFailure",
		"InternalServerError",
		"ServiceUnavailable",
		"GeneralServiceException",
		"RequestTimeout",
		"exceeded maximum number of attempts",
	} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

// backoffDelay returns base * 2^(attempt-1) capped at maxDelay, with up
// to 25% jitter added to avoid thundering-herd retries from concurrent
// matrix jobs all hitting the same recovery window.
func backoffDelay(attempt int, base, maxDelay time.Duration) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	shift := attempt - 1
	if shift > 30 {
		shift = 30
	}
	delay := base * (1 << shift)
	if delay > maxDelay || delay <= 0 {
		delay = maxDelay
	}
	jitter := time.Duration(rand.Int64N(int64(delay) / 4))
	return delay + jitter
}
