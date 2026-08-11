// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package rds

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/rdsdata"
	rdsdatatypes "github.com/aws/aws-sdk-go-v2/service/rdsdata/types"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// stashClock pins the clock the pending deadline is measured against, and
// returns it so a test can move it forward. The original is restored when the
// test ends.
func stashClock(t *testing.T) *time.Time {
	t.Helper()
	previous := pendingNow
	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	pendingNow = func() time.Time { return now }
	t.Cleanup(func() { pendingNow = previous })
	return &now
}

// forgetPending drops whatever a test parked, so package-level state does not
// travel between tests.
func forgetPending(t *testing.T, nativeID string) {
	t.Helper()
	t.Cleanup(func() {
		if entry, parked := pendingDatabases.peek(nativeID); parked {
			pendingDatabases.release(nativeID, entry.sequence)
		}
		if entry, parked := pendingRoles.peek(nativeID); parked {
			pendingRoles.release(nativeID, entry.sequence)
		}
	})
}

// notServing answers every statement with the fault an Aurora cluster raises
// while its writer instance is still coming up.
func notServing() *mockDataAPIClient {
	client := &mockDataAPIClient{}
	client.On("ExecuteStatement", mock.Anything, mock.Anything).Return(nil, clusterNotServing())
	return client
}

func TestDatabaseCreateWaitsForAClusterThatIsNotServingYet(t *testing.T) {
	nativeID := buildNativeID(testClusterArn, testSecretArn, "appdb")
	forgetPending(t, nativeID)

	client := notServing()
	result, err := testDatabase().createWithClient(context.Background(), client, aurora(t),
		&resource.CreateRequest{Properties: databaseProps(t, nil)})
	require.NoError(t, err)

	assert.Equal(t, resource.OperationCreate, result.ProgressResult.Operation)
	assert.Equal(t, resource.OperationStatusInProgress, result.ProgressResult.OperationStatus)
	assert.Equal(t, nativeID, result.ProgressResult.NativeID)
	assert.Equal(t, []string{"SELECT 1"}, client.statements,
		"a cluster that cannot answer must be probed and left alone — no DDL may reach the wire")
}

func TestDatabaseRoleCreateWaitsForAClusterThatIsNotServingYet(t *testing.T) {
	nativeID := buildNativeID(testClusterArn, testSecretArn, "appuser")
	forgetPending(t, nativeID)

	client := notServing()
	result, err := testRole().createWithClient(context.Background(), client, aurora(t),
		&resource.CreateRequest{Properties: roleProps(t, nil)})
	require.NoError(t, err)

	assert.Equal(t, resource.OperationCreate, result.ProgressResult.Operation)
	assert.Equal(t, resource.OperationStatusInProgress, result.ProgressResult.OperationStatus)
	assert.Equal(t, nativeID, result.ProgressResult.NativeID)
	assert.Equal(t, []string{"SELECT 1"}, client.statements,
		"a cluster that cannot answer must be probed and left alone — no DDL may reach the wire")
}

// The parked intent carries the composed verifier, never the plaintext: it
// outlives the request that produced it, so the reusable credential must not.
func TestAParkedRoleCreateHoldsTheVerifierAndNotThePlaintext(t *testing.T) {
	nativeID := buildNativeID(testClusterArn, testSecretArn, "appuser")
	forgetPending(t, nativeID)

	_, err := testRole().createWithClient(context.Background(), notServing(), aurora(t),
		&resource.CreateRequest{Properties: roleProps(t, nil)})
	require.NoError(t, err)

	entry, parked := pendingRoles.peek(nativeID)
	require.True(t, parked)
	assert.Contains(t, entry.intent.verifier, "SCRAM-SHA-256$")
	assert.NotContains(t, entry.intent.verifier, testPassword)
}

func TestDatabaseStatusKeepsWaitingWhileTheClusterIsNotServing(t *testing.T) {
	nativeID := buildNativeID(testClusterArn, testSecretArn, "appdb")
	forgetPending(t, nativeID)

	_, err := testDatabase().createWithClient(context.Background(), notServing(), aurora(t),
		&resource.CreateRequest{Properties: databaseProps(t, nil)})
	require.NoError(t, err)

	client := notServing()
	result, err := testDatabase().statusWithClient(context.Background(), client,
		&resource.StatusRequest{NativeID: nativeID})
	require.NoError(t, err)

	assert.Equal(t, resource.OperationCreate, result.ProgressResult.Operation)
	assert.Equal(t, resource.OperationStatusInProgress, result.ProgressResult.OperationStatus)
	assert.Equal(t, nativeID, result.ProgressResult.NativeID)
	assert.Equal(t, []string{"SELECT 1"}, client.statements)
}

