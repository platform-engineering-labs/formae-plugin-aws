// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

// Package rds provides resources for objects that live inside an Aurora
// PostgreSQL cluster — databases and their owning roles — provisioned over the
// RDS Data API. CloudControl models the cluster and its instances but nothing
// inside the engine, so these are implemented directly against the AWS SDK.
package rds

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/arn"
	"github.com/aws/aws-sdk-go-v2/service/rdsdata"
	rdsdatatypes "github.com/aws/aws-sdk-go-v2/service/rdsdata/types"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
)

// adminDatabase is the database the admin credentials connect to in order to
// run catalog queries and cluster-level DDL. CREATE DATABASE and DROP DATABASE
// cannot run from inside the database they operate on.
const adminDatabase = "postgres"

// dataAPIClient is the narrow slice of the rds-data API these resources use.
type dataAPIClient interface {
	ExecuteStatement(ctx context.Context, params *rdsdata.ExecuteStatementInput, optFns ...func(*rdsdata.Options)) (*rdsdata.ExecuteStatementOutput, error)
}

// buildNativeID joins the cluster ARN, the admin secret ARN and the object name.
// DeleteRequest carries only a NativeID — no properties — so everything a DROP
// needs to authenticate has to live here. ARNs and validated PostgreSQL
// identifiers cannot contain "|".
func buildNativeID(clusterArn, secretArn, name string) string {
	return fmt.Sprintf("%s|%s|%s", clusterArn, secretArn, name)
}

func parseNativeID(nativeID string) (clusterArn, secretArn, name string, err error) {
	parts := strings.SplitN(nativeID, "|", 3)
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", "", "", fmt.Errorf("invalid NativeID format: expected clusterArn|adminSecretArn|name, got: %s", nativeID)
	}
	return parts[0], parts[1], parts[2], nil
}

// identifierPattern is the set of names accepted as a PostgreSQL identifier.
// Identifiers cannot be bind parameters in DDL, so anything reaching the wire
// unquoted has to be proven safe first; 63 characters is PostgreSQL's NAMEDATALEN
// limit, past which the engine silently truncates.
var identifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_$]{0,62}$`)

func validateIdentifier(name string) error {
	if !identifierPattern.MatchString(name) {
		return fmt.Errorf("invalid identifier %q: must start with a letter or underscore, contain only letters, digits, underscores or dollar signs, and be at most 63 characters", name)
	}
	return nil
}

// quoteIdentifier double-quotes an identifier, doubling any embedded quote.
// Callers validate first — this is the second line of defence, not the first.
func quoteIdentifier(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// quoteLiteral single-quotes a string literal, doubling any embedded quote. Used
// only for the SCRAM verifier, which a PASSWORD clause cannot take as a bind
// parameter.
func quoteLiteral(s string) string {
	return `'` + strings.ReplaceAll(s, `'`, `''`) + `'`
}

// validateClusterArn checks the two ARNs before any call goes out, so a request
// is never silently sent to the target's region for a cluster that lives
// somewhere else.
//
// The target's account id is not available to the plugin — pkg/config carries
// only Region and Profile, and resolving an account would cost an STS call per
// operation. Requiring the cluster and its admin secret to share an account is a
// pure string check that still catches the realistic mistake of pairing a
// cluster with a secret from another account; a consistently-wrong pair fails
// closed at the Data API with AccessDenied.
func validateClusterArn(clusterArn, secretArn, region string) error {
	cluster, err := arn.Parse(clusterArn)
	if err != nil {
		return fmt.Errorf("invalid cluster ARN %q: %w", clusterArn, err)
	}
	if cluster.Service != "rds" {
		return fmt.Errorf("invalid cluster ARN %q: expected an rds ARN, got service %q", clusterArn, cluster.Service)
	}
	if !strings.HasPrefix(cluster.Resource, "cluster:") {
		return fmt.Errorf("invalid cluster ARN %q: expected an rds cluster ARN, got resource %q", clusterArn, cluster.Resource)
	}

	secret, err := arn.Parse(secretArn)
	if err != nil {
		return fmt.Errorf("invalid admin secret ARN %q: %w", secretArn, err)
	}
	if secret.Service != "secretsmanager" {
		return fmt.Errorf("invalid admin secret ARN %q: expected a secretsmanager ARN, got service %q", secretArn, secret.Service)
	}

	if cluster.Region != region {
		return fmt.Errorf("cluster %q is in region %q but the target is configured for region %q", clusterArn, cluster.Region, region)
	}
	if cluster.AccountID != secret.AccountID {
		return fmt.Errorf("cluster %q is in account %q but its admin secret is in account %q", clusterArn, cluster.AccountID, secret.AccountID)
	}

	return nil
}

