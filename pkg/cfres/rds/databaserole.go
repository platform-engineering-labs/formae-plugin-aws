// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package rds

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/aws-sdk-go-v2/service/rdsdata"
	rdsdatatypes "github.com/aws/aws-sdk-go-v2/service/rdsdata/types"

	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres/prov"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres/registry"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres/utils"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/config"
	"github.com/platform-engineering-labs/formae/pkg/plugin"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
)

const databaseRoleType = "AWS::RDS::DatabaseRole"

// DatabaseRole provisions a PostgreSQL login role inside an Aurora cluster over
// the RDS Data API.
type DatabaseRole struct {
	cfg *config.Config
}

var _ prov.Provisioner = &DatabaseRole{}

func init() {
	registry.Register(databaseRoleType,
		[]resource.Operation{
			resource.OperationCreate,
			resource.OperationRead,
			resource.OperationUpdate,
			resource.OperationDelete,
			resource.OperationCheckStatus,
			resource.OperationList,
		},
		func(cfg *config.Config) prov.Provisioner {
			return &DatabaseRole{cfg: cfg}
		})
}

// roleSettings is the declared state of a role, as parsed from a request.
type roleSettings struct {
	clusterArn string
	secretArn  string
	roleName   string
	password   string
	canLogin   bool
}

func (r *DatabaseRole) parseSettings(properties json.RawMessage) (*roleSettings, error) {
	var props map[string]any
	if err := json.Unmarshal(properties, &props); err != nil {
		return nil, fmt.Errorf("failed to parse properties: %w", err)
	}

	clusterArn, err := utils.GetStringProperty(props, "ClusterArn")
	if err != nil {
		return nil, fmt.Errorf("invalid ClusterArn: %w", err)
	}
	secretArn, err := utils.GetStringProperty(props, "AdminSecretArn")
	if err != nil {
		return nil, fmt.Errorf("invalid AdminSecretArn: %w", err)
	}
	roleName, err := utils.GetStringProperty(props, "RoleName")
	if err != nil {
		return nil, fmt.Errorf("invalid RoleName: %w", err)
	}
	password, err := utils.GetStringProperty(props, "Password")
	if err != nil {
		return nil, fmt.Errorf("invalid Password: %w", err)
	}

	if err := validateIdentifier(roleName); err != nil {
		return nil, err
	}
	if err := validateClusterArn(clusterArn, secretArn, r.cfg.Region); err != nil {
		return nil, err
	}

	return &roleSettings{
		clusterArn: clusterArn,
		secretArn:  secretArn,
		roleName:   roleName,
		password:   password,
		canLogin:   boolProperty(props, "CanLogin", true),
	}, nil
}

func (r *DatabaseRole) Create(ctx context.Context, request *resource.CreateRequest) (*resource.CreateResult, error) {
	cfg, err := r.cfg.ToAwsConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to load AWS config: %w", err)
	}
	return r.createWithClient(ctx, rdsdata.NewFromConfig(cfg), rds.NewFromConfig(cfg), request)
}

// createWithClient reports a recognised Data API fault through the result, so
// the agent sees its error code and can retry a condition that clears on its
// own — most often a cluster that is not serving statements yet.
//
// The error is logged before it is handed back: the agent reports a classified
// failure by its error code alone, so the text diagnosing it reaches no log
// otherwise. Every error this package returns for a statement carrying secret
// material has been through secretSafeError first.
func (r *DatabaseRole) createWithClient(ctx context.Context, client dataAPIClient, clusters rdsClusterClient, request *resource.CreateRequest) (*resource.CreateResult, error) {
	result, err := r.create(ctx, client, clusters, request)
	if err != nil {
		clusterArn, roleName := r.declaredIdentity(request.Properties)
		plugin.LoggerFromContext(ctx).Error("rds: failed to create database role",
			"cluster_arn", clusterArn, "role_name", roleName, "error", logSafeError(err))
		if progress, ok := dataAPIProgressFailure(resource.OperationCreate, "", err); ok {
			return &resource.CreateResult{ProgressResult: progress}, nil
		}
		return nil, err
	}
	return result, nil
}

