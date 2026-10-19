// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package rds

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	rdstypes "github.com/aws/aws-sdk-go-v2/service/rds/types"
	"github.com/aws/aws-sdk-go-v2/service/rdsdata"
	rdsdatatypes "github.com/aws/aws-sdk-go-v2/service/rdsdata/types"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/config"
	"github.com/platform-engineering-labs/formae/pkg/plugin"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

const testPassword = "aB3$xY7!kL9#mN2%"

func testRole() *DatabaseRole {
	return &DatabaseRole{cfg: &config.Config{Region: testRegion}}
}

func roleProps(t *testing.T, overrides map[string]any) json.RawMessage {
	t.Helper()
	props := map[string]any{
		"ClusterArn":     testClusterArn,
		"AdminSecretArn": testSecretArn,
		"RoleName":       "appuser",
		"Password":       testPassword,
		"CanLogin":       true,
	}
	for k, v := range overrides {
		if v == nil {
			delete(props, k)
			continue
		}
		props[k] = v
	}
	raw, err := json.Marshal(props)
	require.NoError(t, err)
	return raw
}

// recordsOutput builds a Data API result set in the JSON record format.
func recordsOutput(t *testing.T, records []map[string]any) *rdsdata.ExecuteStatementOutput {
	t.Helper()
	encoded, err := json.Marshal(records)
	require.NoError(t, err)
	formatted := string(encoded)
	return &rdsdata.ExecuteStatementOutput{FormattedRecords: &formatted}
}

func emptyRoleCatalog(t *testing.T) *rdsdata.ExecuteStatementOutput {
	return recordsOutput(t, []map[string]any{})
}

func existingRoleCatalog(t *testing.T, canLogin bool) *rdsdata.ExecuteStatementOutput {
	return recordsOutput(t, []map[string]any{{"rolname": "appuser", "rolcanlogin": canLogin}})
}

func TestDatabaseRoleCreateIssuesCreateRoleWhenAbsent(t *testing.T) {
	client := &mockDataAPIClient{}
	servingProbe(client)
	// catalog probe → absent, CREATE ROLE, then the read-back probe
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
	assert.Equal(t, buildNativeID(testClusterArn, testSecretArn, "appuser"), result.ProgressResult.NativeID)

	create := client.statements[2]
	// Asserted as a prefix, not a substring: "LOGIN" is also a substring of
	// "NOLOGIN", so a substring check could not tell the two apart.
	assert.True(t, strings.HasPrefix(create, `CREATE ROLE "appuser" LOGIN PASSWORD '`), "got: %s", create)
	assert.Contains(t, create, "SCRAM-SHA-256$4096:")
	assert.NotContains(t, create, testPassword, "the plaintext password must never reach the SQL text")

	// The read-back must not hand the password back to the agent.
	var props map[string]any
	require.NoError(t, json.Unmarshal(result.ProgressResult.ResourceProperties, &props))
	assert.NotContains(t, props, "Password")
	assert.Equal(t, "appuser", props["RoleName"])
	assert.Equal(t, true, props["CanLogin"])
}

func TestDatabaseRoleCreateAdoptsAnExistingRole(t *testing.T) {
	client := &mockDataAPIClient{}
	servingProbe(client)
	client.On("ExecuteStatement", mock.Anything, mock.Anything).
		Return(existingRoleCatalog(t, false), nil).Once()
	client.On("ExecuteStatement", mock.Anything, mock.Anything).
		Return(&rdsdata.ExecuteStatementOutput{}, nil).Times(2)
	client.On("ExecuteStatement", mock.Anything, mock.Anything).
		Return(existingRoleCatalog(t, true), nil).Once()

	result, err := testRole().createWithClient(context.Background(), client, aurora(t),
		&resource.CreateRequest{Properties: roleProps(t, nil)})
	require.NoError(t, err)

	assert.Equal(t, buildNativeID(testClusterArn, testSecretArn, "appuser"), result.ProgressResult.NativeID)
	joined := strings.Join(client.statements, "\n")
	assert.NotContains(t, joined, "CREATE ROLE", "an existing role is adopted, not recreated")
	// The declared state is re-asserted: the password cannot be compared, and
	// LOGIN may have drifted out of band.
	assert.Contains(t, joined, `ALTER ROLE "appuser" PASSWORD`)
	assert.Equal(t, `ALTER ROLE "appuser" LOGIN`, client.statements[3])
}

