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

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	rdstypes "github.com/aws/aws-sdk-go-v2/service/rds/types"
	"github.com/aws/aws-sdk-go-v2/service/rdsdata"
	rdsdatatypes "github.com/aws/aws-sdk-go-v2/service/rdsdata/types"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/config"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func testDatabase() *Database {
	return &Database{cfg: &config.Config{Region: testRegion}}
}

func databaseProps(t *testing.T, overrides map[string]any) json.RawMessage {
	t.Helper()
	props := map[string]any{
		"ClusterArn":     testClusterArn,
		"AdminSecretArn": testSecretArn,
		"DatabaseName":   "appdb",
		"Owner":          "appuser",
	}
	for k, v := range overrides {
		props[k] = v
	}
	raw, err := json.Marshal(props)
	require.NoError(t, err)
	return raw
}

// aurora returns a cluster client reporting a Data API-enabled Aurora
// PostgreSQL cluster.
func aurora(t *testing.T) *mockRDSClusterClient {
	t.Helper()
	clusters := &mockRDSClusterClient{}
	clusters.On("DescribeDBClusters", mock.Anything, mock.Anything).
		Return(&rds.DescribeDBClustersOutput{
			DBClusters: []rdstypes.DBCluster{{
				Engine:              aws.String("aurora-postgresql"),
				HttpEndpointEnabled: aws.Bool(true),
			}},
		}, nil)
	return clusters
}

func membershipHeld(t *testing.T, held bool) *rdsdata.ExecuteStatementOutput {
	return recordsOutput(t, []map[string]any{{"has_role": held}})
}

func emptyDatabaseCatalog(t *testing.T) *rdsdata.ExecuteStatementOutput {
	return recordsOutput(t, []map[string]any{})
}

func existingDatabaseCatalog(t *testing.T, owner string) *rdsdata.ExecuteStatementOutput {
	return recordsOutput(t, []map[string]any{{"datname": "appdb", "rolname": owner}})
}

// servingProbe answers the readiness probe every create starts with.
func servingProbe(client *mockDataAPIClient) {
	client.On("ExecuteStatement", mock.Anything, mock.Anything).Return(&rdsdata.ExecuteStatementOutput{}, nil).Once()
}

func TestDatabaseCreateGrantsMembershipThenCreates(t *testing.T) {
	client := &mockDataAPIClient{}
	servingProbe(client)
	// catalog probe → absent
	client.On("ExecuteStatement", mock.Anything, mock.Anything).Return(emptyDatabaseCatalog(t), nil).Once()
	// membership probe → not a member
	client.On("ExecuteStatement", mock.Anything, mock.Anything).Return(membershipHeld(t, false), nil).Once()
	// GRANT
	client.On("ExecuteStatement", mock.Anything, mock.Anything).Return(&rdsdata.ExecuteStatementOutput{}, nil).Once()
	// CREATE DATABASE
	client.On("ExecuteStatement", mock.Anything, mock.Anything).Return(&rdsdata.ExecuteStatementOutput{}, nil).Once()
	// read-back
	client.On("ExecuteStatement", mock.Anything, mock.Anything).Return(existingDatabaseCatalog(t, "appuser"), nil).Once()

	result, err := testDatabase().createWithClient(context.Background(), client, aurora(t),
		&resource.CreateRequest{Properties: databaseProps(t, nil)})
	require.NoError(t, err)

	assert.Equal(t, resource.OperationStatusSuccess, result.ProgressResult.OperationStatus)
	assert.Equal(t, buildNativeID(testClusterArn, testSecretArn, "appdb"), result.ProgressResult.NativeID)
	assert.Equal(t, `GRANT "appuser" TO CURRENT_USER`, client.statements[3])
	assert.Equal(t, `CREATE DATABASE "appdb" OWNER "appuser"`, client.statements[4])
}

func TestDatabaseCreateSkipsTheGrantWhenMembershipIsAlreadyHeld(t *testing.T) {
	client := &mockDataAPIClient{}
	servingProbe(client)
	client.On("ExecuteStatement", mock.Anything, mock.Anything).Return(emptyDatabaseCatalog(t), nil).Once()
	client.On("ExecuteStatement", mock.Anything, mock.Anything).Return(membershipHeld(t, true), nil).Once()
	client.On("ExecuteStatement", mock.Anything, mock.Anything).Return(&rdsdata.ExecuteStatementOutput{}, nil).Once()
	client.On("ExecuteStatement", mock.Anything, mock.Anything).Return(existingDatabaseCatalog(t, "appuser"), nil).Once()

	_, err := testDatabase().createWithClient(context.Background(), client, aurora(t),
		&resource.CreateRequest{Properties: databaseProps(t, nil)})
	require.NoError(t, err)

	assert.NotContains(t, strings.Join(client.statements, "\n"), "GRANT",
		"membership already held must not emit a GRANT")
}