// declaredIdentity names the cluster and the role a request was for, for a log
// line. A request the plugin could not parse names nothing, which is the
// diagnosis rather than a reason to fail the log.
func (r *DatabaseRole) declaredIdentity(properties json.RawMessage) (clusterArn, roleName string) {
	settings, err := r.parseSettings(properties)
	if err != nil {
		return "", ""
	}
	return settings.clusterArn, settings.roleName
}

// roleCreateIntent is everything a create needs to bring a role to its declared
// state. The plaintext password is deliberately absent: it is turned into a
// verifier before the intent is built, and only the verifier ever reaches the
// engine — so this is what a deferred create can be parked with.
type roleCreateIntent struct {
	clusterArn string
	secretArn  string
	roleName   string
	canLogin   bool
	verifier   string
}

func (r *DatabaseRole) create(ctx context.Context, client dataAPIClient, clusters rdsClusterClient, request *resource.CreateRequest) (*resource.CreateResult, error) {
	settings, err := r.parseSettings(request.Properties)
	if err != nil {
		return nil, err
	}

	// One describe at the point where a clear diagnosis is worth most, matching
	// the database resource: an unsupported engine is named here rather than
	// reaching the engine as a catalog query it cannot answer.
	if err := preflightCluster(ctx, clusters, settings.clusterArn); err != nil {
		return nil, err
	}

	verifier, err := newScramVerifier(settings.password)
	if err != nil {
		return nil, err
	}

	intent := &roleCreateIntent{
		clusterArn: settings.clusterArn,
		secretArn:  settings.secretArn,
		roleName:   settings.roleName,
		canLogin:   settings.canLogin,
		verifier:   verifier,
	}
	nativeID := buildNativeID(intent.clusterArn, intent.secretArn, intent.roleName)

	// A cluster that reports itself available can still be minutes away from
	// running a statement. The probe decides that before any DDL is composed, so
	// the wait costs nothing but a SELECT.
	ready, code, err := clusterServing(ctx, client, intent.clusterArn, intent.secretArn)
	if !ready {
		if !resource.IsRecoverable(code) {
			return nil, err
		}
		plugin.LoggerFromContext(ctx).Info("rds: cluster is not serving statements yet; waiting to create the database role",
			"cluster_arn", intent.clusterArn, "role_name", intent.roleName)
		return &resource.CreateResult{ProgressResult: deferCreate(pendingRoles, nativeID, intent)}, nil
	}

	progress, err := r.ensureRole(ctx, client, intent)
	if err != nil {
		return nil, err
	}
	return &resource.CreateResult{ProgressResult: progress}, nil
}

// ensureRole brings the role to its declared state. It is idempotent — it adopts
// a role that already exists and re-asserts its attributes — which is what lets
// both the create and the poll that finishes a deferred create run it.
func (r *DatabaseRole) ensureRole(ctx context.Context, client dataAPIClient, intent *roleCreateIntent) (*resource.ProgressResult, error) {
	log := plugin.LoggerFromContext(ctx)
	log.Info("rds: creating database role",
		"cluster_arn", intent.clusterArn, "role_name", intent.roleName)

	exists, err := roleExists(ctx, client, intent)
	if err != nil {
		return nil, err
	}

	if !exists {
		create := fmt.Sprintf("CREATE ROLE %s %s PASSWORD %s",
			quoteIdentifier(intent.roleName), loginClause(intent.canLogin), quoteLiteral(intent.verifier))
		if _, err := execute(ctx, client, intent.clusterArn, intent.secretArn, create, nil); err != nil {
			// The call may have committed before its response was lost, so a
			// duplicate here means the role is ours to adopt, not a failure.
			if !isDuplicateObjectError(err) {
				return nil, secretSafeError(err, "failed to create role %q", intent.roleName, intent.verifier)
			}
			log.Info("rds: role already existed on create; adopting it",
				"cluster_arn", intent.clusterArn, "role_name", intent.roleName)
			exists = true
		}
	}

	if exists {
		// The stored verifier is salted, so the declared password cannot be
		// compared against it — re-assert both attributes instead.
		if err := r.assertRoleState(ctx, client, intent); err != nil {
			return nil, err
		}
	}

	nativeID := buildNativeID(intent.clusterArn, intent.secretArn, intent.roleName)
	properties, err := r.readBack(ctx, client, nativeID)
	if err != nil {
		return nil, err
	}

	return &resource.ProgressResult{
		Operation:          resource.OperationCreate,
		OperationStatus:    resource.OperationStatusSuccess,
		NativeID:           nativeID,
		ResourceProperties: properties,
	}, nil
}