// A Data API call can commit and lose its response, so a CREATE can fail with
// "already exists" even though the catalog probe said the role was absent.
func TestDatabaseRoleCreateAdoptsARacedRole(t *testing.T) {
	client := &mockDataAPIClient{}
	servingProbe(client)
	client.On("ExecuteStatement", mock.Anything, mock.Anything).
		Return(emptyRoleCatalog(t), nil).Once()
	client.On("ExecuteStatement", mock.Anything, mock.Anything).
		Return(nil, &rdsdatatypes.BadRequestException{
			Message: strPtr(`ERROR: role "appuser" already exists; SQLState: 42710`),
		}).Once()
	client.On("ExecuteStatement", mock.Anything, mock.Anything).
		Return(&rdsdata.ExecuteStatementOutput{}, nil).Times(2)
	client.On("ExecuteStatement", mock.Anything, mock.Anything).
		Return(existingRoleCatalog(t, true), nil).Once()

	result, err := testRole().createWithClient(context.Background(), client, aurora(t),
		&resource.CreateRequest{Properties: roleProps(t, nil)})
	require.NoError(t, err)
	assert.Equal(t, resource.OperationStatusSuccess, result.ProgressResult.OperationStatus)
}

func TestDatabaseRoleCreateValidatesBeforeReachingTheWire(t *testing.T) {
	tests := []struct {
		name      string
		overrides map[string]any
		wantErr   string
	}{
		{"invalid role name", map[string]any{"RoleName": `a"; DROP DATABASE x --`}, "identifier"},
		{"non ascii password", map[string]any{"Password": "café"}, "U+0020"},
		{"cluster in another region", map[string]any{
			"ClusterArn": "arn:aws:rds:eu-west-1:123456789012:cluster:other",
		}, "region"},
		{"secret in another account", map[string]any{
			"AdminSecretArn": "arn:aws:secretsmanager:us-east-1:999988887777:secret:other-abc123",
		}, "account"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &mockDataAPIClient{}
			_, err := testRole().createWithClient(context.Background(), client, aurora(t),
				&resource.CreateRequest{Properties: roleProps(t, tt.overrides)})
			require.Error(t, err)
			assert.Contains(t, strings.ToLower(err.Error()), strings.ToLower(tt.wantErr))
			assert.Empty(t, client.statements, "nothing may reach the wire once validation fails")
		})
	}
}

// The role is as Aurora-PostgreSQL-only as the database is, so an unsupported
// engine must be named outright rather than surfacing as a stray SQL error from
// a catalog query no MySQL cluster can answer.
func TestDatabaseRoleCreateRejectsANonAuroraPostgresCluster(t *testing.T) {
	for _, tt := range []struct {
		name    string
		cluster rdstypes.DBCluster
		wantErr string
	}{
		{
			name:    "wrong engine",
			cluster: rdstypes.DBCluster{Engine: aws.String("aurora-mysql"), HttpEndpointEnabled: aws.Bool(true)},
			wantErr: "aurora-postgresql",
		},
		{
			name:    "data api disabled",
			cluster: rdstypes.DBCluster{Engine: aws.String("aurora-postgresql"), HttpEndpointEnabled: aws.Bool(false)},
			wantErr: "Data API",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			clusters := &mockRDSClusterClient{}
			clusters.On("DescribeDBClusters", mock.Anything, mock.Anything).
				Return(&rds.DescribeDBClustersOutput{DBClusters: []rdstypes.DBCluster{tt.cluster}}, nil)

			client := &mockDataAPIClient{}
			_, err := testRole().createWithClient(context.Background(), client, clusters,
				&resource.CreateRequest{Properties: roleProps(t, nil)})
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
			assert.Empty(t, client.statements, "the preflight must fail before any statement is sent")
		})
	}
}

func TestDatabaseRoleReadMapsCatalogRow(t *testing.T) {
	client := &mockDataAPIClient{}
	client.On("ExecuteStatement", mock.Anything, mock.Anything).
		Return(existingRoleCatalog(t, false), nil).Once()

	nativeID := buildNativeID(testClusterArn, testSecretArn, "appuser")
	result, err := testRole().readWithClient(context.Background(), client,
		&resource.ReadRequest{NativeID: nativeID})
	require.NoError(t, err)
	assert.Empty(t, result.ErrorCode)

	var props map[string]any
	require.NoError(t, json.Unmarshal([]byte(result.Properties), &props))
	assert.Equal(t, testClusterArn, props["ClusterArn"])
	assert.Equal(t, testSecretArn, props["AdminSecretArn"])
	assert.Equal(t, "appuser", props["RoleName"])
	assert.Equal(t, false, props["CanLogin"])
	assert.NotContains(t, props, "Password", "the catalog stores a salted verifier; there is no password to report")

	// The catalog lookup binds the name rather than interpolating it.
	input := client.Calls[0].Arguments.Get(1).(*rdsdata.ExecuteStatementInput)
	require.Len(t, input.Parameters, 1)
	assert.Equal(t, "name", *input.Parameters[0].Name)
}

