// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package ccx

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/platform-engineering-labs/formae/pkg/plugin"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
)

// countingHandler is a slog.Handler that counts the records routed to it,
// letting a test assert that a retry path logged through an injected logger
// rather than the package-global slog default. It records counts only — no
// message text or level is inspected.
type countingHandler struct{ count *int }

func (h countingHandler) Enabled(context.Context, slog.Level) bool  { return true }
func (h countingHandler) Handle(context.Context, slog.Record) error { *h.count++; return nil }
func (h countingHandler) WithAttrs([]slog.Attr) slog.Handler        { return h }
func (h countingHandler) WithGroup(string) slog.Handler             { return h }

// ctxWithCountingLogger returns a context carrying a plugin.Logger whose records
// land in the counter, plus the counter pointer.
func ctxWithCountingLogger() (context.Context, *int) {
	count := 0
	logger := plugin.NewPluginLogger(slog.New(countingHandler{count: &count}))
	return plugin.WithLogger(context.Background(), logger), &count
}

// Sub-millisecond options keep the retry loop fast under test while still
// exercising every code path (backoff loop, exhaustion, cancellation).
func testOpts(attempts int) retryOpts {
	return retryOpts{
		MaxAttempts: attempts,
		BaseDelay:   time.Microsecond,
		MaxDelay:    time.Microsecond,
	}
}

func TestIsRecoverable_ByErrorCode(t *testing.T) {
	for _, code := range []resource.OperationErrorCode{
		resource.OperationErrorCodeThrottling,
		resource.OperationErrorCodeInternalFailure,
		resource.OperationErrorCodeServiceInternalError,
		resource.OperationErrorCodeServiceTimeout,
		resource.OperationErrorCodeNetworkFailure,
		resource.OperationErrorCodeNotStabilized,
		resource.OperationErrorCodeGeneralServiceException,
	} {
		if !isRecoverable(nil, string(code)) {
			t.Errorf("expected %s to be recoverable", code)
		}
	}
	for _, code := range []resource.OperationErrorCode{
		resource.OperationErrorCodeNotFound,
		resource.OperationErrorCodeAccessDenied,
		resource.OperationErrorCodeInvalidRequest,
	} {
		if isRecoverable(nil, string(code)) {
			t.Errorf("expected %s to be non-recoverable", code)
		}
	}
}

func TestIsRecoverable_ByErrorMessage(t *testing.T) {
	for _, msg := range []string{
		"ThrottlingException: Rate exceeded",
		"HandlerFailureException: Internal Failure occurred in downstream resource handler",
		"InternalFailure",
		"exceeded maximum number of attempts, 2",
		"ServiceUnavailable",
	} {
		if !isRecoverable(errors.New(msg), "") {
			t.Errorf("expected %q to be recoverable", msg)
		}
	}
	if isRecoverable(errors.New("ResourceNotFoundException"), "") {
		t.Error("expected NotFound message to be non-recoverable")
	}
	if isRecoverable(context.Canceled, "") {
		t.Error("expected context.Canceled to be non-recoverable")
	}
}

func TestRetryRead_SucceedsAfterTransientThrottling(t *testing.T) {
	calls := 0
	res, err := retryRead(context.Background(), testOpts(5), "test",
		func(ctx context.Context) (*resource.ReadResult, error) {
			calls++
			if calls < 3 {
				return &resource.ReadResult{ErrorCode: resource.OperationErrorCodeThrottling}, nil
			}
			return &resource.ReadResult{Properties: `{"k":"v"}`}, nil
		})
	if err != nil {
		t.Fatalf("retryRead: %v", err)
	}
	if res == nil || res.Properties != `{"k":"v"}` {
		t.Fatalf("expected properties on success, got %+v", res)
	}
	if calls != 3 {
		t.Errorf("expected 3 calls, got %d", calls)
	}
}

func TestRetryRead_ExhaustsBudgetOnPersistentThrottling(t *testing.T) {
	calls := 0
	res, err := retryRead(context.Background(), testOpts(4), "test",
		func(ctx context.Context) (*resource.ReadResult, error) {
			calls++
			return &resource.ReadResult{ErrorCode: resource.OperationErrorCodeThrottling}, nil
		})
	if err != nil {
		t.Fatalf("retryRead should not surface err on exhausted recoverable code, got %v", err)
	}
	if res == nil || res.ErrorCode != resource.OperationErrorCodeThrottling {
		t.Errorf("expected last result with Throttling, got %+v", res)
	}
	if calls != 4 {
		t.Errorf("expected 4 calls, got %d", calls)
	}
}