// execute runs one statement against the cluster's admin database. Statements
// are never wrapped in a transaction: CREATE DATABASE and DROP DATABASE cannot
// run inside a transaction block.
func execute(ctx context.Context, client dataAPIClient, clusterArn, secretArn, sql string, params []rdsdatatypes.SqlParameter) (*rdsdata.ExecuteStatementOutput, error) {
	return client.ExecuteStatement(ctx, &rdsdata.ExecuteStatementInput{
		ResourceArn:     aws.String(clusterArn),
		SecretArn:       aws.String(secretArn),
		Database:        aws.String(adminDatabase),
		Sql:             aws.String(sql),
		Parameters:      params,
		FormatRecordsAs: rdsdatatypes.RecordsFormatTypeJson,
	})
}

// stringParam builds a named string bind parameter for a catalog query.
func stringParam(name, value string) rdsdatatypes.SqlParameter {
	return rdsdatatypes.SqlParameter{
		Name:  aws.String(name),
		Value: &rdsdatatypes.FieldMemberStringValue{Value: value},
	}
}

// decodeRecords unmarshals a JSON-formatted Data API result set. An empty result
// set decodes to no records rather than an error.
func decodeRecords(out *rdsdata.ExecuteStatementOutput) ([]map[string]any, error) {
	if out == nil || out.FormattedRecords == nil || *out.FormattedRecords == "" {
		return nil, nil
	}
	var records []map[string]any
	if err := json.Unmarshal([]byte(*out.FormattedRecords), &records); err != nil {
		return nil, fmt.Errorf("failed to decode the result set: %w", err)
	}
	return records, nil
}

func recordString(record map[string]any, column string) string {
	value, _ := record[column].(string)
	return value
}

func recordBool(record map[string]any, column string) bool {
	value, _ := record[column].(bool)
	return value
}

// boolProperty reads an optional boolean property, falling back to the schema's
// default when the agent did not send one.
func boolProperty(props map[string]any, key string, fallback bool) bool {
	value, ok := props[key].(bool)
	if !ok {
		return fallback
	}
	return value
}

// secretSafeError wraps an engine failure for a statement that carried secret
// material. PostgreSQL quotes the offending statement back in some errors, so
// the password and the verifier are redacted out of the message before it is
// forwarded — the verifier is offline-attackable material, not a harmless
// digest.
func secretSafeError(err error, format string, name string, secrets ...string) error {
	message := err.Error()
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		message = strings.ReplaceAll(message, secret, "[redacted]")
	}
	return fmt.Errorf(format+": %s", name, message)
}

// synchronousStatus reports success immediately: every Data API call completes
// before its response returns, so there is no asynchronous state to poll.
func synchronousStatus(request *resource.StatusRequest) *resource.StatusResult {
	return &resource.StatusResult{
		ProgressResult: &resource.ProgressResult{
			Operation:       resource.OperationCheckStatus,
			OperationStatus: resource.OperationStatusSuccess,
			NativeID:        request.NativeID,
		},
	}
}

// unsupportedListError explains why listing is refused rather than returning an
// empty page, which would read as "there are none".
func unsupportedListError(resourceType string) error {
	return fmt.Errorf("listing is not supported: %s is not discoverable, because enumerating objects inside a cluster needs admin credentials discovery cannot supply", resourceType)
}