func TestDatabaseRoleReadReportsNotFoundOnZeroRows(t *testing.T) {
	client := &mockDataAPIClient{}
	client.On("ExecuteStatement", mock.Anything, mock.Anything).
		Return(emptyRoleCatalog(t), nil).Once()

	result, err := testRole().readWithClient(context.Background(), client,
		&resource.ReadRequest{NativeID: buildNativeID(testClusterArn, testSecretArn, "appuser")})
	require.NoError(t, err)
	assert.Equal(t, resource.OperationErrorCodeNotFound, result.ErrorCode)
}

// priorRoleProps is what the agent really sends as prior state: the password is
// opaque, so its prior value is redacted to an unusable non-scalar sentinel.
func priorRoleProps(t *testing.T, overrides map[string]any) json.RawMessage {
	t.Helper()
	props := map[string]any{"Password": map[string]any{"$opaque": "redacted"}}
	for k, v := range overrides {
		props[k] = v
	}
	return roleProps(t, props)
}

func patchDoc(t *testing.T, ops []map[string]any) *string {
	t.Helper()
	encoded, err := json.Marshal(ops)
	require.NoError(t, err)
	document := string(encoded)
	return &document
}

func TestDatabaseRoleUpdateRotatesThePassword(t *testing.T) {
	client := &mockDataAPIClient{}
	client.On("ExecuteStatement", mock.Anything, mock.Anything).
		Return(&rdsdata.ExecuteStatementOutput{}, nil).Once()
	client.On("ExecuteStatement", mock.Anything, mock.Anything).
		Return(existingRoleCatalog(t, true), nil).Once()

	nativeID := buildNativeID(testClusterArn, testSecretArn, "appuser")
	result, err := testRole().updateWithClient(context.Background(), client, &resource.UpdateRequest{
		NativeID:          nativeID,
		PriorProperties:   priorRoleProps(t, nil),
		DesiredProperties: roleProps(t, nil),
		PatchDocument: patchDoc(t, []map[string]any{
			{"op": "replace", "path": "/Password", "value": testPassword},
		}),
	})
	require.NoError(t, err)
	assert.Equal(t, nativeID, result.ProgressResult.NativeID)

	assert.Contains(t, client.statements[0], `ALTER ROLE "appuser" PASSWORD`)
	assert.Contains(t, client.statements[0], "SCRAM-SHA-256$4096:")
	assert.NotContains(t, client.statements[0], testPassword)
}

// The password is write-only, so prior state can never carry it: an opaque
// property's prior value arrives redacted, and reading it at all fails the
// update before any statement is composed.
func TestDatabaseRoleUpdateDoesNotReadTheRedactedPriorPassword(t *testing.T) {
	client := &mockDataAPIClient{}
	client.On("ExecuteStatement", mock.Anything, mock.Anything).
		Return(&rdsdata.ExecuteStatementOutput{}, nil).Once()
	client.On("ExecuteStatement", mock.Anything, mock.Anything).
		Return(existingRoleCatalog(t, true), nil).Once()

	result, err := testRole().updateWithClient(context.Background(), client, &resource.UpdateRequest{
		NativeID:          buildNativeID(testClusterArn, testSecretArn, "appuser"),
		PriorProperties:   priorRoleProps(t, nil),
		DesiredProperties: roleProps(t, nil),
		PatchDocument: patchDoc(t, []map[string]any{
			{"op": "replace", "path": "/Password", "value": testPassword},
		}),
	})
	require.NoError(t, err)
	require.NotNil(t, result.ProgressResult)
	assert.Equal(t, resource.OperationStatusSuccess, result.ProgressResult.OperationStatus)
}

// The patch is the only signal that separates a rotation from an unrelated
// change, since the declared password cannot be compared against prior state.
func TestDatabaseRoleUpdateLeavesThePasswordAloneWhenThePatchDoes(t *testing.T) {
	client := &mockDataAPIClient{}
	client.On("ExecuteStatement", mock.Anything, mock.Anything).
		Return(&rdsdata.ExecuteStatementOutput{}, nil).Once()
	client.On("ExecuteStatement", mock.Anything, mock.Anything).
		Return(existingRoleCatalog(t, false), nil).Once()

	nativeID := buildNativeID(testClusterArn, testSecretArn, "appuser")
	_, err := testRole().updateWithClient(context.Background(), client, &resource.UpdateRequest{
		NativeID:          nativeID,
		PriorProperties:   priorRoleProps(t, nil),
		DesiredProperties: roleProps(t, map[string]any{"CanLogin": false}),
		PatchDocument: patchDoc(t, []map[string]any{
			{"op": "replace", "path": "/CanLogin", "value": false},
		}),
	})
	require.NoError(t, err)

	assert.Equal(t, `ALTER ROLE "appuser" NOLOGIN`, client.statements[0])
	assert.NotContains(t, strings.Join(client.statements, "\n"), "PASSWORD",
		"a patch that leaves the password alone must not rewrite it")
}