func TestDatabaseCreateStopsWhenTheGrantFails(t *testing.T) {
	client := &mockDataAPIClient{}
	servingProbe(client)
	client.On("ExecuteStatement", mock.Anything, mock.Anything).Return(emptyDatabaseCatalog(t), nil).Once()
	client.On("ExecuteStatement", mock.Anything, mock.Anything).Return(membershipHeld(t, false), nil).Once()
	client.On("ExecuteStatement", mock.Anything, mock.Anything).
		Return(nil, &rdsdatatypes.BadRequestException{
			Message: strPtr(`ERROR: must have admin option on role "appuser"; SQLState: 42501`),
		}).Once()

	result, err := testDatabase().createWithClient(context.Background(), client, aurora(t),
		&resource.CreateRequest{Properties: databaseProps(t, nil)})
	// The engine rejected the grant, so the failure comes back classified on the
	// result; the diagnosis travels in the status message.
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, resource.OperationStatusFailure, result.ProgressResult.OperationStatus)
	assert.Contains(t, result.ProgressResult.StatusMessage, "appuser")
	assert.False(t, resource.IsRecoverable(result.ProgressResult.ErrorCode),
		"a refused grant is not going to clear on a retry")
	assert.NotContains(t, strings.Join(client.statements, "\n"), "CREATE DATABASE",
		"no DDL may be attempted once the ownership grant fails")
}

func TestDatabaseCreateAdoptsADatabaseWithTheSameOwner(t *testing.T) {
	client := &mockDataAPIClient{}
	servingProbe(client)
	client.On("ExecuteStatement", mock.Anything, mock.Anything).Return(existingDatabaseCatalog(t, "appuser"), nil).Once()
	client.On("ExecuteStatement", mock.Anything, mock.Anything).Return(existingDatabaseCatalog(t, "appuser"), nil).Once()

	result, err := testDatabase().createWithClient(context.Background(), client, aurora(t),
		&resource.CreateRequest{Properties: databaseProps(t, nil)})
	require.NoError(t, err)

	assert.Equal(t, resource.OperationStatusSuccess, result.ProgressResult.OperationStatus)
	assert.Equal(t, buildNativeID(testClusterArn, testSecretArn, "appdb"), result.ProgressResult.NativeID)

	joined := strings.Join(client.statements, "\n")
	assert.NotContains(t, joined, "CREATE DATABASE")
	assert.NotContains(t, joined, "pg_has_role",
		"an adopted database is already owned correctly, so no membership work is needed")
}

// A name collision must leave nothing behind: the membership grant is permanent,
// so it may only be attempted once the create is known to be going ahead.
func TestDatabaseCreateRefusesADatabaseOwnedBySomeoneElse(t *testing.T) {
	client := &mockDataAPIClient{}
	servingProbe(client)
	client.On("ExecuteStatement", mock.Anything, mock.Anything).Return(existingDatabaseCatalog(t, "someone-else"), nil).Once()

	result, err := testDatabase().createWithClient(context.Background(), client, aurora(t),
		&resource.CreateRequest{Properties: databaseProps(t, nil)})
	require.NoError(t, err)

	assert.Equal(t, resource.OperationStatusFailure, result.ProgressResult.OperationStatus)
	assert.Equal(t, resource.OperationErrorCodeAlreadyExists, result.ProgressResult.ErrorCode,
		"a genuine name collision must never be silently absorbed")

	joined := strings.Join(client.statements, "\n")
	assert.NotContains(t, joined, "GRANT",
		"a failed create must not leave the admin permanently added to the owner role")
	assert.NotContains(t, joined, "pg_has_role",
		"the collision is decided from the catalog alone, before any membership work")
}

// A raced create must re-read the catalog rather than assume the winner agrees
// with us: a database that appeared with a different owner is still a collision.
func TestDatabaseCreateRefusesARacedDatabaseWithAnotherOwner(t *testing.T) {
	client := &mockDataAPIClient{}
	servingProbe(client)
	client.On("ExecuteStatement", mock.Anything, mock.Anything).Return(emptyDatabaseCatalog(t), nil).Once()
	client.On("ExecuteStatement", mock.Anything, mock.Anything).Return(membershipHeld(t, true), nil).Once()
	client.On("ExecuteStatement", mock.Anything, mock.Anything).
		Return(nil, &rdsdatatypes.BadRequestException{
			Message: strPtr(`ERROR: database "appdb" already exists; SQLState: 42P04`),
		}).Once()
	client.On("ExecuteStatement", mock.Anything, mock.Anything).Return(existingDatabaseCatalog(t, "someone-else"), nil).Once()

	result, err := testDatabase().createWithClient(context.Background(), client, aurora(t),
		&resource.CreateRequest{Properties: databaseProps(t, nil)})
	require.NoError(t, err)
	assert.Equal(t, resource.OperationErrorCodeAlreadyExists, result.ProgressResult.ErrorCode)
}