// assertRoleState re-applies the declared password and login attribute to a role
// that already exists.
func (r *DatabaseRole) assertRoleState(ctx context.Context, client dataAPIClient, intent *roleCreateIntent) error {
	setPassword := fmt.Sprintf("ALTER ROLE %s PASSWORD %s",
		quoteIdentifier(intent.roleName), quoteLiteral(intent.verifier))
	if _, err := execute(ctx, client, intent.clusterArn, intent.secretArn, setPassword, nil); err != nil {
		return secretSafeError(err, "failed to set the password of role %q", intent.roleName, intent.verifier)
	}

	setLogin := fmt.Sprintf("ALTER ROLE %s %s",
		quoteIdentifier(intent.roleName), loginClause(intent.canLogin))
	if _, err := execute(ctx, client, intent.clusterArn, intent.secretArn, setLogin, nil); err != nil {
		return fmt.Errorf("failed to set the login attribute of role %q: %w", intent.roleName, err)
	}

	return nil
}

func (r *DatabaseRole) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	client, err := r.newClient(ctx)
	if err != nil {
		return nil, err
	}
	return r.readWithClient(ctx, client, request)
}

func (r *DatabaseRole) readWithClient(ctx context.Context, client dataAPIClient, request *resource.ReadRequest) (*resource.ReadResult, error) {
	result, err := r.read(ctx, client, request)
	if err != nil {
		clusterArn, roleName := nativeIdentity(request.NativeID)
		plugin.LoggerFromContext(ctx).Error("rds: failed to read database role",
			"cluster_arn", clusterArn, "role_name", roleName, "error", logSafeError(err))
		if code, ok := recognizeDataAPIFault(err); ok {
			return &resource.ReadResult{ResourceType: databaseRoleType, ErrorCode: code}, nil
		}
		return nil, err
	}
	return result, nil
}

func (r *DatabaseRole) read(ctx context.Context, client dataAPIClient, request *resource.ReadRequest) (*resource.ReadResult, error) {
	clusterArn, secretArn, roleName, err := parseNativeID(request.NativeID)
	if err != nil {
		return nil, err
	}

	records, err := queryRole(ctx, client, clusterArn, secretArn, roleName)
	if err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return &resource.ReadResult{
			ResourceType: databaseRoleType,
			ErrorCode:    resource.OperationErrorCodeNotFound,
		}, nil
	}

	// Password is deliberately absent: PostgreSQL stores a salted verifier, so
	// there is no value to report and nothing to compare against.
	props := map[string]any{
		"ClusterArn":     clusterArn,
		"AdminSecretArn": secretArn,
		"RoleName":       recordString(records[0], "rolname"),
		"CanLogin":       recordBool(records[0], "rolcanlogin"),
	}

	encoded, err := json.Marshal(props)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal properties: %w", err)
	}

	return &resource.ReadResult{
		ResourceType: databaseRoleType,
		Properties:   string(encoded),
	}, nil
}

func (r *DatabaseRole) Update(ctx context.Context, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	client, err := r.newClient(ctx)
	if err != nil {
		return nil, err
	}
	return r.updateWithClient(ctx, client, request)
}

func (r *DatabaseRole) updateWithClient(ctx context.Context, client dataAPIClient, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	result, err := r.update(ctx, client, request)
	if err != nil {
		clusterArn, roleName := nativeIdentity(request.NativeID)
		plugin.LoggerFromContext(ctx).Error("rds: failed to update database role",
			"cluster_arn", clusterArn, "role_name", roleName, "error", logSafeError(err))
		if progress, ok := dataAPIProgressFailure(resource.OperationUpdate, request.NativeID, err); ok {
			return &resource.UpdateResult{ProgressResult: progress}, nil
		}
		return nil, err
	}
	return result, nil
}