func TestDatabaseStatusCreatesTheDatabaseOnceTheClusterServes(t *testing.T) {
	nativeID := buildNativeID(testClusterArn, testSecretArn, "appdb")
	forgetPending(t, nativeID)

	_, err := testDatabase().createWithClient(context.Background(), notServing(), aurora(t),
		&resource.CreateRequest{Properties: databaseProps(t, nil)})
	require.NoError(t, err)

	client := &mockDataAPIClient{}
	// readiness probe
	client.On("ExecuteStatement", mock.Anything, mock.Anything).Return(&rdsdata.ExecuteStatementOutput{}, nil).Once()
	// catalog probe → absent
	client.On("ExecuteStatement", mock.Anything, mock.Anything).Return(emptyDatabaseCatalog(t), nil).Once()
	// membership probe → already a member
	client.On("ExecuteStatement", mock.Anything, mock.Anything).Return(membershipHeld(t, true), nil).Once()
	// CREATE DATABASE
	client.On("ExecuteStatement", mock.Anything, mock.Anything).Return(&rdsdata.ExecuteStatementOutput{}, nil).Once()
	// read-back
	client.On("ExecuteStatement", mock.Anything, mock.Anything).Return(existingDatabaseCatalog(t, "appuser"), nil).Once()

	result, err := testDatabase().statusWithClient(context.Background(), client,
		&resource.StatusRequest{NativeID: nativeID})
	require.NoError(t, err)

	assert.Equal(t, resource.OperationCreate, result.ProgressResult.Operation)
	assert.Equal(t, resource.OperationStatusSuccess, result.ProgressResult.OperationStatus)
	assert.Equal(t, nativeID, result.ProgressResult.NativeID)

	var props map[string]any
	require.NoError(t, json.Unmarshal(result.ProgressResult.ResourceProperties, &props))
	assert.Equal(t, "appdb", props["DatabaseName"])
	assert.Equal(t, "appuser", props["Owner"])

	assert.Equal(t, 1, strings.Count(strings.Join(client.statements, "\n"), "CREATE DATABASE"),
		"the deferred create must run its DDL exactly once")

	_, parked := pendingDatabases.peek(nativeID)
	assert.False(t, parked, "a finished create must not stay parked")
}

func TestDatabaseRoleStatusCreatesTheRoleOnceTheClusterServes(t *testing.T) {
	nativeID := buildNativeID(testClusterArn, testSecretArn, "appuser")
	forgetPending(t, nativeID)

	_, err := testRole().createWithClient(context.Background(), notServing(), aurora(t),
		&resource.CreateRequest{Properties: roleProps(t, nil)})
	require.NoError(t, err)

	client := &mockDataAPIClient{}
	// readiness probe
	client.On("ExecuteStatement", mock.Anything, mock.Anything).Return(&rdsdata.ExecuteStatementOutput{}, nil).Once()
	// catalog probe → absent
	client.On("ExecuteStatement", mock.Anything, mock.Anything).Return(emptyRoleCatalog(t), nil).Once()
	// CREATE ROLE
	client.On("ExecuteStatement", mock.Anything, mock.Anything).Return(&rdsdata.ExecuteStatementOutput{}, nil).Once()
	// read-back
	client.On("ExecuteStatement", mock.Anything, mock.Anything).Return(existingRoleCatalog(t, true), nil).Once()

	result, err := testRole().statusWithClient(context.Background(), client,
		&resource.StatusRequest{NativeID: nativeID})
	require.NoError(t, err)

	assert.Equal(t, resource.OperationCreate, result.ProgressResult.Operation)
	assert.Equal(t, resource.OperationStatusSuccess, result.ProgressResult.OperationStatus)

	create := client.statements[2]
	assert.True(t, strings.HasPrefix(create, `CREATE ROLE "appuser" LOGIN PASSWORD '`), "got: %s", create)
	assert.Contains(t, create, "SCRAM-SHA-256$4096:")
	assert.NotContains(t, create, testPassword)

	var props map[string]any
	require.NoError(t, json.Unmarshal(result.ProgressResult.ResourceProperties, &props))
	assert.NotContains(t, props, "Password")

	_, parked := pendingRoles.peek(nativeID)
	assert.False(t, parked, "a finished create must not stay parked")
}

