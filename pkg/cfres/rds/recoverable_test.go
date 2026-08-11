// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package rds

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/rdsdata"
	rdsdatatypes "github.com/aws/aws-sdk-go-v2/service/rdsdata/types"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// An Aurora cluster reports itself available, with the Data API switched on,
// several minutes before it can actually answer a statement: until the cluster
// has a running DB instance the Data API rejects every call. The plugin only
// ever addresses the "postgres" administrative database, which exists on every
// Aurora PostgreSQL cluster, so this fault can only mean the cluster is not
// serving yet — a condition that clears on its own and must be reported as
// recoverable rather than failing the resource outright.
func clusterNotServing() error {
	return &rdsdatatypes.DatabaseNotFoundException{
		Message: strPtr("Cannot find DBInstance in DBCluster " + testClusterArn),
	}
}

func TestClassifyReportsAClusterThatIsNotServingAsRecoverable(t *testing.T) {
	code, recoverable := classifyDataAPIError(clusterNotServing())

	assert.Equal(t, resource.OperationErrorCodeNotStabilized, code)
	assert.True(t, recoverable)
	assert.True(t, resource.IsRecoverable(code), "the agent must be willing to retry this")
}

// The HTTP endpoint is verified by the create preflight, which names a genuinely
// disabled endpoint before a statement is ever sent. Reaching this exception
// past that check means the endpoint is still coming up with the cluster.
func TestClassifyReportsAnEndpointStillComingUpAsRecoverable(t *testing.T) {
	code, recoverable := classifyDataAPIError(&rdsdatatypes.HttpEndpointNotEnabledException{
		Message: strPtr("HttpEndpoint is not enabled for resource " + testClusterArn),
	})

	assert.Equal(t, resource.OperationErrorCodeNotStabilized, code)
	assert.True(t, recoverable)
	assert.True(t, resource.IsRecoverable(code))
}

// A fault the mapping does not recognise must not be dressed up as a classified
// Data API failure — it stays a plain error, as it is today.
func TestRecognizeDataAPIFaultIgnoresUnrelatedErrors(t *testing.T) {
	_, ok := recognizeDataAPIFault(assert.AnError)
	assert.False(t, ok)
}

func TestDatabaseRoleCreateReportsARecoverableClusterFaultToTheAgent(t *testing.T) {
	client := &mockDataAPIClient{}
	client.On("ExecuteStatement", mock.Anything, mock.Anything).
		Return(nil, clusterNotServing()).Once()

	result, err := testRole().createWithClient(context.Background(), client, aurora(t),
		&resource.CreateRequest{Properties: roleProps(t, nil)})

	// A classified failure is reported through the result, not as a Go error:
	// only the result carries the error code the agent retries on.
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, resource.OperationStatusFailure, result.ProgressResult.OperationStatus)
	assert.Equal(t, resource.OperationErrorCodeNotStabilized, result.ProgressResult.ErrorCode)
	assert.True(t, resource.IsRecoverable(result.ProgressResult.ErrorCode))
	assert.NotEmpty(t, result.ProgressResult.StatusMessage)
}

func TestDatabaseCreateReportsARecoverableClusterFaultToTheAgent(t *testing.T) {
	client := &mockDataAPIClient{}
	client.On("ExecuteStatement", mock.Anything, mock.Anything).
		Return(nil, clusterNotServing()).Once()

	result, err := testDatabase().createWithClient(context.Background(), client, aurora(t),
		&resource.CreateRequest{Properties: databaseProps(t, nil)})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, resource.OperationStatusFailure, result.ProgressResult.OperationStatus)
	assert.Equal(t, resource.OperationErrorCodeNotStabilized, result.ProgressResult.ErrorCode)
	assert.True(t, resource.IsRecoverable(result.ProgressResult.ErrorCode))
}

func TestDatabaseRoleReadReportsARecoverableClusterFaultToTheAgent(t *testing.T) {
	client := &mockDataAPIClient{}
	client.On("ExecuteStatement", mock.Anything, mock.Anything).
		Return(nil, clusterNotServing()).Once()

	result, err := testRole().readWithClient(context.Background(), client,
		&resource.ReadRequest{NativeID: buildNativeID(testClusterArn, testSecretArn, "appuser")})

	// ReadResult carries the code directly — it has no ProgressResult.
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, resource.OperationErrorCodeNotStabilized, result.ErrorCode)
	assert.True(t, resource.IsRecoverable(result.ErrorCode))
}

func TestDatabaseRoleDeleteReportsARecoverableClusterFaultToTheAgent(t *testing.T) {
	client := &mockDataAPIClient{}
	client.On("ExecuteStatement", mock.Anything, mock.Anything).
		Return(nil, clusterNotServing()).Once()

	result, err := testRole().deleteWithClient(context.Background(), client,
		&resource.DeleteRequest{NativeID: buildNativeID(testClusterArn, testSecretArn, "appuser")})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, resource.OperationStatusFailure, result.ProgressResult.OperationStatus)
	assert.Equal(t, resource.OperationErrorCodeNotStabilized, result.ProgressResult.ErrorCode)
}

