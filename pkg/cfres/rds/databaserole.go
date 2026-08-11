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

func (r *DatabaseRole) createWithClient(ctx context.Context, client dataAPIClient, clusters rdsClusterClient, request *resource.CreateRequest) (*resource.CreateResult, error) {
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

	log := plugin.LoggerFromContext(ctx)
	log.Info("rds: creating database role",
		"cluster_arn", settings.clusterArn, "role_name", settings.roleName)

	exists, err := roleExists(ctx, client, settings)
	if err != nil {
		return nil, err
	}

	if !exists {
		create := fmt.Sprintf("CREATE ROLE %s %s PASSWORD %s",
			quoteIdentifier(settings.roleName), loginClause(settings.canLogin), quoteLiteral(verifier))
		if _, err := execute(ctx, client, settings.clusterArn, settings.secretArn, create, nil); err != nil {
			// The call may have committed before its response was lost, so a
			// duplicate here means the role is ours to adopt, not a failure.
			if !isDuplicateObjectError(err) {
				return nil, secretSafeError(err, "failed to create role %q", settings.roleName, settings.password, verifier)
			}
			log.Info("rds: role already existed on create; adopting it",
				"cluster_arn", settings.clusterArn, "role_name", settings.roleName)
			exists = true
		}
	}

	if exists {
		// The stored verifier is salted, so the declared password cannot be
		// compared against it — re-assert both attributes instead.
		if err := r.assertRoleState(ctx, client, settings, verifier); err != nil {
			return nil, err
		}
	}

	nativeID := buildNativeID(settings.clusterArn, settings.secretArn, settings.roleName)
	properties, err := r.readBack(ctx, client, nativeID)
	if err != nil {
		return nil, err
	}

	return &resource.CreateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationCreate,
			OperationStatus:    resource.OperationStatusSuccess,
			NativeID:           nativeID,
			ResourceProperties: properties,
		},
	}, nil
}

// assertRoleState re-applies the declared password and login attribute to a role
// that already exists.
func (r *DatabaseRole) assertRoleState(ctx context.Context, client dataAPIClient, settings *roleSettings, verifier string) error {
	setPassword := fmt.Sprintf("ALTER ROLE %s PASSWORD %s",
		quoteIdentifier(settings.roleName), quoteLiteral(verifier))
	if _, err := execute(ctx, client, settings.clusterArn, settings.secretArn, setPassword, nil); err != nil {
		return secretSafeError(err, "failed to set the password of role %q", settings.roleName, settings.password, verifier)
	}

	setLogin := fmt.Sprintf("ALTER ROLE %s %s",
		quoteIdentifier(settings.roleName), loginClause(settings.canLogin))
	if _, err := execute(ctx, client, settings.clusterArn, settings.secretArn, setLogin, nil); err != nil {
		return fmt.Errorf("failed to set the login attribute of role %q: %w", settings.roleName, err)
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
					StatusMessage: fmt.Sprintf("the replacement admin secret %q could not be used to reach the cluster: %v",
						desired.secretArn, err),
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

// Status returns success immediately — every Data API call is synchronous.
func (r *DatabaseRole) Status(_ context.Context, request *resource.StatusRequest) (*resource.StatusResult, error) {
	return synchronousStatus(request), nil
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

func roleExists(ctx context.Context, client dataAPIClient, settings *roleSettings) (bool, error) {
	records, err := queryRole(ctx, client, settings.clusterArn, settings.secretArn, settings.roleName)
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