// A parent-object or whole-document replacement carries the password with it, so
// matching the exact path would silently miss the rotation. The schema's own
// spelling of the property counts too.
func TestDatabaseRoleUpdateAppliesThePasswordForAnOpAboveIt(t *testing.T) {
	for _, tt := range []struct {
		name string
		ops  []map[string]any
	}{
		{"whole document replaced", []map[string]any{{"op": "replace", "path": ""}}},
		{"schema-cased property", []map[string]any{{"op": "replace", "path": "/password"}}},
		{"moved onto the property", []map[string]any{{"op": "move", "from": "/Other", "path": "/Password"}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			client := &mockDataAPIClient{}
			client.On("ExecuteStatement", mock.Anything, mock.Anything).
				Return(&rdsdata.ExecuteStatementOutput{}, nil).Once()
			client.On("ExecuteStatement", mock.Anything, mock.Anything).
				Return(existingRoleCatalog(t, true), nil).Once()

			_, err := testRole().updateWithClient(context.Background(), client, &resource.UpdateRequest{
				NativeID:          buildNativeID(testClusterArn, testSecretArn, "appuser"),
				PriorProperties:   priorRoleProps(t, nil),
				DesiredProperties: roleProps(t, nil),
				PatchDocument:     patchDoc(t, tt.ops),
			})
			require.NoError(t, err)
			assert.Contains(t, client.statements[0], `ALTER ROLE "appuser" PASSWORD`)
		})
	}
}

// A missed rotation is a correctness bug; a re-applied password converges. So an
// unusable signal means apply, not skip.
func TestDatabaseRoleUpdateAppliesThePasswordWithoutAUsableSignal(t *testing.T) {
	empty := ""
	emptyOps := "[]"
	malformed := "{not a patch"
	null := "null"

	for _, tt := range []struct {
		name  string
		patch *string
	}{
		{"absent", nil},
		{"empty string", &empty},
		{"no operations", &emptyOps},
		{"unparseable", &malformed},
		{"json null", &null},
	} {
		t.Run(tt.name, func(t *testing.T) {
			client := &mockDataAPIClient{}
			client.On("ExecuteStatement", mock.Anything, mock.Anything).
				Return(&rdsdata.ExecuteStatementOutput{}, nil).Once()
			client.On("ExecuteStatement", mock.Anything, mock.Anything).
				Return(existingRoleCatalog(t, true), nil).Once()

			_, err := testRole().updateWithClient(context.Background(), client, &resource.UpdateRequest{
				NativeID:          buildNativeID(testClusterArn, testSecretArn, "appuser"),
				PriorProperties:   priorRoleProps(t, nil),
				DesiredProperties: roleProps(t, nil),
				PatchDocument:     tt.patch,
			})
			require.NoError(t, err)
			assert.Contains(t, client.statements[0], `ALTER ROLE "appuser" PASSWORD`)
		})
	}
}

func TestDatabaseRoleUpdateProvesANewAdminSecretBeforeRewritingTheNativeID(t *testing.T) {
	newSecret := "arn:aws:secretsmanager:us-east-1:123456789012:secret:formae-test-new456"

	client := &mockDataAPIClient{}
	client.On("ExecuteStatement", mock.Anything, mock.Anything).
		Return(&rdsdata.ExecuteStatementOutput{}, nil).Once()
	client.On("ExecuteStatement", mock.Anything, mock.Anything).
		Return(existingRoleCatalog(t, true), nil).Once()

	oldNativeID := buildNativeID(testClusterArn, testSecretArn, "appuser")
	result, err := testRole().updateWithClient(context.Background(), client, &resource.UpdateRequest{
		NativeID:          oldNativeID,
		PriorProperties:   priorRoleProps(t, nil),
		DesiredProperties: roleProps(t, map[string]any{"AdminSecretArn": newSecret}),
		PatchDocument: patchDoc(t, []map[string]any{
			{"op": "replace", "path": "/AdminSecretArn", "value": newSecret},
		}),
	})
	require.NoError(t, err)

	// The probe is the first thing on the wire, and it goes through the NEW secret.
	probe := client.Calls[0].Arguments.Get(1).(*rdsdata.ExecuteStatementInput)
	assert.Equal(t, newSecret, *probe.SecretArn)
	assert.Equal(t, buildNativeID(testClusterArn, newSecret, "appuser"), result.ProgressResult.NativeID)
	assert.NotContains(t, strings.Join(client.statements, "\n"), "ALTER ROLE",
		"repointing the admin credential changes nothing about the role itself")
}

