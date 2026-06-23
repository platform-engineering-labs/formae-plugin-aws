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
