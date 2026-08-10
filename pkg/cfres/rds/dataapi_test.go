// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package rds

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/rdsdata"
	rdsdatatypes "github.com/aws/aws-sdk-go-v2/service/rdsdata/types"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

const (
	testClusterArn = "arn:aws:rds:us-east-1:123456789012:cluster:formae-test"
	testSecretArn  = "arn:aws:secretsmanager:us-east-1:123456789012:secret:formae-test-abc123"
	testRegion     = "us-east-1"
)

func TestNativeIDRoundTrip(t *testing.T) {
	id := buildNativeID(testClusterArn, testSecretArn, "appdb")
	assert.Equal(t, testClusterArn+"|"+testSecretArn+"|appdb", id)

	clusterArn, secretArn, name, err := parseNativeID(id)
	require.NoError(t, err)
	assert.Equal(t, testClusterArn, clusterArn)
	assert.Equal(t, testSecretArn, secretArn)
	assert.Equal(t, "appdb", name)
}

func TestParseNativeIDRejectsMalformed(t *testing.T) {
	tests := []struct {
		name     string
		nativeID string
	}{
		{"empty", ""},
		{"no separators", "arn:aws:rds:us-east-1:123456789012:cluster:x"},
		{"one separator", testClusterArn + "|" + testSecretArn},
		{"empty cluster", "|" + testSecretArn + "|appdb"},
		{"empty secret", testClusterArn + "||appdb"},
		{"empty name", testClusterArn + "|" + testSecretArn + "|"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, _, err := parseNativeID(tt.nativeID)
			assert.Error(t, err)
		})
	}
}

func TestParseNativeIDKeepsNamesContainingSeparatorOutOfEarlierFields(t *testing.T) {
	// SplitN with a limit of 3 means only the first two pipes are structural.
	_, _, name, err := parseNativeID(testClusterArn + "|" + testSecretArn + "|odd|name")
	require.NoError(t, err)
	assert.Equal(t, "odd|name", name)
}

func TestValidateIdentifier(t *testing.T) {
	valid := []string{"appdb", "_private", "a", "App_Db$1", strings.Repeat("a", 63)}
	for _, name := range valid {
		t.Run("valid/"+name, func(t *testing.T) {
			assert.NoError(t, validateIdentifier(name))
		})
	}

	invalid := []struct {
		name  string
		value string
	}{
		{"empty", ""},
		{"leading digit", "1db"},
		{"hyphen", "app-db"},
		{"space", "app db"},
		{"quote injection", `a"; DROP DATABASE x --`},
		{"semicolon", "app;drop"},
		{"too long", strings.Repeat("a", 64)},
		{"non ascii", "café"},
		{"newline", "app\ndb"},
	}
	for _, tt := range invalid {
		t.Run("invalid/"+tt.name, func(t *testing.T) {
			assert.Error(t, validateIdentifier(tt.value))
		})
	}
}

func TestQuoteIdentifierDoublesEmbeddedQuotes(t *testing.T) {
	assert.Equal(t, `"appdb"`, quoteIdentifier("appdb"))
	assert.Equal(t, `"a""b"`, quoteIdentifier(`a"b`))
}

func TestQuoteLiteralDoublesEmbeddedQuotes(t *testing.T) {
	assert.Equal(t, `'secret'`, quoteLiteral("secret"))
	assert.Equal(t, `'it''s'`, quoteLiteral("it's"))
}