func TestDatabaseRoleUpdateKeepsTheOldNativeIDWhenTheNewSecretFails(t *testing.T) {
	newSecret := "arn:aws:secretsmanager:us-east-1:123456789012:secret:formae-test-broken"

	client := &mockDataAPIClient{}
	client.On("ExecuteStatement", mock.Anything, mock.Anything).
		Return(nil, &rdsdatatypes.InvalidSecretException{Message: strPtr("secret is not in the expected format")}).Once()

	oldNativeID := buildNativeID(testClusterArn, testSecretArn, "appuser")
	result, err := testRole().updateWithClient(context.Background(), client, &resource.UpdateRequest{
		NativeID:          oldNativeID,
		PriorProperties:   priorRoleProps(t, nil),
		DesiredProperties: roleProps(t, map[string]any{"AdminSecretArn": newSecret}),
	})
	require.NoError(t, err)
	assert.Equal(t, resource.OperationStatusFailure, result.ProgressResult.OperationStatus)
	assert.Equal(t, oldNativeID, result.ProgressResult.NativeID,
		"the old NativeID still works and must survive a failed credential swap")
}

// unusableRoleProps is desired state whose declared password cannot be read:
// the property is present, but its value is not the type the schema declares.
// The encoding below is one shape of that rather than the shape — what is
// asserted is the type, which is the only thing guaranteed about such a value.
func unusableRoleProps(t *testing.T, overrides map[string]any) json.RawMessage {
	t.Helper()
	props := map[string]any{"Password": map[string]any{"$opaque": "preserved"}}
	for k, v := range overrides {
		props[k] = v
	}
	return roleProps(t, props)
}

// A password that cannot be read is only a problem for an update that writes it.
// Failing on it regardless would freeze the role: neither the login attribute nor
// the admin credential could ever be changed again.
func TestDatabaseRoleUpdateTogglesCanLoginWithAnUnusablePassword(t *testing.T) {
	client := &mockDataAPIClient{}
	client.On("ExecuteStatement", mock.Anything, mock.Anything).
		Return(&rdsdata.ExecuteStatementOutput{}, nil).Once()
	client.On("ExecuteStatement", mock.Anything, mock.Anything).
		Return(existingRoleCatalog(t, false), nil).Once()

	nativeID := buildNativeID(testClusterArn, testSecretArn, "appuser")
	result, err := testRole().updateWithClient(context.Background(), client, &resource.UpdateRequest{
		NativeID:          nativeID,
		PriorProperties:   priorRoleProps(t, nil),
		DesiredProperties: unusableRoleProps(t, map[string]any{"CanLogin": false}),
		PatchDocument: patchDoc(t, []map[string]any{
			{"op": "replace", "path": "/CanLogin", "value": false},
		}),
	})
	require.NoError(t, err)
	assert.Equal(t, resource.OperationStatusSuccess, result.ProgressResult.OperationStatus)

	assert.Equal(t, `ALTER ROLE "appuser" NOLOGIN`, client.statements[0])
	assert.NotContains(t, strings.Join(client.statements, "\n"), "PASSWORD",
		"a password that is not written does not need to be readable")
}

func TestDatabaseRoleUpdateRepointsTheAdminSecretWithAnUnusablePassword(t *testing.T) {
	newSecret := "arn:aws:secretsmanager:us-east-1:123456789012:secret:formae-test-new456"

	client := &mockDataAPIClient{}
	client.On("ExecuteStatement", mock.Anything, mock.Anything).
		Return(&rdsdata.ExecuteStatementOutput{}, nil).Once()
	client.On("ExecuteStatement", mock.Anything, mock.Anything).
		Return(existingRoleCatalog(t, true), nil).Once()

	result, err := testRole().updateWithClient(context.Background(), client, &resource.UpdateRequest{
		NativeID:          buildNativeID(testClusterArn, testSecretArn, "appuser"),
		PriorProperties:   priorRoleProps(t, nil),
		DesiredProperties: unusableRoleProps(t, map[string]any{"AdminSecretArn": newSecret}),
		PatchDocument: patchDoc(t, []map[string]any{
			{"op": "replace", "path": "/AdminSecretArn", "value": newSecret},
		}),
	})
	require.NoError(t, err)

	probe := client.Calls[0].Arguments.Get(1).(*rdsdata.ExecuteStatementInput)
	assert.Equal(t, "SELECT 1", *probe.Sql)
	assert.Equal(t, newSecret, *probe.SecretArn)
	assert.Equal(t, buildNativeID(testClusterArn, newSecret, "appuser"), result.ProgressResult.NativeID)
	assert.NotContains(t, strings.Join(client.statements, "\n"), "PASSWORD",
		"repointing the admin credential does not write the password")
}