func TestDatabaseCreateRejectsANonAuroraPostgresCluster(t *testing.T) {
	tests := []struct {
		name    string
		cluster rdstypes.DBCluster
		wantErr string
	}{
		{
			name:    "mysql engine",
			cluster: rdstypes.DBCluster{Engine: aws.String("aurora-mysql"), HttpEndpointEnabled: aws.Bool(true)},
			wantErr: "aurora-postgresql",
		},
		{
			name:    "data api disabled",
			cluster: rdstypes.DBCluster{Engine: aws.String("aurora-postgresql"), HttpEndpointEnabled: aws.Bool(false)},
			wantErr: "Data API",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clusters := &mockRDSClusterClient{}
			clusters.On("DescribeDBClusters", mock.Anything, mock.Anything).
				Return(&rds.DescribeDBClustersOutput{DBClusters: []rdstypes.DBCluster{tt.cluster}}, nil)

			client := &mockDataAPIClient{}
			_, err := testDatabase().createWithClient(context.Background(), client, clusters,
				&resource.CreateRequest{Properties: databaseProps(t, nil)})
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
			assert.Empty(t, client.statements, "the preflight must fail before any statement is sent")
		})
	}
}

func TestDatabaseCreateValidatesIdentifiersBeforeReachingTheWire(t *testing.T) {
	for _, tt := range []struct {
		name      string
		overrides map[string]any
	}{
		{"database name", map[string]any{"DatabaseName": `a"; DROP DATABASE x --`}},
		{"owner", map[string]any{"Owner": "bad owner"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			client := &mockDataAPIClient{}
			_, err := testDatabase().createWithClient(context.Background(), client, aurora(t),
				&resource.CreateRequest{Properties: databaseProps(t, tt.overrides)})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "identifier")
			assert.Empty(t, client.statements)
		})
	}
}

func TestDatabaseReadMapsOwnerFromTheCatalogJoin(t *testing.T) {
	client := &mockDataAPIClient{}
	client.On("ExecuteStatement", mock.Anything, mock.Anything).
		Return(existingDatabaseCatalog(t, "appuser"), nil).Once()

	nativeID := buildNativeID(testClusterArn, testSecretArn, "appdb")
	result, err := testDatabase().readWithClient(context.Background(), client,
		&resource.ReadRequest{NativeID: nativeID})
	require.NoError(t, err)

	var props map[string]any
	require.NoError(t, json.Unmarshal([]byte(result.Properties), &props))
	assert.Equal(t, testClusterArn, props["ClusterArn"])
	assert.Equal(t, testSecretArn, props["AdminSecretArn"])
	assert.Equal(t, "appdb", props["DatabaseName"])
	assert.Equal(t, "appuser", props["Owner"])

	input := client.Calls[0].Arguments.Get(1).(*rdsdata.ExecuteStatementInput)
	require.Len(t, input.Parameters, 1)
	assert.Equal(t, "name", *input.Parameters[0].Name)
}

func TestDatabaseReadReportsNotFoundOnZeroRows(t *testing.T) {
	client := &mockDataAPIClient{}
	client.On("ExecuteStatement", mock.Anything, mock.Anything).Return(emptyDatabaseCatalog(t), nil).Once()

	result, err := testDatabase().readWithClient(context.Background(), client,
		&resource.ReadRequest{NativeID: buildNativeID(testClusterArn, testSecretArn, "appdb")})
	require.NoError(t, err)
	assert.Equal(t, resource.OperationErrorCodeNotFound, result.ErrorCode)
}

func TestDatabaseUpdateReassignsTheOwner(t *testing.T) {
	client := &mockDataAPIClient{}
	client.On("ExecuteStatement", mock.Anything, mock.Anything).Return(membershipHeld(t, true), nil).Once()
	client.On("ExecuteStatement", mock.Anything, mock.Anything).Return(&rdsdata.ExecuteStatementOutput{}, nil).Once()
	client.On("ExecuteStatement", mock.Anything, mock.Anything).Return(existingDatabaseCatalog(t, "newowner"), nil).Once()

	nativeID := buildNativeID(testClusterArn, testSecretArn, "appdb")
	result, err := testDatabase().updateWithClient(context.Background(), client, &resource.UpdateRequest{
		NativeID:          nativeID,
		PriorProperties:   databaseProps(t, nil),
		DesiredProperties: databaseProps(t, map[string]any{"Owner": "newowner"}),
	})
	require.NoError(t, err)

	assert.Equal(t, `ALTER DATABASE "appdb" OWNER TO "newowner"`, client.statements[1])
	assert.Equal(t, nativeID, result.ProgressResult.NativeID)
}