func (r *DatabaseRole) update(ctx context.Context, client dataAPIClient, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	desired, err := r.parseSettings(request.DesiredProperties)
	if err != nil {
		return nil, err
	}
	prior, err := r.parseSettings(request.PriorProperties)
	if err != nil {
		return nil, err
	}

	log := plugin.LoggerFromContext(ctx)
	log.Info("rds: updating database role",
		"cluster_arn", desired.clusterArn, "role_name", desired.roleName)

	// Prove a replacement credential works before the NativeID starts depending
	// on it — a NativeID naming a secret that cannot authenticate would leave
	// the resource unreadable and undeletable.
	if desired.secretArn != prior.secretArn {
		if _, err := execute(ctx, client, desired.clusterArn, desired.secretArn, "SELECT 1", nil); err != nil {
			code, _ := classifyDataAPIError(err)
			return &resource.UpdateResult{
				ProgressResult: &resource.ProgressResult{
					Operation:       resource.OperationUpdate,
					OperationStatus: resource.OperationStatusFailure,
					NativeID:        request.NativeID,
					ErrorCode:       code,
					StatusMessage: fmt.Sprintf("the replacement admin secret %q could not be used to reach the cluster: %s",
						desired.secretArn, logSafeError(err)),
				},
			}, nil
		}
	}

	if desired.password != prior.password {
		verifier, err := newScramVerifier(desired.password)
		if err != nil {
			return nil, err
		}
		statement := fmt.Sprintf("ALTER ROLE %s PASSWORD %s",
			quoteIdentifier(desired.roleName), quoteLiteral(verifier))
		if _, err := execute(ctx, client, desired.clusterArn, desired.secretArn, statement, nil); err != nil {
			return nil, secretSafeError(err, "failed to rotate the password of role %q", desired.roleName, desired.password, verifier)
		}
	}

	if desired.canLogin != prior.canLogin {
		statement := fmt.Sprintf("ALTER ROLE %s %s",
			quoteIdentifier(desired.roleName), loginClause(desired.canLogin))
		if _, err := execute(ctx, client, desired.clusterArn, desired.secretArn, statement, nil); err != nil {
			return nil, fmt.Errorf("failed to set the login attribute of role %q: %w", desired.roleName, err)
		}
	}

	nativeID := buildNativeID(desired.clusterArn, desired.secretArn, desired.roleName)
	properties, err := r.readBack(ctx, client, nativeID)
	if err != nil {
		return nil, err
	}

	return &resource.UpdateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationUpdate,
			OperationStatus:    resource.OperationStatusSuccess,
			NativeID:           nativeID,
			ResourceProperties: properties,
		},
	}, nil
}

func (r *DatabaseRole) Delete(ctx context.Context, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	client, err := r.newClient(ctx)
	if err != nil {
		return nil, err
	}
	return r.deleteWithClient(ctx, client, request)
}

func (r *DatabaseRole) deleteWithClient(ctx context.Context, client dataAPIClient, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	result, err := r.delete(ctx, client, request)
	if err != nil {
		clusterArn, roleName := nativeIdentity(request.NativeID)
		plugin.LoggerFromContext(ctx).Error("rds: failed to drop database role",
			"cluster_arn", clusterArn, "role_name", roleName, "error", logSafeError(err))
		if progress, ok := dataAPIProgressFailure(resource.OperationDelete, request.NativeID, err); ok {
			return &resource.DeleteResult{ProgressResult: progress}, nil
		}
		return nil, err
	}
	return result, nil
}