// The patch is biased towards yes, so plenty of shapes mean write. When the
// declared password cannot be read, every one of them must fail: writing
// hash-derived material would lock the role's consumer out while the operation
// reported success.
func TestDatabaseRoleUpdateFailsWhenAnUnusablePasswordMustBeWritten(t *testing.T) {
	document := func(body string) *string { return &body }

	for _, tt := range []struct {
		name  string
		patch *string
	}{
		{"an op on the property", document(`[{"op":"replace","path":"/Password","value":"x"}]`)},
		{"the whole document replaced", document(`[{"op":"replace","path":"","value":{}}]`)},
		{"moved onto the property", document(`[{"op":"move","from":"/Other","path":"/Password"}]`)},
		{"copied onto the property", document(`[{"op":"copy","from":"/Other","path":"/Password"}]`)},
		{"copied from the property", document(`[{"op":"copy","from":"/Password","path":"/Other"}]`)},
		{"absent", nil},
		{"empty string", document("")},
		{"no operations", document("[]")},
		{"unparseable", document("{not a patch")},
		{"json null", document("null")},
		{"an empty operation", document(`[{}]`)},
		{"a non-string path", document(`[{"op":"replace","path":7}]`)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			client := &mockDataAPIClient{}
			_, err := testRole().updateWithClient(context.Background(), client, &resource.UpdateRequest{
				NativeID:          buildNativeID(testClusterArn, testSecretArn, "appuser"),
				PriorProperties:   priorRoleProps(t, nil),
				DesiredProperties: unusableRoleProps(t, nil),
				PatchDocument:     tt.patch,
			})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "appuser")
			assert.Empty(t, client.statements,
				"a password that cannot be read must be written as nothing at all")
		})
	}
}

// Proving a replacement credential is read-only and precedes the password, so a
// combined change probes before it fails. Nothing has been written by then.
func TestDatabaseRoleUpdateProbesANewAdminSecretBeforeFailingOnAnUnusablePassword(t *testing.T) {
	newSecret := "arn:aws:secretsmanager:us-east-1:123456789012:secret:formae-test-new456"

	client := &mockDataAPIClient{}
	client.On("ExecuteStatement", mock.Anything, mock.Anything).
		Return(&rdsdata.ExecuteStatementOutput{}, nil).Once()

	_, err := testRole().updateWithClient(context.Background(), client, &resource.UpdateRequest{
		NativeID:          buildNativeID(testClusterArn, testSecretArn, "appuser"),
		PriorProperties:   priorRoleProps(t, nil),
		DesiredProperties: unusableRoleProps(t, map[string]any{"AdminSecretArn": newSecret}),
		PatchDocument: patchDoc(t, []map[string]any{
			{"op": "replace", "path": "/AdminSecretArn", "value": newSecret},
			{"op": "replace", "path": "/Password"},
		}),
	})
	require.Error(t, err)

	require.Len(t, client.statements, 1)
	assert.Equal(t, "SELECT 1", client.statements[0])
	assert.NotContains(t, strings.Join(client.statements, "\n"), "ALTER ROLE",
		"the probe reads; nothing about the role is altered")
}

// The login attribute is written after the password, so a failure on the
// password leaves it alone.
func TestDatabaseRoleUpdateWritesNoLoginAttributeWhenThePasswordCannotBeRead(t *testing.T) {
	client := &mockDataAPIClient{}

	_, err := testRole().updateWithClient(context.Background(), client, &resource.UpdateRequest{
		NativeID:          buildNativeID(testClusterArn, testSecretArn, "appuser"),
		PriorProperties:   priorRoleProps(t, nil),
		DesiredProperties: unusableRoleProps(t, map[string]any{"CanLogin": false}),
		PatchDocument: patchDoc(t, []map[string]any{
			{"op": "replace", "path": "/Password"},
			{"op": "replace", "path": "/CanLogin", "value": false},
		}),
	})
	require.Error(t, err)
	assert.Empty(t, client.statements, "the login attribute must not be written on its own")
}

