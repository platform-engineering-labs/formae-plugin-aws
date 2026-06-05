// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package ccx

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"strings"
	"time"

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
const (
	defaultRetryMaxAttempts = 6
	defaultRetryBaseDelay   = 1 * time.Second
	defaultRetryMaxDelay    = 30 * time.Second
)

// retryOpts allows callers to override the default budget for tests or for
// call sites with different tolerance for latency.
type retryOpts struct {
	MaxAttempts int
	BaseDelay   time.Duration
	MaxDelay    time.Duration
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
// cancellation, or once the attempt budget is exhausted. The last
// observed result is returned in all exit paths so the caller can
// inspect it.
func retryRead(
	ctx context.Context,
	opts retryOpts,
	logHint string,
	fn func(context.Context) (*resource.ReadResult, error),
) (*resource.ReadResult, error) {
	opts = opts.withDefaults()

	var last *resource.ReadResult
	var lastErr error
	for attempt := 1; attempt <= opts.MaxAttempts; attempt++ {
		res, err := fn(ctx)
		last = res
		lastErr = err

		switch {
		case err != nil && !isRecoverable(err, ""):
			return res, err
		case err == nil && res != nil && res.ErrorCode == "" && res.Properties != "":
			return res, nil
		case err == nil && res != nil && res.ErrorCode != "" && !isRecoverable(nil, string(res.ErrorCode)):
			// Non-recoverable CCAPI error code (e.g. NotFound) — surface
			// without further retries.
			return res, nil
		}

		if attempt == opts.MaxAttempts {
			break
		}

		delay := backoffDelay(attempt, opts.BaseDelay, opts.MaxDelay)
		errCode := ""
		if res != nil {
			errCode = string(res.ErrorCode)
		}
		slog.Info("ccx: retrying read on recoverable error",
			"hint", logHint,
			"attempt", attempt,
			"maxAttempts", opts.MaxAttempts,
			"delay", delay,
			"err", err,
			"errorCode", errCode)

		select {
		case <-ctx.Done():
			return last, ctx.Err()
		case <-time.After(delay):
		}
	}

	if lastErr != nil {
		return last, lastErr
	}
	return last, nil
}

// retryCallable repeats `fn` with exponential-backoff-plus-jitter while
// the returned error matches AWS's recoverable surface (Throttling,
// HandlerFailure, internal service errors). It exits on success, on
// non-recoverable error, on context cancellation, or once the attempt
// budget is exhausted. The last observed result is returned.
func retryCallable[T any](
	ctx context.Context,
	opts retryOpts,
	logHint string,
	fn func(context.Context) (T, error),
) (T, error) {
	opts = opts.withDefaults()

	var last T
	var lastErr error
	for attempt := 1; attempt <= opts.MaxAttempts; attempt++ {
		v, err := fn(ctx)
		last = v
		lastErr = err
		if err == nil {
			return v, nil
		}
		if !isRecoverable(err, "") {
			return v, err
		}
		if attempt == opts.MaxAttempts {
			break
		}
		delay := backoffDelay(attempt, opts.BaseDelay, opts.MaxDelay)
		slog.Info("ccx: retrying call on recoverable error",
			"hint", logHint,
			"attempt", attempt,
			"maxAttempts", opts.MaxAttempts,
			"delay", delay,
			"err", err)
		select {
		case <-ctx.Done():
			return last, ctx.Err()
		case <-time.After(delay):
		}
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