func TestValidateClusterArn(t *testing.T) {
	tests := []struct {
		name       string
		clusterArn string
		secretArn  string
		region     string
		wantErr    string
	}{
		{
			name:       "matching pair",
			clusterArn: testClusterArn,
			secretArn:  testSecretArn,
			region:     testRegion,
		},
		{
			name:       "malformed cluster arn",
			clusterArn: "not-an-arn",
			secretArn:  testSecretArn,
			region:     testRegion,
			wantErr:    "cluster ARN",
		},
		{
			name:       "cluster arn is not an rds cluster",
			clusterArn: "arn:aws:rds:us-east-1:123456789012:db:formae-test",
			secretArn:  testSecretArn,
			region:     testRegion,
			wantErr:    "cluster",
		},
		{
			name:       "wrong service",
			clusterArn: "arn:aws:ec2:us-east-1:123456789012:cluster:formae-test",
			secretArn:  testSecretArn,
			region:     testRegion,
			wantErr:    "rds",
		},
		{
			name:       "region mismatch with target",
			clusterArn: "arn:aws:rds:eu-west-1:123456789012:cluster:formae-test",
			secretArn:  "arn:aws:secretsmanager:eu-west-1:123456789012:secret:formae-test-abc123",
			region:     testRegion,
			wantErr:    "region",
		},
		{
			name:       "account mismatch between cluster and secret",
			clusterArn: testClusterArn,
			secretArn:  "arn:aws:secretsmanager:us-east-1:999988887777:secret:formae-test-abc123",
			region:     testRegion,
			wantErr:    "account",
		},
		{
			name:       "malformed secret arn",
			clusterArn: testClusterArn,
			secretArn:  "not-an-arn",
			region:     testRegion,
			wantErr:    "secret ARN",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateClusterArn(tt.clusterArn, tt.secretArn, tt.region)
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, strings.ToLower(err.Error()), strings.ToLower(tt.wantErr))
		})
	}
}

func TestClassifyDataAPIError(t *testing.T) {
	tests := []struct {
		name            string
		err             error
		wantCode        resource.OperationErrorCode
		wantRecoverable bool
	}{
		{"resuming", &rdsdatatypes.DatabaseResumingException{}, resource.OperationErrorCodeNotStabilized, true},
		{"unavailable", &rdsdatatypes.DatabaseUnavailableException{}, resource.OperationErrorCodeNotStabilized, true},
		{"service unavailable", &rdsdatatypes.ServiceUnavailableError{}, resource.OperationErrorCodeServiceInternalError, true},
		{"internal server error", &rdsdatatypes.InternalServerErrorException{}, resource.OperationErrorCodeServiceInternalError, true},
		{"statement timeout", &rdsdatatypes.StatementTimeoutException{}, resource.OperationErrorCodeServiceTimeout, true},
		{"http endpoint disabled", &rdsdatatypes.HttpEndpointNotEnabledException{}, resource.OperationErrorCodeInvalidRequest, false},
		{"access denied", &rdsdatatypes.AccessDeniedException{}, resource.OperationErrorCodeAccessDenied, false},
		{"forbidden", &rdsdatatypes.ForbiddenException{}, resource.OperationErrorCodeAccessDenied, false},
		{"invalid secret", &rdsdatatypes.InvalidSecretException{}, resource.OperationErrorCodeInvalidCredentials, false},
		{"secrets error", &rdsdatatypes.SecretsErrorException{}, resource.OperationErrorCodeInvalidCredentials, false},
		{"database not found", &rdsdatatypes.DatabaseNotFoundException{}, resource.OperationErrorCodeNotFound, false},
		{"not found", &rdsdatatypes.NotFoundException{}, resource.OperationErrorCodeNotFound, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, recoverable := classifyDataAPIError(tt.err)
			assert.Equal(t, tt.wantCode, code)
			assert.Equal(t, tt.wantRecoverable, recoverable)
		})
	}
}

func TestClassifyDataAPIErrorMapsDuplicateObjectToAlreadyExists(t *testing.T) {
	for _, msg := range []string{
		`ERROR: database "appdb" already exists; SQLState: 42P04`,
		`ERROR: role "appuser" already exists; SQLState: 42710`,
	} {
		err := &rdsdatatypes.BadRequestException{Message: strPtr(msg)}
		code, recoverable := classifyDataAPIError(err)
		assert.Equal(t, resource.OperationErrorCodeAlreadyExists, code, msg)
		assert.False(t, recoverable)
	}
}