// What makes a declared password unreadable is that it is not the type the
// schema declares, not any particular encoding of it.
func TestDatabaseRoleUpdateReadsThePasswordByTypeNotByEncoding(t *testing.T) {
	for _, tt := range []struct {
		name  string
		value any
	}{
		{"object", map[string]any{"$opaque": "preserved"}},
		{"array", []any{"preserved"}},
		{"number", 7},
		{"boolean", true},
		{"null", json.RawMessage("null")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			desired := func(overrides map[string]any) json.RawMessage {
				overrides["Password"] = tt.value
				return roleProps(t, overrides)
			}

			t.Run("tolerated when the password is not written", func(t *testing.T) {
				client := &mockDataAPIClient{}
				client.On("ExecuteStatement", mock.Anything, mock.Anything).
					Return(&rdsdata.ExecuteStatementOutput{}, nil).Once()
				client.On("ExecuteStatement", mock.Anything, mock.Anything).
					Return(existingRoleCatalog(t, false), nil).Once()

				_, err := testRole().updateWithClient(context.Background(), client, &resource.UpdateRequest{
					NativeID:          buildNativeID(testClusterArn, testSecretArn, "appuser"),
					PriorProperties:   priorRoleProps(t, nil),
					DesiredProperties: desired(map[string]any{"CanLogin": false}),
					PatchDocument: patchDoc(t, []map[string]any{
						{"op": "replace", "path": "/CanLogin", "value": false},
					}),
				})
				require.NoError(t, err)
				assert.Equal(t, `ALTER ROLE "appuser" NOLOGIN`, client.statements[0])
				assert.NotContains(t, strings.Join(client.statements, "\n"), "PASSWORD")
			})

			t.Run("loud when it must be written", func(t *testing.T) {
				client := &mockDataAPIClient{}
				_, err := testRole().updateWithClient(context.Background(), client, &resource.UpdateRequest{
					NativeID:          buildNativeID(testClusterArn, testSecretArn, "appuser"),
					PriorProperties:   priorRoleProps(t, nil),
					DesiredProperties: desired(map[string]any{}),
					PatchDocument: patchDoc(t, []map[string]any{
						{"op": "replace", "path": "/Password"},
					}),
				})
				require.Error(t, err)
				assert.Empty(t, client.statements)
			})
		})
	}
}

// An absent password is malformed rather than unreadable: the desired document
// is complete, and the property is required.
func TestDatabaseRoleRejectsAnAbsentPassword(t *testing.T) {
	absent := map[string]any{"Password": nil}

	t.Run("create", func(t *testing.T) {
		client := &mockDataAPIClient{}
		_, err := testRole().createWithClient(context.Background(), client, aurora(t),
			&resource.CreateRequest{Properties: roleProps(t, absent)})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "required property Password not found")
		assert.Empty(t, client.statements)
	})

	t.Run("update", func(t *testing.T) {
		client := &mockDataAPIClient{}
		_, err := testRole().updateWithClient(context.Background(), client, &resource.UpdateRequest{
			NativeID:          buildNativeID(testClusterArn, testSecretArn, "appuser"),
			PriorProperties:   priorRoleProps(t, nil),
			DesiredProperties: roleProps(t, absent),
			PatchDocument: patchDoc(t, []map[string]any{
				{"op": "replace", "path": "/CanLogin", "value": false},
			}),
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "required property Password not found",
			"an absent password fails even where an unreadable one is tolerated")
		assert.Empty(t, client.statements)
	})
}

// A create always writes the password, so it demands a readable one up front —
// before the cluster is described, let alone asked to run anything.
func TestDatabaseRoleCreateRejectsAnUnusablePasswordBeforeAnyAWSCall(t *testing.T) {
	client := &mockDataAPIClient{}
	clusters := &mockRDSClusterClient{}

	_, err := testRole().createWithClient(context.Background(), client, clusters,
		&resource.CreateRequest{Properties: unusableRoleProps(t, nil)})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "appuser")

	clusters.AssertNotCalled(t, "DescribeDBClusters", mock.Anything, mock.Anything)
	assert.Empty(t, client.statements)
}

// The declared value of an opaque property is hash-derived material, so it is
// classed as secret alongside the password and the verifier: refusing it must
// not quote it back.
func TestDatabaseRoleUnreadablePasswordIsNeverQuotedBack(t *testing.T) {
	var logged bytes.Buffer
	ctx := plugin.WithLogger(context.Background(),
		plugin.NewPluginLogger(slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelDebug}))))

	const material = "S0LqZmF0ZWtleQ"

	client := &mockDataAPIClient{}
	_, err := testRole().updateWithClient(ctx, client, &resource.UpdateRequest{
		NativeID:          buildNativeID(testClusterArn, testSecretArn, "appuser"),
		PriorProperties:   priorRoleProps(t, nil),
		DesiredProperties: roleProps(t, map[string]any{"Password": map[string]any{"$opaque": material}}),
		PatchDocument: patchDoc(t, []map[string]any{
			{"op": "replace", "path": "/Password"},
		}),
	})
	require.Error(t, err)

	// The role name is not secret and already names the failure elsewhere.
	assert.Contains(t, err.Error(), "appuser")
	assert.NotContains(t, err.Error(), material)
	assert.NotContains(t, err.Error(), "SCRAM-SHA-256$")
	assert.NotContains(t, logged.String(), material)
	assert.NotContains(t, logged.String(), "SCRAM-SHA-256$")
}

