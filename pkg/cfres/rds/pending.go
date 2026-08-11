// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package rds

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
)

// An Aurora cluster answers as available, with the Data API switched on,
// minutes before its writer instance can run a statement. A create that finds
// the cluster in that window reports InProgress and finishes from a later
// Status, which gives it the agent's uncapped status-poll budget instead of the
// far shorter retry ladder a failed operation gets.
//
// What the create was asked to build cannot travel with the poll. A
// StatusRequest carries only RequestID, NativeID, ResourceType and
// TargetConfig — not the declared properties — so Status cannot re-derive the
// database's owner or the role's verifier from its request, and neither
// identifier may carry them: the NativeID is public and both are persisted by
// the agent, while a SCRAM verifier is offline-attackable material. The intent
// is therefore parked in this process-local store, keyed by NativeID.
//
// The store lives and dies with the plugin process, and so does the operator
// that could re-issue the create. A poll that finds no parked intent therefore
// fails the create outright: nothing left running can re-drive it, and reporting
// success would report a create that never ran.

// pendingCreateTimeout bounds the wait for a cluster to start serving. An
// Aurora cluster takes around six minutes from available to serving; the rest is
// headroom.
const pendingCreateTimeout = 15 * time.Minute

// pendingNow is the clock the wait is measured against.
var pendingNow = time.Now

// pendingEntry is one create waiting for its cluster. The sequence identifies
// this parking of this NativeID, so a poll can only end the wait it was
// answering — a stale poll must not throw away an intent a newer create parked
// under the same name.
type pendingEntry[T any] struct {
	intent   T
	deadline time.Time
	sequence uint64
}

// pendingStore holds the intents of deferred creates, keyed by NativeID.
type pendingStore[T any] struct {
	mu       sync.Mutex
	entries  map[string]pendingEntry[T]
	sequence uint64
}

func newPendingStore[T any]() *pendingStore[T] {
	return &pendingStore[T]{entries: make(map[string]pendingEntry[T])}
}

var (
	pendingDatabases = newPendingStore[*databaseSettings]()
	pendingRoles     = newPendingStore[*roleCreateIntent]()
)

// park records an intent for a later poll to finish, bounding its wait.
//
// An abandoned create — a cancelled changeset, a dependency that failed — is
// never polled again, so its deadline is never reached by a poll. Parking is
// therefore where expired intents are dropped, rather than holding a verifier
// for the life of the process.
func (s *pendingStore[T]) park(nativeID string, intent T) {
	now := pendingNow()

	s.mu.Lock()
	defer s.mu.Unlock()

	for key, entry := range s.entries {
		if now.After(entry.deadline) {
			delete(s.entries, key)
		}
	}

	s.sequence++
	s.entries[nativeID] = pendingEntry[T]{
		intent:   intent,
		deadline: now.Add(pendingCreateTimeout),
		sequence: s.sequence,
	}
}

// peek returns a parked intent and leaves it in place: a poll that finds the
// cluster still not serving has to be able to poll again.
func (s *pendingStore[T]) peek(nativeID string) (pendingEntry[T], bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.entries[nativeID]
	return entry, ok
}

// release ends the wait the sequence identifies. A terminal outcome goes
// through it — a finished create, a fault that will not clear, an expired
// deadline — and a poll answering a wait that has already been replaced ends
// nothing.
func (s *pendingStore[T]) release(nativeID string, sequence uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry, ok := s.entries[nativeID]; ok && entry.sequence == sequence {
		delete(s.entries, nativeID)
	}
}

// deferCreate parks an intent and reports the create as still running.
func deferCreate[T any](store *pendingStore[T], nativeID string, intent T) *resource.ProgressResult {
	store.park(nativeID, intent)
	return &resource.ProgressResult{
		Operation:       resource.OperationCreate,
		OperationStatus: resource.OperationStatusInProgress,
		NativeID:        nativeID,
	}
}

// resumeCreate is the polling half of a deferred create: it re-probes the
// cluster and, once the cluster answers, finishes the parked intent through
// ensure. The result reports the create rather than the poll, because the create
// is the operation whose outcome the agent is waiting on.
func resumeCreate[T any](
	ctx context.Context,
	client dataAPIClient,
	store *pendingStore[T],
	request *resource.StatusRequest,
	ensure func(context.Context, dataAPIClient, T) (*resource.ProgressResult, error),
) (*resource.StatusResult, error) {
	clusterArn, secretArn, _, err := parseNativeID(request.NativeID)
	if err != nil {
		return nil, err
	}

	entry, parked := store.peek(request.NativeID)
	if !parked {
		// The intent is gone, so this process cannot finish the create — it no
		// longer knows what it was asked to build. The failure is deliberately
		// not recoverable: the only path that reaches here is a plugin that
		// restarted mid-wait, which also took with it the operator holding the
		// original request, so a retry can only schedule another poll of a
		// process that will never have the intent. Reporting success would be
		// worse still — it would report a create that never ran.
		return pollResult(request, &resource.ProgressResult{
			Operation:       resource.OperationCreate,
			OperationStatus: resource.OperationStatusFailure,
			NativeID:        request.NativeID,
			ErrorCode:       resource.OperationErrorCodeGeneralServiceException,
			StatusMessage:   "the create was interrupted before the cluster started serving statements and cannot be resumed; apply again to create the resource",
		}), nil
	}

	ready, code, err := clusterServing(ctx, client, clusterArn, secretArn)
	switch {
	case ready:
	case code == "" || resource.IsRecoverable(code):
		// An unrecognised failure is not evidence the cluster will never serve:
		// the Data API raises no throttling exception, so a throttle, a reset
		// connection or an expired call deadline all arrive unclassified. The
		// deadline is what bounds this wait, not one failed probe.
		//
		// The cluster is probed before the deadline is given up on, so one that
		// starts serving on the boundary still finishes.
		if pendingNow().After(entry.deadline) {
			store.release(request.NativeID, entry.sequence)
			return nil, fmt.Errorf("cluster %q did not start serving statements within %s of the create: %w",
				clusterArn, pendingCreateTimeout, err)
		}
		return pollResult(request, &resource.ProgressResult{
			Operation:       resource.OperationCreate,
			OperationStatus: resource.OperationStatusInProgress,
			NativeID:        request.NativeID,
		}), nil
	default:
		store.release(request.NativeID, entry.sequence)
		return pollFailure(request, err)
	}

	progress, err := ensure(ctx, client, entry.intent)
	if err != nil {
		// A fault that clears on its own — a failover between the probe and the
		// statement — leaves the intent parked, so whichever call comes next can
		// finish the create rather than find nothing to do.
		if faultCode, recognised := recognizeDataAPIFault(err); !recognised || !resource.IsRecoverable(faultCode) {
			store.release(request.NativeID, entry.sequence)
		}
		return pollFailure(request, err)
	}
	store.release(request.NativeID, entry.sequence)
	return pollResult(request, progress), nil
}

// pollFailure reports a recognised Data API fault through the result so the
// agent sees its error code, and leaves anything else as the error it was.
func pollFailure(request *resource.StatusRequest, err error) (*resource.StatusResult, error) {
	if progress, ok := dataAPIProgressFailure(resource.OperationCreate, request.NativeID, err); ok {
		return pollResult(request, progress), nil
	}
	return nil, err
}

// pollResult carries the poll's RequestID onto the progress, which is how the
// agent matches an answer to the update it is tracking.
func pollResult(request *resource.StatusRequest, progress *resource.ProgressResult) *resource.StatusResult {
	progress.RequestID = request.RequestID
	return &resource.StatusResult{ProgressResult: progress}
}