func (r *DatabaseRole) delete(ctx context.Context, client dataAPIClient, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	clusterArn, secretArn, roleName, err := parseNativeID(request.NativeID)
	if err != nil {
		return nil, err
	}
	if err := validateIdentifier(roleName); err != nil {
		return nil, err
	}

	plugin.LoggerFromContext(ctx).Info("rds: dropping database role",
		"cluster_arn", clusterArn, "role_name", roleName)

	statement := fmt.Sprintf("DROP ROLE %s", quoteIdentifier(roleName))
	if _, err := execute(ctx, client, clusterArn, secretArn, statement, nil); err != nil {
		switch {
		case isUndefinedObjectError(err):
			// A committed DROP whose response was lost must not fail a retry.
		case isDependentObjectsError(err):
			return nil, fmt.Errorf("cannot drop role %q because it still owns objects; reassign or drop them first: %w", roleName, err)
		default:
			return nil, fmt.Errorf("failed to drop role %q: %w", roleName, err)
		}
	}

	return &resource.DeleteResult{
		ProgressResult: &resource.ProgressResult{
			Operation:       resource.OperationDelete,
			OperationStatus: resource.OperationStatusSuccess,
			NativeID:        request.NativeID,
		},
	}, nil
}

// Status finishes a create that was deferred because the cluster could not run
// a statement yet. Every Data API call is synchronous, so this is the only
// operation with anything to poll.
func (r *DatabaseRole) Status(ctx context.Context, request *resource.StatusRequest) (*resource.StatusResult, error) {
	client, err := r.newClient(ctx)
	if err != nil {
		return nil, err
	}
	return r.statusWithClient(ctx, client, request)
}

func (r *DatabaseRole) statusWithClient(ctx context.Context, client dataAPIClient, request *resource.StatusRequest) (*resource.StatusResult, error) {
	result, err := resumeCreate(ctx, client, pendingRoles, request, r.ensureRole)
	if err != nil {
		clusterArn, roleName := nativeIdentity(request.NativeID)
		plugin.LoggerFromContext(ctx).Error("rds: failed to finish creating database role",
			"cluster_arn", clusterArn, "role_name", roleName, "error", logSafeError(err))
		return nil, err
	}
	return result, nil
}

// List is registered so an unsupported listing fails loudly here rather than
// falling through to CloudControl, which has never heard of this type.
func (r *DatabaseRole) List(_ context.Context, _ *resource.ListRequest) (*resource.ListResult, error) {
	return nil, unsupportedListError(databaseRoleType)
}

func (r *DatabaseRole) newClient(ctx context.Context) (dataAPIClient, error) {
	cfg, err := r.cfg.ToAwsConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to load AWS config: %w", err)
	}
	return rdsdata.NewFromConfig(cfg), nil
}

// readBack re-reads the role so the agent persists actual state.
func (r *DatabaseRole) readBack(ctx context.Context, client dataAPIClient, nativeID string) (json.RawMessage, error) {
	result, err := r.readWithClient(ctx, client, &resource.ReadRequest{NativeID: nativeID})
	if err != nil {
		return nil, fmt.Errorf("failed to read back role after write: %w", err)
	}
	if result.ErrorCode == resource.OperationErrorCodeNotFound {
		return nil, fmt.Errorf("role not found immediately after write: %s", nativeID)
	}
	return json.RawMessage(result.Properties), nil
}

// queryRole returns the role's catalog row, or no rows if it does not exist.
func queryRole(ctx context.Context, client dataAPIClient, clusterArn, secretArn, roleName string) ([]map[string]any, error) {
	out, err := execute(ctx, client, clusterArn, secretArn,
		"SELECT rolname, rolcanlogin FROM pg_roles WHERE rolname = :name",
		[]rdsdatatypes.SqlParameter{stringParam("name", roleName)})
	if err != nil {
		return nil, fmt.Errorf("failed to look up role %q: %w", roleName, err)
	}
	return decodeRecords(out)
}

func roleExists(ctx context.Context, client dataAPIClient, intent *roleCreateIntent) (bool, error) {
	records, err := queryRole(ctx, client, intent.clusterArn, intent.secretArn, intent.roleName)
	if err != nil {
		return false, err
	}
	return len(records) > 0, nil
}

func loginClause(canLogin bool) string {
	if canLogin {
		return "LOGIN"
	}
	return "NOLOGIN"
}