// The Data API raises no throttling exception, so a throttle — like a reset
// connection or an expired call deadline — reaches the probe unrecognised. That
// is not evidence the cluster will never serve, and ending the wait on it would
// fail exactly the create the wait exists to protect.
func TestStatusKeepsWaitingWhenTheProbeFailsUnrecognisably(t *testing.T) {
	nativeID := buildNativeID(testClusterArn, testSecretArn, "appdb")
	forgetPending(t, nativeID)

	_, err := testDatabase().createWithClient(context.Background(), notServing(), aurora(t),
		&resource.CreateRequest{Properties: databaseProps(t, nil)})
	require.NoError(t, err)

	client := &mockDataAPIClient{}
	client.On("ExecuteStatement", mock.Anything, mock.Anything).
		Return(nil, errors.New("connection reset by peer")).Once()

	result, err := testDatabase().statusWithClient(context.Background(), client,
		&resource.StatusRequest{NativeID: nativeID})
	require.NoError(t, err)

	assert.Equal(t, resource.OperationStatusInProgress, result.ProgressResult.OperationStatus)
	_, parked := pendingDatabases.peek(nativeID)
	assert.True(t, parked, "an unrecognised probe failure must not end the wait")
}

// A recoverable fault raised while the create is being finished — a failover
// mid-DDL — leaves the intent parked, so the poll that follows can finish the
// create instead of finding nothing to do.
func TestARecoverableFaultWhileFinishingLeavesTheCreateParked(t *testing.T) {
	nativeID := buildNativeID(testClusterArn, testSecretArn, "appdb")
	forgetPending(t, nativeID)

	_, err := testDatabase().createWithClient(context.Background(), notServing(), aurora(t),
		&resource.CreateRequest{Properties: databaseProps(t, nil)})
	require.NoError(t, err)

	client := &mockDataAPIClient{}
	servingProbe(client)
	client.On("ExecuteStatement", mock.Anything, mock.Anything).
		Return(nil, &rdsdatatypes.DatabaseUnavailableException{Message: strPtr("the database is unavailable")}).Once()

	result, err := testDatabase().statusWithClient(context.Background(), client,
		&resource.StatusRequest{NativeID: nativeID})
	require.NoError(t, err)

	assert.Equal(t, resource.OperationStatusFailure, result.ProgressResult.OperationStatus)
	assert.Equal(t, resource.OperationErrorCodeNotStabilized, result.ProgressResult.ErrorCode)
	_, parked := pendingDatabases.peek(nativeID)
	assert.True(t, parked, "a fault that clears on its own must not throw the intent away")
}

// An abandoned create — a cancelled changeset, a dependency that failed — is
// never polled again, so its deadline is never reached by a poll. Parking is
// where the expired ones are dropped, so nothing holds a verifier for the life
// of the process.
func TestParkingDropsIntentsWhoseWaitHasRunOut(t *testing.T) {
	abandoned := buildNativeID(testClusterArn, testSecretArn, "olddb")
	fresh := buildNativeID(testClusterArn, testSecretArn, "appdb")
	forgetPending(t, abandoned)
	forgetPending(t, fresh)
	clock := stashClock(t)

	_, err := testDatabase().createWithClient(context.Background(), notServing(), aurora(t),
		&resource.CreateRequest{Properties: databaseProps(t, map[string]any{"DatabaseName": "olddb"})})
	require.NoError(t, err)

	*clock = clock.Add(pendingCreateTimeout + time.Minute)

	_, err = testDatabase().createWithClient(context.Background(), notServing(), aurora(t),
		&resource.CreateRequest{Properties: databaseProps(t, nil)})
	require.NoError(t, err)

	_, parked := pendingDatabases.peek(abandoned)
	assert.False(t, parked, "an expired intent must not outlive the wait it belongs to")
	_, parked = pendingDatabases.peek(fresh)
	assert.True(t, parked)
}