func TestClassifyDataAPIErrorForwardsOtherEngineFaultsAsInvalidRequest(t *testing.T) {
	err := &rdsdatatypes.DatabaseErrorException{Message: strPtr(`ERROR: syntax error at or near "SELCT"; SQLState: 42601`)}
	code, recoverable := classifyDataAPIError(err)
	assert.Equal(t, resource.OperationErrorCodeInvalidRequest, code)
	assert.False(t, recoverable)
}

func TestClassifyDataAPIErrorIsTerminalForUnknownErrors(t *testing.T) {
	code, recoverable := classifyDataAPIError(errors.New("something unrecognised"))
	assert.Equal(t, resource.OperationErrorCodeGeneralServiceException, code)
	assert.False(t, recoverable)
}

func TestSQLStatePredicates(t *testing.T) {
	tests := []struct {
		name      string
		message   string
		predicate func(error) bool
		want      bool
	}{
		{"duplicate database", `database "appdb" already exists; SQLState: 42P04`, isDuplicateObjectError, true},
		{"duplicate role", `role "appuser" already exists; SQLState: 42710`, isDuplicateObjectError, true},
		{"duplicate by phrase only", `ERROR: database "appdb" already exists`, isDuplicateObjectError, true},
		{"in use", `database "appdb" is being accessed by other users; SQLState: 55006`, isInUseError, true},
		{"dependent objects", `role "appuser" cannot be dropped because some objects depend on it; SQLState: 2BP01`, isDependentObjectsError, true},
		{"undefined role", `role "appuser" does not exist; SQLState: 42704`, isUndefinedObjectError, true},
		{"undefined database", `database "appdb" does not exist; SQLState: 3D000`, isUndefinedObjectError, true},

		// A different, explicit SQLSTATE must not be rescued by phrase matching.
		{"other sqlstate is not duplicate", `syntax error; SQLState: 42601`, isDuplicateObjectError, false},
		{"other sqlstate is not in use", `permission denied; SQLState: 42501`, isInUseError, false},
		{"other sqlstate is not undefined", `syntax error; SQLState: 42601`, isUndefinedObjectError, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.predicate(&rdsdatatypes.BadRequestException{Message: strPtr(tt.message)}))
		})
	}
}

func TestSQLStatePredicatesIgnoreUnrelatedErrors(t *testing.T) {
	assert.False(t, isDuplicateObjectError(errors.New("network unreachable")))
	assert.False(t, isInUseError(nil))
	assert.False(t, isDependentObjectsError(errors.New("boom")))
	assert.False(t, isUndefinedObjectError(errors.New("boom")))
}

func TestExecuteSendsStatementAgainstTheAdminDatabase(t *testing.T) {
	client := &mockDataAPIClient{}
	client.On("ExecuteStatement", mock.Anything, mock.Anything).
		Return(&rdsdata.ExecuteStatementOutput{}, nil)

	params := []rdsdatatypes.SqlParameter{
		{Name: strPtr("name"), Value: &rdsdatatypes.FieldMemberStringValue{Value: "appdb"}},
	}
	_, err := execute(context.Background(), client, testClusterArn, testSecretArn, "SELECT 1", params)
	require.NoError(t, err)

	input := client.Calls[0].Arguments.Get(1).(*rdsdata.ExecuteStatementInput)
	assert.Equal(t, testClusterArn, *input.ResourceArn)
	assert.Equal(t, testSecretArn, *input.SecretArn)
	assert.Equal(t, adminDatabase, *input.Database)
	assert.Equal(t, "SELECT 1", *input.Sql)
	assert.Equal(t, rdsdatatypes.RecordsFormatTypeJson, input.FormatRecordsAs)
	assert.Equal(t, params, input.Parameters)
	// CREATE DATABASE cannot run inside a transaction block.
	assert.Nil(t, input.TransactionId)
}

func strPtr(s string) *string { return &s }