// classifyDataAPIError maps an rds-data fault onto an SDK error code and reports
// whether the condition is recoverable. The mapping keys on the SDK's TYPED
// exceptions rather than message text, so a wording change upstream cannot
// silently reclassify a fault. Message text is consulted only to tell a
// duplicate-object engine error apart from other engine errors, which the API
// reports under one exception type.
//
// The recoverable flag is advisory for callers that need it; the agent's retry
// decision is driven by the returned code, which it looks up in its own
// recoverable-code table.
func classifyDataAPIError(err error) (resource.OperationErrorCode, bool) {
	if err == nil {
		return "", false
	}

	var resuming *rdsdatatypes.DatabaseResumingException
	var unavailable *rdsdatatypes.DatabaseUnavailableException
	if errors.As(err, &resuming) || errors.As(err, &unavailable) {
		return resource.OperationErrorCodeNotStabilized, true
	}

	var serviceUnavailable *rdsdatatypes.ServiceUnavailableError
	var internalError *rdsdatatypes.InternalServerErrorException
	if errors.As(err, &serviceUnavailable) || errors.As(err, &internalError) {
		return resource.OperationErrorCodeServiceInternalError, true
	}

	var timeout *rdsdatatypes.StatementTimeoutException
	if errors.As(err, &timeout) {
		return resource.OperationErrorCodeServiceTimeout, true
	}

	var httpEndpointDisabled *rdsdatatypes.HttpEndpointNotEnabledException
	if errors.As(err, &httpEndpointDisabled) {
		return resource.OperationErrorCodeInvalidRequest, false
	}

	var accessDenied *rdsdatatypes.AccessDeniedException
	var forbidden *rdsdatatypes.ForbiddenException
	if errors.As(err, &accessDenied) || errors.As(err, &forbidden) {
		return resource.OperationErrorCodeAccessDenied, false
	}

	var invalidSecret *rdsdatatypes.InvalidSecretException
	var secretsError *rdsdatatypes.SecretsErrorException
	if errors.As(err, &invalidSecret) || errors.As(err, &secretsError) {
		return resource.OperationErrorCodeInvalidCredentials, false
	}

	var databaseNotFound *rdsdatatypes.DatabaseNotFoundException
	var notFound *rdsdatatypes.NotFoundException
	if errors.As(err, &databaseNotFound) || errors.As(err, &notFound) {
		return resource.OperationErrorCodeNotFound, false
	}

	var badRequest *rdsdatatypes.BadRequestException
	var databaseError *rdsdatatypes.DatabaseErrorException
	if errors.As(err, &badRequest) || errors.As(err, &databaseError) {
		if isDuplicateObjectError(err) {
			return resource.OperationErrorCodeAlreadyExists, false
		}
		return resource.OperationErrorCodeInvalidRequest, false
	}

	return resource.OperationErrorCodeGeneralServiceException, false
}

// PostgreSQL SQLSTATEs the provisioners branch on.
const (
	sqlStateDuplicateDatabase     = "42P04"
	sqlStateDuplicateObject       = "42710"
	sqlStateObjectInUse           = "55006"
	sqlStateDependentObjectsExist = "2BP01"
	sqlStateUndefinedObject       = "42704"
	sqlStateInvalidCatalogName    = "3D000"
)

// sqlStatePattern matches the SQLSTATE the Data API appends to an engine error.
var sqlStatePattern = regexp.MustCompile(`(?i)SQLState:\s*([0-9A-Z]{5})`)

// matchesEngineError reports whether an engine error is one of the given
// SQLSTATEs. When the message carries a SQLSTATE, that answer is final — an
// explicit, non-matching code must not be rescued by phrase matching, which
// would misclassify an unrelated failure. Phrases are consulted only when no
// SQLSTATE is present at all.
func matchesEngineError(err error, states []string, phrases []string) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	if match := sqlStatePattern.FindStringSubmatch(msg); match != nil {
		return slices.Contains(states, strings.ToUpper(match[1]))
	}
	lower := strings.ToLower(msg)
	for _, phrase := range phrases {
		if strings.Contains(lower, phrase) {
			return true
		}
	}
	return false
}

// isDuplicateObjectError reports whether the engine refused because the database
// or role already exists.
func isDuplicateObjectError(err error) bool {
	return matchesEngineError(err,
		[]string{sqlStateDuplicateDatabase, sqlStateDuplicateObject},
		[]string{"already exists"})
}

// isInUseError reports whether the engine refused because the database has open
// connections.
func isInUseError(err error) bool {
	return matchesEngineError(err,
		[]string{sqlStateObjectInUse},
		[]string{"is being accessed by other users"})
}

// isDependentObjectsError reports whether the engine refused a DROP because the
// role still owns objects.
func isDependentObjectsError(err error) bool {
	return matchesEngineError(err,
		[]string{sqlStateDependentObjectsExist},
		[]string{"depend on it", "depends on it"})
}

// isUndefinedObjectError reports whether the object is already gone. A committed
// DROP whose response was lost must not fail the agent's retry, so both delete
// paths treat this as success.
func isUndefinedObjectError(err error) bool {
	return matchesEngineError(err,
		[]string{sqlStateUndefinedObject, sqlStateInvalidCatalogName},
		[]string{"does not exist"})
}