// A plugin process that restarted mid-wait no longer knows what it was asked to
// build, and the poll is all that survives: the operator that could re-drive the
// create died with the process, so a recoverable failure here would poll a
// plugin that can never answer. The create is failed instead, and the apply is
// run again.
func TestStatusWithNoParkedCreateFailsTerminally(t *testing.T) {
	for _, tt := range []struct {
		name   string
		object string
		status func(context.Context, dataAPIClient, *resource.StatusRequest) (*resource.StatusResult, error)
	}{
		{name: "database", object: "appdb", status: testDatabase().statusWithClient},
		{name: "role", object: "appuser", status: testRole().statusWithClient},
	} {
		t.Run(tt.name, func(t *testing.T) {
			client := &mockDataAPIClient{}
			result, err := tt.status(context.Background(), client,
				&resource.StatusRequest{NativeID: buildNativeID(testClusterArn, testSecretArn, tt.object)})
			require.NoError(t, err)

			assert.Equal(t, resource.OperationStatusFailure, result.ProgressResult.OperationStatus)
			assert.Equal(t, resource.OperationErrorCodeGeneralServiceException, result.ProgressResult.ErrorCode)
			assert.False(t, resource.IsRecoverable(result.ProgressResult.ErrorCode),
				"a retry would poll a plugin that cannot know what to build, forever")
			assert.NotEmpty(t, result.ProgressResult.StatusMessage)
			assert.Empty(t, client.statements, "nothing may reach the wire without a parked create")
		})
	}
}

func TestStatusFailsTerminallyOnceTheWaitRunsOut(t *testing.T) {
	nativeID := buildNativeID(testClusterArn, testSecretArn, "appdb")
	forgetPending(t, nativeID)
	clock := stashClock(t)

	_, err := testDatabase().createWithClient(context.Background(), notServing(), aurora(t),
		&resource.CreateRequest{Properties: databaseProps(t, nil)})
	require.NoError(t, err)

	*clock = clock.Add(pendingCreateTimeout + time.Minute)

	// The cluster is probed before the wait is given up on, so one that starts
	// serving on the boundary still finishes rather than being failed anyway.
	client := notServing()
	_, err = testDatabase().statusWithClient(context.Background(), client,
		&resource.StatusRequest{NativeID: nativeID})
	require.Error(t, err)
	assert.Contains(t, err.Error(), testClusterArn)
	assert.Equal(t, []string{"SELECT 1"}, client.statements)

	_, parked := pendingDatabases.peek(nativeID)
	assert.False(t, parked, "an expired wait must not stay parked")
}

// Waiting on a fault that will never clear would poll until the deadline for
// nothing, so only a recoverable fault defers the create.
func TestCreateDoesNotWaitOnAFaultThatWillNotClear(t *testing.T) {
	nativeID := buildNativeID(testClusterArn, testSecretArn, "appdb")
	forgetPending(t, nativeID)

	client := &mockDataAPIClient{}
	client.On("ExecuteStatement", mock.Anything, mock.Anything).
		Return(nil, &rdsdatatypes.AccessDeniedException{Message: strPtr("no")}).Once()

	result, err := testDatabase().createWithClient(context.Background(), client, aurora(t),
		&resource.CreateRequest{Properties: databaseProps(t, nil)})
	require.NoError(t, err)

	assert.Equal(t, resource.OperationStatusFailure, result.ProgressResult.OperationStatus)
	assert.Equal(t, resource.OperationErrorCodeAccessDenied, result.ProgressResult.ErrorCode)

	_, parked := pendingDatabases.peek(nativeID)
	assert.False(t, parked, "a terminal fault must not leave a create waiting")
}

func TestStatusStopsWaitingOnAFaultThatWillNotClear(t *testing.T) {
	nativeID := buildNativeID(testClusterArn, testSecretArn, "appdb")
	forgetPending(t, nativeID)

	_, err := testDatabase().createWithClient(context.Background(), notServing(), aurora(t),
		&resource.CreateRequest{Properties: databaseProps(t, nil)})
	require.NoError(t, err)

	client := &mockDataAPIClient{}
	client.On("ExecuteStatement", mock.Anything, mock.Anything).
		Return(nil, &rdsdatatypes.InvalidSecretException{Message: strPtr("secret is not in the expected format")}).Once()

	result, err := testDatabase().statusWithClient(context.Background(), client,
		&resource.StatusRequest{NativeID: nativeID})
	require.NoError(t, err)

	assert.Equal(t, resource.OperationStatusFailure, result.ProgressResult.OperationStatus)
	assert.Equal(t, resource.OperationErrorCodeInvalidCredentials, result.ProgressResult.ErrorCode)

	_, parked := pendingDatabases.peek(nativeID)
	assert.False(t, parked, "a terminal fault must not leave a create waiting")
}