func TestDatabaseUpdateProvesANewAdminSecretBeforeRewritingTheNativeID(t *testing.T) {
	newSecret := "arn:aws:secretsmanager:us-east-1:123456789012:secret:formae-test-new456"

	client := &mockDataAPIClient{}
	client.On("ExecuteStatement", mock.Anything, mock.Anything).Return(&rdsdata.ExecuteStatementOutput{}, nil).Once()
	client.On("ExecuteStatement", mock.Anything, mock.Anything).Return(existingDatabaseCatalog(t, "appuser"), nil).Once()

	result, err := testDatabase().updateWithClient(context.Background(), client, &resource.UpdateRequest{
		NativeID:          buildNativeID(testClusterArn, testSecretArn, "appdb"),
		PriorProperties:   databaseProps(t, nil),
		DesiredProperties: databaseProps(t, map[string]any{"AdminSecretArn": newSecret}),
	})
	require.NoError(t, err)

	probe := client.Calls[0].Arguments.Get(1).(*rdsdata.ExecuteStatementInput)
	assert.Equal(t, newSecret, *probe.SecretArn)
	assert.Equal(t, buildNativeID(testClusterArn, newSecret, "appdb"), result.ProgressResult.NativeID)
}

func TestDatabaseDeleteDropsTheDatabase(t *testing.T) {
	client := &mockDataAPIClient{}
	client.On("ExecuteStatement", mock.Anything, mock.Anything).Return(&rdsdata.ExecuteStatementOutput{}, nil).Once()

	result, err := testDatabase().deleteWithClient(context.Background(), client,
		&resource.DeleteRequest{NativeID: buildNativeID(testClusterArn, testSecretArn, "appdb")})
	require.NoError(t, err)

	assert.Equal(t, resource.OperationStatusSuccess, result.ProgressResult.OperationStatus)
	assert.Equal(t, []string{`DROP DATABASE "appdb"`}, client.statements)
}

// FORCE terminates live sessions, so it is only ever a response to the engine
// reporting the database in use — never the first attempt, and never a loop.
func TestDatabaseDeleteRetriesExactlyOnceWithForceWhenInUse(t *testing.T) {
	client := &mockDataAPIClient{}
	client.On("ExecuteStatement", mock.Anything, mock.Anything).
		Return(nil, &rdsdatatypes.BadRequestException{
			Message: strPtr(`ERROR: database "appdb" is being accessed by other users; SQLState: 55006`),
		}).Once()
	client.On("ExecuteStatement", mock.Anything, mock.Anything).Return(&rdsdata.ExecuteStatementOutput{}, nil).Once()

	result, err := testDatabase().deleteWithClient(context.Background(), client,
		&resource.DeleteRequest{NativeID: buildNativeID(testClusterArn, testSecretArn, "appdb")})
	require.NoError(t, err)

	assert.Equal(t, resource.OperationStatusSuccess, result.ProgressResult.OperationStatus)
	assert.Equal(t, []string{
		`DROP DATABASE "appdb"`,
		`DROP DATABASE "appdb" WITH (FORCE)`,
	}, client.statements)
}

func TestDatabaseDeleteDoesNotForceOnOtherErrors(t *testing.T) {
	client := &mockDataAPIClient{}
	client.On("ExecuteStatement", mock.Anything, mock.Anything).
		Return(nil, &rdsdatatypes.BadRequestException{
			Message: strPtr(`ERROR: permission denied to drop database; SQLState: 42501`),
		}).Once()

	result, err := testDatabase().deleteWithClient(context.Background(), client,
		&resource.DeleteRequest{NativeID: buildNativeID(testClusterArn, testSecretArn, "appdb")})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, resource.OperationStatusFailure, result.ProgressResult.OperationStatus)
	assert.Equal(t, []string{`DROP DATABASE "appdb"`}, client.statements,
		"FORCE must never follow an error that is not an in-use error")
}

func TestDatabaseDeleteIsIdempotent(t *testing.T) {
	client := &mockDataAPIClient{}
	client.On("ExecuteStatement", mock.Anything, mock.Anything).
		Return(nil, &rdsdatatypes.BadRequestException{
			Message: strPtr(`ERROR: database "appdb" does not exist; SQLState: 3D000`),
		}).Once()

	result, err := testDatabase().deleteWithClient(context.Background(), client,
		&resource.DeleteRequest{NativeID: buildNativeID(testClusterArn, testSecretArn, "appdb")})
	require.NoError(t, err)
	assert.Equal(t, resource.OperationStatusSuccess, result.ProgressResult.OperationStatus)
}

func TestDatabaseListIsUnsupported(t *testing.T) {
	_, err := testDatabase().List(context.Background(), &resource.ListRequest{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not supported")
	assert.Contains(t, err.Error(), databaseType)
}