func TestRetryRead_NonRecoverableExitsImmediately(t *testing.T) {
	calls := 0
	res, err := retryRead(context.Background(), testOpts(5), "test",
		func(ctx context.Context) (*resource.ReadResult, error) {
			calls++
			return &resource.ReadResult{ErrorCode: resource.OperationErrorCodeNotFound}, nil
		})
	if err != nil {
		t.Fatalf("retryRead: %v", err)
	}
	if res == nil || res.ErrorCode != resource.OperationErrorCodeNotFound {
		t.Errorf("expected NotFound to be returned without retry, got %+v", res)
	}
	if calls != 1 {
		t.Errorf("expected 1 call for non-recoverable error, got %d", calls)
	}
}

func TestRetryRead_ContextCancelExitsCleanly(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	_, err := retryRead(ctx, retryOpts{MaxAttempts: 10, BaseDelay: 50 * time.Millisecond, MaxDelay: 50 * time.Millisecond}, "test",
		func(ctx context.Context) (*resource.ReadResult, error) {
			calls++
			if calls == 1 {
				cancel()
			}
			return &resource.ReadResult{ErrorCode: resource.OperationErrorCodeThrottling}, nil
		})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

func TestRetryCallable_SucceedsAfterTransientError(t *testing.T) {
	calls := 0
	result, err := retryCallable(context.Background(), testOpts(5), "test",
		func(ctx context.Context) (string, error) {
			calls++
			if calls < 3 {
				return "", fmt.Errorf("ThrottlingException: Rate exceeded")
			}
			return "ok", nil
		})
	if err != nil {
		t.Fatalf("retryCallable: %v", err)
	}
	if result != "ok" {
		t.Errorf("expected ok, got %q", result)
	}
	if calls != 3 {
		t.Errorf("expected 3 calls, got %d", calls)
	}
}

func TestRetryCallable_ExhaustsOnPersistentHandlerFailure(t *testing.T) {
	calls := 0
	_, err := retryCallable(context.Background(), testOpts(4), "test",
		func(ctx context.Context) (string, error) {
			calls++
			return "", fmt.Errorf("HandlerFailureException: Internal Failure occurred in downstream resource handler")
		})
	if err == nil {
		t.Fatal("expected error after exhausted budget")
	}
	if calls != 4 {
		t.Errorf("expected 4 calls, got %d", calls)
	}
}

func TestRetryCallable_NonRecoverableErrorReturnsImmediately(t *testing.T) {
	calls := 0
	_, err := retryCallable(context.Background(), testOpts(5), "test",
		func(ctx context.Context) (string, error) {
			calls++
			return "", fmt.Errorf("ValidationException: invalid input")
		})
	if err == nil {
		t.Fatal("expected error to propagate")
	}
	if calls != 1 {
		t.Errorf("expected 1 call for non-recoverable error, got %d", calls)
	}
}

// The retry paths must log through the context logger so the agent's plugin
// supervisor preserves the intended INFO level (and the SDK routing attributes)
// instead of flattening the line to ERROR off stderr. These assert the wiring
// only: that a retried call records through the logger injected on ctx.

func TestRetryCallable_LogsViaContextLogger(t *testing.T) {
	ctx, count := ctxWithCountingLogger()
	_, _ = retryCallable(ctx, testOpts(3), "test",
		func(ctx context.Context) (string, error) {
			return "", fmt.Errorf("ThrottlingException: Rate exceeded")
		})
	if *count == 0 {
		t.Error("expected retry to record through the context logger, got 0 records")
	}
}

func TestRetryRead_LogsViaContextLogger(t *testing.T) {
	ctx, count := ctxWithCountingLogger()
	_, _ = retryRead(ctx, testOpts(3), "test",
		func(ctx context.Context) (*resource.ReadResult, error) {
			return &resource.ReadResult{ErrorCode: resource.OperationErrorCodeThrottling}, nil
		})
	if *count == 0 {
		t.Error("expected retry to record through the context logger, got 0 records")
	}
}

// budgetedOpts is testOpts plus a wall-clock budget on the whole loop.
func budgetedOpts(attempts int, budget time.Duration) retryOpts {
	o := testOpts(attempts)
	o.Budget = budget
	return o
}

// throttledRead is an attempt that fails fast with a recoverable CCAPI code.
func throttledRead(context.Context) (*resource.ReadResult, error) {
	return &resource.ReadResult{ErrorCode: resource.OperationErrorCodeThrottling}, nil
}

// blockingRead is an attempt that never returns of its own accord: it runs
// until the context it was handed is done, which is what an AWS call blocked
// behind the SDK's own retryer looks like from the retry loop's point of view.
func blockingRead(ctx context.Context) (*resource.ReadResult, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestRetryRead_BudgetExitsBeforeMaxAttempts(t *testing.T) {
	calls := 0
	opts := budgetedOpts(20, 60*time.Millisecond)
	res, err := retryRead(context.Background(), opts, "test",
		func(ctx context.Context) (*resource.ReadResult, error) {
			calls++
			time.Sleep(25 * time.Millisecond)
			return &resource.ReadResult{ErrorCode: resource.OperationErrorCodeThrottling}, nil
		})
	if !errors.Is(err, errRetryBudgetExhausted) {
		t.Fatalf("expected budget-exhausted sentinel, got %v", err)
	}
	if calls >= opts.MaxAttempts {
		t.Errorf("expected the budget to stop the loop before %d attempts, got %d calls", opts.MaxAttempts, calls)
	}
	if res == nil || res.ErrorCode != resource.OperationErrorCodeThrottling {
		t.Errorf("expected the last observed result to be returned, got %+v", res)
	}
}

func TestRetryRead_SingleAttemptConsumingBudgetReturnsSentinel(t *testing.T) {
	calls := 0
	_, err := retryRead(context.Background(), budgetedOpts(5, 20*time.Millisecond), "test",
		func(ctx context.Context) (*resource.ReadResult, error) {
			calls++
			return blockingRead(ctx)
		})
	if !errors.Is(err, errRetryBudgetExhausted) {
		t.Fatalf("expected budget-exhausted sentinel, got %v", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected the sentinel to wrap the cause, got %v", err)
	}
	if calls != 1 {
		t.Errorf("expected 1 call, got %d", calls)
	}
}

func TestRetryRead_FastFailuresDecliningToSleepReturnSentinel(t *testing.T) {
	// Attempts fail immediately, so the budget still has almost all of its
	// wall clock left; it is the next backoff that would cross the deadline.
	// A budget check keyed only on the deadline having fired would miss this
	// exit and return a bare "sparse success" instead.
	calls := 0
	opts := retryOpts{MaxAttempts: 5, BaseDelay: time.Hour, MaxDelay: time.Hour, Budget: time.Minute}
	start := time.Now()
	res, err := retryRead(context.Background(), opts, "test",
		func(ctx context.Context) (*resource.ReadResult, error) {
			calls++
			return throttledRead(ctx)
		})
	if !errors.Is(err, errRetryBudgetExhausted) {
		t.Fatalf("expected budget-exhausted sentinel, got err=%v res=%+v", err, res)
	}
	if calls >= opts.MaxAttempts {
		t.Errorf("expected the loop to stop before exhausting %d attempts, got %d calls", opts.MaxAttempts, calls)
	}
	if elapsed := time.Since(start); elapsed >= opts.Budget {
		t.Errorf("expected the loop to decline the sleep rather than wait out the budget, took %v", elapsed)
	}
}

func TestRetryRead_BudgetAlreadyExpiredOnEntryReturnsSentinel(t *testing.T) {
	calls := 0
	_, err := retryRead(context.Background(), budgetedOpts(5, time.Nanosecond), "test",
		func(ctx context.Context) (*resource.ReadResult, error) {
			calls++
			return throttledRead(ctx)
		})
	if !errors.Is(err, errRetryBudgetExhausted) {
		t.Fatalf("expected budget-exhausted sentinel, got %v", err)
	}
	if calls != 1 {
		t.Errorf("expected 1 call, got %d", calls)
	}
}

func TestRetryRead_ParentCancelIsNotBudgetExhaustion(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	opts := budgetedOpts(10, time.Minute)
	opts.BaseDelay = 50 * time.Millisecond
	opts.MaxDelay = 50 * time.Millisecond
	_, err := retryRead(ctx, opts, "test",
		func(ctx context.Context) (*resource.ReadResult, error) {
			calls++
			if calls == 1 {
				cancel()
			}
			return throttledRead(ctx)
		})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if errors.Is(err, errRetryBudgetExhausted) {
		t.Error("parent cancellation must not be reported as budget exhaustion")
	}
}

func TestRetryRead_ParentDeadlineIsNotBudgetExhaustion(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err := retryRead(ctx, budgetedOpts(5, time.Minute), "test", blockingRead)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context.DeadlineExceeded, got %v", err)
	}
	if errors.Is(err, errRetryBudgetExhausted) {
		t.Error("a parent deadline shorter than the budget must not be reported as budget exhaustion")
	}
}

func TestRetryRead_NonRecoverableFailureUnderBudgetHasNoSentinel(t *testing.T) {
	calls := 0
	res, err := retryRead(context.Background(), budgetedOpts(5, time.Minute), "test",
		func(ctx context.Context) (*resource.ReadResult, error) {
			calls++
			return &resource.ReadResult{ErrorCode: resource.OperationErrorCodeNotFound}, nil
		})
	if err != nil {
		t.Fatalf("retryRead: %v", err)
	}
	if res == nil || res.ErrorCode != resource.OperationErrorCodeNotFound {
		t.Errorf("expected NotFound to be returned without retry, got %+v", res)
	}
	if calls != 1 {
		t.Errorf("expected 1 call for non-recoverable error, got %d", calls)
	}
}

func TestRetryRead_SuccessInsideBudgetIsUnaffected(t *testing.T) {
	calls := 0
	res, err := retryRead(context.Background(), budgetedOpts(5, time.Minute), "test",
		func(ctx context.Context) (*resource.ReadResult, error) {
			calls++
			if calls < 3 {
				return throttledRead(ctx)
			}
			return &resource.ReadResult{Properties: `{"k":"v"}`}, nil
		})
	if err != nil {
		t.Fatalf("retryRead: %v", err)
	}
	if res == nil || res.Properties != `{"k":"v"}` {
		t.Fatalf("expected properties on success, got %+v", res)
	}
	if calls != 3 {
		t.Errorf("expected 3 calls, got %d", calls)
	}
}

// Discovery's ListResources is deliberately unbudgeted: it runs outside any
// watchdog-observed RPC and keeps the long, attempt-bounded budget.
func TestRetryRead_ZeroBudgetKeepsAttemptBoundedBehaviour(t *testing.T) {
	calls := 0
	res, err := retryRead(context.Background(), testOpts(4), "test",
		func(ctx context.Context) (*resource.ReadResult, error) {
			calls++
			return throttledRead(ctx)
		})
	if err != nil {
		t.Fatalf("an unbudgeted loop must exhaust its attempts without an error, got %v", err)
	}
	if res == nil || res.ErrorCode != resource.OperationErrorCodeThrottling {
		t.Errorf("expected last result with Throttling, got %+v", res)
	}
	if calls != 4 {
		t.Errorf("expected 4 calls, got %d", calls)
	}
}

func TestRetryCallable_BudgetExitsBeforeMaxAttempts(t *testing.T) {
	calls := 0
	opts := budgetedOpts(20, 60*time.Millisecond)
	_, err := retryCallable(context.Background(), opts, "test",
		func(ctx context.Context) (string, error) {
			calls++
			time.Sleep(25 * time.Millisecond)
			return "", fmt.Errorf("ThrottlingException: Rate exceeded")
		})
	if !errors.Is(err, errRetryBudgetExhausted) {
		t.Fatalf("expected budget-exhausted sentinel, got %v", err)
	}
	if calls >= opts.MaxAttempts {
		t.Errorf("expected the budget to stop the loop before %d attempts, got %d calls", opts.MaxAttempts, calls)
	}
}

func TestRetryCallable_SingleAttemptConsumingBudgetReturnsSentinel(t *testing.T) {
	calls := 0
	_, err := retryCallable(context.Background(), budgetedOpts(5, 20*time.Millisecond), "test",
		func(ctx context.Context) (string, error) {
			calls++
			<-ctx.Done()
			return "", ctx.Err()
		})
	if !errors.Is(err, errRetryBudgetExhausted) {
		t.Fatalf("expected budget-exhausted sentinel, got %v", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected the sentinel to wrap the cause, got %v", err)
	}
	if calls != 1 {
		t.Errorf("expected 1 call, got %d", calls)
	}
}

func TestRetryCallable_FastFailuresDecliningToSleepReturnSentinel(t *testing.T) {
	calls := 0
	opts := retryOpts{MaxAttempts: 5, BaseDelay: time.Hour, MaxDelay: time.Hour, Budget: time.Minute}
	start := time.Now()
	_, err := retryCallable(context.Background(), opts, "test",
		func(ctx context.Context) (string, error) {
			calls++
			return "", fmt.Errorf("ThrottlingException: Rate exceeded")
		})
	if !errors.Is(err, errRetryBudgetExhausted) {
		t.Fatalf("expected budget-exhausted sentinel, got %v", err)
	}
	if calls >= opts.MaxAttempts {
		t.Errorf("expected the loop to stop before exhausting %d attempts, got %d calls", opts.MaxAttempts, calls)
	}
	if elapsed := time.Since(start); elapsed >= opts.Budget {
		t.Errorf("expected the loop to decline the sleep rather than wait out the budget, took %v", elapsed)
	}
}

func TestRetryCallable_BudgetAlreadyExpiredOnEntryReturnsSentinel(t *testing.T) {
	calls := 0
	_, err := retryCallable(context.Background(), budgetedOpts(5, time.Nanosecond), "test",
		func(ctx context.Context) (string, error) {
			calls++
			return "", fmt.Errorf("ThrottlingException: Rate exceeded")
		})
	if !errors.Is(err, errRetryBudgetExhausted) {
		t.Fatalf("expected budget-exhausted sentinel, got %v", err)
	}
	if calls != 1 {
		t.Errorf("expected 1 call, got %d", calls)
	}
}

func TestRetryCallable_ParentCancelIsNotBudgetExhaustion(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	opts := budgetedOpts(10, time.Minute)
	opts.BaseDelay = 50 * time.Millisecond
	opts.MaxDelay = 50 * time.Millisecond
	_, err := retryCallable(ctx, opts, "test",
		func(ctx context.Context) (string, error) {
			calls++
			if calls == 1 {
				cancel()
			}
			return "", fmt.Errorf("ThrottlingException: Rate exceeded")
		})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if errors.Is(err, errRetryBudgetExhausted) {
		t.Error("parent cancellation must not be reported as budget exhaustion")
	}
}

func TestRetryCallable_ParentDeadlineIsNotBudgetExhaustion(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err := retryCallable(ctx, budgetedOpts(5, time.Minute), "test",
		func(ctx context.Context) (string, error) {
			<-ctx.Done()
			return "", ctx.Err()
		})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context.DeadlineExceeded, got %v", err)
	}
	if errors.Is(err, errRetryBudgetExhausted) {
		t.Error("a parent deadline shorter than the budget must not be reported as budget exhaustion")
	}
}

func TestRetryCallable_NonRecoverableFailureUnderBudgetHasNoSentinel(t *testing.T) {
	calls := 0
	_, err := retryCallable(context.Background(), budgetedOpts(5, time.Minute), "test",
		func(ctx context.Context) (string, error) {
			calls++
			return "", fmt.Errorf("ValidationException: invalid input")
		})
	if err == nil {
		t.Fatal("expected error to propagate")
	}
	if errors.Is(err, errRetryBudgetExhausted) {
		t.Error("a non-recoverable failure must not be reported as budget exhaustion")
	}
	if calls != 1 {
		t.Errorf("expected 1 call for non-recoverable error, got %d", calls)
	}
}

func TestRetryCallable_SuccessInsideBudgetIsUnaffected(t *testing.T) {
	calls := 0
	result, err := retryCallable(context.Background(), budgetedOpts(5, time.Minute), "test",
		func(ctx context.Context) (string, error) {
			calls++
			if calls < 3 {
				return "", fmt.Errorf("ThrottlingException: Rate exceeded")
			}
			return "ok", nil
		})
	if err != nil {
		t.Fatalf("retryCallable: %v", err)
	}
	if result != "ok" {
		t.Errorf("expected ok, got %q", result)
	}
	if calls != 3 {
		t.Errorf("expected 3 calls, got %d", calls)
	}
}

func TestRetryCallable_ZeroBudgetKeepsAttemptBoundedBehaviour(t *testing.T) {
	calls := 0
	_, err := retryCallable(context.Background(), testOpts(4), "test",
		func(ctx context.Context) (string, error) {
			calls++
			return "", fmt.Errorf("HandlerFailureException: Internal Failure occurred in downstream resource handler")
		})
	if err == nil {
		t.Fatal("expected error after exhausted attempts")
	}
	if errors.Is(err, errRetryBudgetExhausted) {
		t.Error("an unbudgeted loop must never report budget exhaustion")
	}
	if calls != 4 {
		t.Errorf("expected 4 calls, got %d", calls)
	}
}

func TestBackoffDelay_ExponentialWithJitter(t *testing.T) {
	base := 100 * time.Millisecond
	max := 5 * time.Second
	last := time.Duration(0)
	for attempt := 1; attempt <= 5; attempt++ {
		d := backoffDelay(attempt, base, max)
		if d < base {
			t.Errorf("attempt %d: delay %v less than base %v", attempt, d, base)
		}
		if d > max+max/4 {
			t.Errorf("attempt %d: delay %v exceeds max+jitter %v", attempt, d, max+max/4)
		}
		if attempt > 1 && d < last/2 {
			t.Errorf("attempt %d: delay %v unexpectedly smaller than half of prev %v", attempt, d, last)
		}
		last = d
	}
}

// deadlineOpts is testOpts bounded by an absolute instant instead of a duration,
// as callers do when several calls inside one watched RPC share a budget.
func deadlineOpts(attempts int, deadline time.Time) retryOpts {
	o := testOpts(attempts)
	o.Deadline = deadline
	return o
}

func TestWithRetryBudget_NeitherBudgetNorDeadlineLeavesContextUntouched(t *testing.T) {
	ctx := context.Background()
	callCtx, deadline, cancel := withRetryBudget(ctx, retryOpts{})
	defer cancel()
	if callCtx != ctx {
		t.Error("an unbudgeted loop must run its attempts on the caller's own context")
	}
	if !deadline.IsZero() {
		t.Errorf("expected no deadline, got %v", deadline)
	}
}

func TestWithRetryBudget_ExplicitDeadlineWinsOverBudget(t *testing.T) {
	want := time.Now().Add(30 * time.Millisecond)
	_, deadline, cancel := withRetryBudget(context.Background(), retryOpts{Budget: time.Hour, Deadline: want})
	defer cancel()
	if !deadline.Equal(want) {
		t.Errorf("expected the explicit deadline %v, got %v", want, deadline)
	}
}

func TestRetryRead_ExplicitDeadlineBoundsTheLoop(t *testing.T) {
	start := time.Now()
	_, err := retryRead(context.Background(), deadlineOpts(5, start.Add(30*time.Millisecond)), "test", blockingRead)
	if !errors.Is(err, errRetryBudgetExhausted) {
		t.Fatalf("expected budget-exhausted sentinel, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("expected the loop to stop at its deadline, took %v", elapsed)
	}
}

// A deadline shared with an earlier call in the same RPC can already be spent by
// the time this loop starts, which must exhaust immediately rather than run a
// full budget of its own.
func TestRetryRead_ExplicitDeadlineAlreadyPastReturnsSentinel(t *testing.T) {
	calls := 0
	_, err := retryRead(context.Background(), deadlineOpts(5, time.Now().Add(-time.Second)), "test",
		func(ctx context.Context) (*resource.ReadResult, error) {
			calls++
			return throttledRead(ctx)
		})
	if !errors.Is(err, errRetryBudgetExhausted) {
		t.Fatalf("expected budget-exhausted sentinel, got %v", err)
	}
	if calls != 1 {
		t.Errorf("expected 1 call, got %d", calls)
	}
}

func TestRetryRead_ExplicitDeadlineParentCancelIsNotBudgetExhaustion(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, err := retryRead(ctx, deadlineOpts(5, time.Now().Add(time.Minute)), "test",
		func(ctx context.Context) (*resource.ReadResult, error) {
			cancel()
			<-ctx.Done()
			return nil, ctx.Err()
		})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if errors.Is(err, errRetryBudgetExhausted) {
		t.Error("parent cancellation must not be reported as budget exhaustion")
	}
}

func TestRetryCallable_ExplicitDeadlineBoundsTheLoop(t *testing.T) {
	start := time.Now()
	_, err := retryCallable(context.Background(), deadlineOpts(5, start.Add(30*time.Millisecond)), "test",
		func(ctx context.Context) (string, error) {
			<-ctx.Done()
			return "", ctx.Err()
		})
	if !errors.Is(err, errRetryBudgetExhausted) {
		t.Fatalf("expected budget-exhausted sentinel, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("expected the loop to stop at its deadline, took %v", elapsed)
	}
}

func TestRetryCallable_ExplicitDeadlineParentDeadlineIsNotBudgetExhaustion(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err := retryCallable(ctx, deadlineOpts(5, time.Now().Add(time.Minute)), "test",
		func(ctx context.Context) (string, error) {
			<-ctx.Done()
			return "", ctx.Err()
		})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context.DeadlineExceeded, got %v", err)
	}
	if errors.Is(err, errRetryBudgetExhausted) {
		t.Error("a parent deadline shorter than the shared deadline must not be reported as budget exhaustion")
	}
}