func TestDatabaseRoleDeleteDropsTheRole(t *testing.T) {
	client := &mockDataAPIClient{}
	client.On("ExecuteStatement", mock.Anything, mock.Anything).
		Return(&rdsdata.ExecuteStatementOutput{}, nil).Once()

	nativeID := buildNativeID(testClusterArn, testSecretArn, "appuser")
	result, err := testRole().deleteWithClient(context.Background(), client,
		&resource.DeleteRequest{NativeID: nativeID})
	require.NoError(t, err)

	assert.Equal(t, resource.OperationStatusSuccess, result.ProgressResult.OperationStatus)
	assert.Equal(t, `DROP ROLE "appuser"`, client.statements[0])
}

// A committed DROP whose response was lost must not fail the agent's retry.
func TestDatabaseRoleDeleteIsIdempotent(t *testing.T) {
	client := &mockDataAPIClient{}
	client.On("ExecuteStatement", mock.Anything, mock.Anything).
		Return(nil, &rdsdatatypes.BadRequestException{
			Message: strPtr(`ERROR: role "appuser" does not exist; SQLState: 42704`),
		}).Once()

	result, err := testRole().deleteWithClient(context.Background(), client,
		&resource.DeleteRequest{NativeID: buildNativeID(testClusterArn, testSecretArn, "appuser")})
	require.NoError(t, err)
	assert.Equal(t, resource.OperationStatusSuccess, result.ProgressResult.OperationStatus)
}

func TestDatabaseRoleDeleteReportsOwnedObjectsActionably(t *testing.T) {
	client := &mockDataAPIClient{}
	client.On("ExecuteStatement", mock.Anything, mock.Anything).
		Return(nil, &rdsdatatypes.BadRequestException{
			Message: strPtr(`ERROR: role "appuser" cannot be dropped because some objects depend on it; SQLState: 2BP01`),
		}).Once()

	result, err := testRole().deleteWithClient(context.Background(), client,
		&resource.DeleteRequest{NativeID: buildNativeID(testClusterArn, testSecretArn, "appuser")})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, resource.OperationStatusFailure, result.ProgressResult.OperationStatus)
	assert.Contains(t, result.ProgressResult.StatusMessage, "appuser")
	assert.Contains(t, strings.ToLower(result.ProgressResult.StatusMessage), "owns")
}

func TestDatabaseRoleListIsUnsupported(t *testing.T) {
	_, err := testRole().List(context.Background(), &resource.ListRequest{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not supported")
	assert.Contains(t, err.Error(), databaseRoleType)
}

// The verifier is offline-attackable material, so it is classed as secret
// alongside the password itself: neither may appear in a log line or in an
// error returned to the agent.
func TestDatabaseRoleNeverLeaksPasswordOrVerifier(t *testing.T) {
	var logged bytes.Buffer
	ctx := plugin.WithLogger(context.Background(),
		plugin.NewPluginLogger(slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelDebug}))))

	client := &mockDataAPIClient{}
	servingProbe(client)
	client.On("ExecuteStatement", mock.Anything, mock.Anything).
		Return(emptyRoleCatalog(t), nil).Once()
	// The engine rejects the statement and quotes it back — verifier included.
	client.On("ExecuteStatement", mock.Anything, mock.Anything).
		Return(nil, &rdsdatatypes.DatabaseErrorException{
			Message: strPtr(`ERROR: syntax error at or near "SCRAM-SHA-256$4096:AAAA$BBBB:CCCC"`),
		}).Once()

	result, err := testRole().createWithClient(ctx, client, aurora(t),
		&resource.CreateRequest{Properties: roleProps(t, nil)})
	require.NoError(t, err)
	require.NotNil(t, result)

	verifier := extractVerifier(t, client.statements)
	// The engine quoted back a verifier this run did not compose, so it cannot
	// be redacted by value: nothing verifier-shaped may survive into what the
	// agent is handed, or into a log line.
	reported := result.ProgressResult.StatusMessage

	assert.NotContains(t, reported, testPassword)
	assert.NotContains(t, reported, verifier)
	assert.NotContains(t, reported, "SCRAM-SHA-256$")
	assert.NotContains(t, logged.String(), testPassword)
	assert.NotContains(t, logged.String(), verifier)
	assert.NotContains(t, logged.String(), "SCRAM-SHA-256$",
		"no log line may carry verifier material at all")
}

// extractVerifier pulls the composed verifier out of the CREATE ROLE statement
// the run actually produced, so the leak assertions test the real value rather
// than a fixture.
func extractVerifier(t *testing.T, statements []string) string {
	t.Helper()
	for _, statement := range statements {
		if _, after, found := strings.Cut(statement, "PASSWORD '"); found {
			verifier, _, ok := strings.Cut(after, "'")
			require.True(t, ok)
			require.Contains(t, verifier, "SCRAM-SHA-256$")
			return verifier
		}
	}
	t.Fatal("no statement carried a composed verifier")
	return ""
}