func TestDatabaseDeleteReportsARecoverableClusterFaultToTheAgent(t *testing.T) {
	client := &mockDataAPIClient{}
	client.On("ExecuteStatement", mock.Anything, mock.Anything).
		Return(nil, clusterNotServing()).Once()

	result, err := testDatabase().deleteWithClient(context.Background(), client,
		&resource.DeleteRequest{NativeID: buildNativeID(testClusterArn, testSecretArn, "appdb")})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, resource.OperationStatusFailure, result.ProgressResult.OperationStatus)
	assert.Equal(t, resource.OperationErrorCodeNotStabilized, result.ProgressResult.ErrorCode)
}

// A terminal fault must keep failing terminally: reporting everything as
// recoverable would make a genuinely broken resource retry until it timed out.
func TestDatabaseRoleCreateKeepsATerminalFaultTerminal(t *testing.T) {
	client := &mockDataAPIClient{}
	client.On("ExecuteStatement", mock.Anything, mock.Anything).
		Return(nil, &rdsdatatypes.AccessDeniedException{Message: strPtr("no")}).Once()

	result, err := testRole().createWithClient(context.Background(), client, aurora(t),
		&resource.CreateRequest{Properties: roleProps(t, nil)})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, resource.OperationStatusFailure, result.ProgressResult.OperationStatus)
	assert.Equal(t, resource.OperationErrorCodeAccessDenied, result.ProgressResult.ErrorCode)
	assert.False(t, resource.IsRecoverable(result.ProgressResult.ErrorCode))
}

// Input the plugin rejects before it reaches AWS is a caller error, not a Data
// API fault, and must keep surfacing as a plain Go error.
func TestCreateStillReturnsPlainErrorsForBadInput(t *testing.T) {
	client := &mockDataAPIClient{}

	_, err := testRole().createWithClient(context.Background(), client, aurora(t),
		&resource.CreateRequest{Properties: roleProps(t, map[string]any{"RoleName": "bad name!"})})

	require.Error(t, err)
	assert.Empty(t, client.statements, "nothing may reach the wire")
}

// The classified failure path must honour the same secret floor as the error
// path it replaces: a fault reported while a verifier-carrying statement is in
// flight must not carry the password or the verifier into the status message.
func TestClassifiedFailureNeverLeaksPasswordOrVerifier(t *testing.T) {
	client := &mockDataAPIClient{}
	client.On("ExecuteStatement", mock.Anything, mock.Anything).
		Return(emptyRoleCatalog(t), nil).Once()
	client.On("ExecuteStatement", mock.Anything, mock.Anything).
		Return(nil, &rdsdatatypes.DatabaseErrorException{
			Message: strPtr("ERROR: something went wrong; SQLState: 58000"),
		}).Once()

	result, err := testRole().createWithClient(context.Background(), client, aurora(t),
		&resource.CreateRequest{Properties: roleProps(t, nil)})

	var reported string
	if err != nil {
		reported = err.Error()
	} else {
		require.NotNil(t, result)
		reported = result.ProgressResult.StatusMessage
		var props map[string]any
		if len(result.ProgressResult.ResourceProperties) > 0 {
			require.NoError(t, json.Unmarshal(result.ProgressResult.ResourceProperties, &props))
			assert.NotContains(t, props, "Password")
		}
	}

	assert.NotContains(t, reported, testPassword, "the plaintext password must never be reported")
	verifier := extractVerifier(t, client.statements)
	if verifier != "" {
		assert.NotContains(t, reported, verifier, "the verifier must never be reported")
	}
}

// Guards the shape the whole fix depends on: a recoverable code reported through
// a ProgressResult is what the agent retries. Returning the same condition as a
// Go error yields no code at all, which is why every Data API fault has to come
// back through the result.
func TestRecoverableCodesAreTheOnesTheAgentRetries(t *testing.T) {
	for _, code := range []resource.OperationErrorCode{
		resource.OperationErrorCodeNotStabilized,
		resource.OperationErrorCodeServiceInternalError,
		resource.OperationErrorCodeServiceTimeout,
	} {
		assert.True(t, resource.IsRecoverable(code), "%s must be retried", code)
	}
	assert.False(t, resource.IsRecoverable(resource.OperationErrorCodeNotSet),
		"an unclassified failure is terminal, so a bare error is never retried")
}

func TestDatabaseRoleCreateSucceedsUnchangedWhenTheClusterIsServing(t *testing.T) {
	client := &mockDataAPIClient{}
	client.On("ExecuteStatement", mock.Anything, mock.Anything).
		Return(emptyRoleCatalog(t), nil).Once()
	client.On("ExecuteStatement", mock.Anything, mock.Anything).
		Return(&rdsdata.ExecuteStatementOutput{}, nil).Once()
	client.On("ExecuteStatement", mock.Anything, mock.Anything).
		Return(existingRoleCatalog(t, true), nil).Once()

	result, err := testRole().createWithClient(context.Background(), client, aurora(t),
		&resource.CreateRequest{Properties: roleProps(t, nil)})

	require.NoError(t, err)
	assert.Equal(t, resource.OperationStatusSuccess, result.ProgressResult.OperationStatus)
	assert.Empty(t, result.ProgressResult.ErrorCode)
	assert.True(t, strings.HasPrefix(client.statements[1], `CREATE ROLE "appuser" LOGIN PASSWORD '`))
}
