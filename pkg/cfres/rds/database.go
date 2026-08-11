// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package rds

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
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

const databaseType = "AWS::RDS::Database"

// auroraPostgresEngine is the only engine these resources support: the Data API
// is an Aurora feature, and every statement here is PostgreSQL.
const auroraPostgresEngine = "aurora-postgresql"

// rdsClusterClient is the narrow slice of the rds control-plane API used to
// preflight a cluster before the first statement is sent.
type rdsClusterClient interface {
	DescribeDBClusters(ctx context.Context, params *rds.DescribeDBClustersInput, optFns ...func(*rds.Options)) (*rds.DescribeDBClustersOutput, error)
}

// Database provisions a PostgreSQL database inside an Aurora cluster over the
// RDS Data API.
type Database struct {
	cfg *config.Config
}

var _ prov.Provisioner = &Database{}

func init() {
	registry.Register(databaseType,
		[]resource.Operation{
			resource.OperationCreate,
			resource.OperationRead,
			resource.OperationUpdate,
			resource.OperationDelete,
			resource.OperationCheckStatus,
			resource.OperationList,
		},
		func(cfg *config.Config) prov.Provisioner {
			return &Database{cfg: cfg}
		})
}

// databaseSettings is the declared state of a database, as parsed from a request.
type databaseSettings struct {
	clusterArn   string
	secretArn    string
	databaseName string
	owner        string
}

func (d *Database) parseSettings(properties json.RawMessage) (*databaseSettings, error) {
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
	databaseName, err := utils.GetStringProperty(props, "DatabaseName")
	if err != nil {
		return nil, fmt.Errorf("invalid DatabaseName: %w", err)
	}
	owner, err := utils.GetStringProperty(props, "Owner")
	if err != nil {
		return nil, fmt.Errorf("invalid Owner: %w", err)
	}

	if err := validateIdentifier(databaseName); err != nil {
		return nil, err
	}
	if err := validateIdentifier(owner); err != nil {
		return nil, err
	}
	if err := validateClusterArn(clusterArn, secretArn, d.cfg.Region); err != nil {
		return nil, err
	}

	return &databaseSettings{
		clusterArn:   clusterArn,
		secretArn:    secretArn,
		databaseName: databaseName,
		owner:        owner,
	}, nil
}

func (d *Database) Create(ctx context.Context, request *resource.CreateRequest) (*resource.CreateResult, error) {
	cfg, err := d.cfg.ToAwsConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to load AWS config: %w", err)
	}
	return d.createWithClient(ctx, rdsdata.NewFromConfig(cfg), rds.NewFromConfig(cfg), request)
}

// createWithClient reports a recognised Data API fault through the result, so
// the agent sees its error code and can retry a condition that clears on its
// own — most often a cluster that is not serving statements yet.
func (d *Database) createWithClient(ctx context.Context, client dataAPIClient, clusters rdsClusterClient, request *resource.CreateRequest) (*resource.CreateResult, error) {
	result, err := d.create(ctx, client, clusters, request)
	if err != nil {
		if progress, ok := dataAPIProgressFailure(resource.OperationCreate, "", err); ok {
			return &resource.CreateResult{ProgressResult: progress}, nil
		}
		return nil, err
	}
	return result, nil
}

func (d *Database) create(ctx context.Context, client dataAPIClient, clusters rdsClusterClient, request *resource.CreateRequest) (*resource.CreateResult, error) {
	settings, err := d.parseSettings(request.Properties)
	if err != nil {
		return nil, err
	}

	// One describe at the point where a clear diagnosis is worth most. Later
	// operations get the same actionable text from the typed-exception mapping
	// without paying for a describe each time.
	if err := preflightCluster(ctx, clusters, settings.clusterArn); err != nil {
		return nil, err
	}

	log := plugin.LoggerFromContext(ctx)
	log.Info("rds: creating database",
		"cluster_arn", settings.clusterArn, "database_name", settings.databaseName, "owner", settings.owner)

	nativeID := buildNativeID(settings.clusterArn, settings.secretArn, settings.databaseName)

	owner, exists, err := lookupDatabaseOwner(ctx, client, settings.clusterArn, settings.secretArn, settings.databaseName)
	if err != nil {
		return nil, err
	}

	if !exists {
		// The membership grant is permanent, so it is only attempted once the
		// catalog says the create is going ahead: a name collision must fail
		// without leaving the admin added to the requested owner role.
		if err := ensureOwnerMembership(ctx, client, settings); err != nil {
			return nil, err
		}

		statement := fmt.Sprintf("CREATE DATABASE %s OWNER %s",
			quoteIdentifier(settings.databaseName), quoteIdentifier(settings.owner))
		if _, err := execute(ctx, client, settings.clusterArn, settings.secretArn, statement, nil); err != nil {
			if !isDuplicateObjectError(err) {
				return nil, fmt.Errorf("failed to create database %q: %w", settings.databaseName, err)
			}
			// The database appeared between the probe and the create — either our
			// own committed call whose response was lost, or somebody else's. Re-read
			// the catalog rather than assume it is ours.
			owner, exists, err = lookupDatabaseOwner(ctx, client, settings.clusterArn, settings.secretArn, settings.databaseName)
			if err != nil {
				return nil, err
			}
			if !exists {
				return nil, fmt.Errorf("database %q reported as already existing but is not in the catalog", settings.databaseName)
			}
		}
	}

	if exists && owner != settings.owner {
		return &resource.CreateResult{
			ProgressResult: &resource.ProgressResult{
				Operation:       resource.OperationCreate,
				OperationStatus: resource.OperationStatusFailure,
				NativeID:        nativeID,
				ErrorCode:       resource.OperationErrorCodeAlreadyExists,
				StatusMessage: fmt.Sprintf("database %q already exists and is owned by %q, not %q",
					settings.databaseName, owner, settings.owner),
			},
		}, nil
	}

	properties, err := d.readBack(ctx, client, nativeID)
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

// preflightCluster fails with an explicit diagnosis unless the cluster is an
// Aurora PostgreSQL cluster with the Data API switched on.
func preflightCluster(ctx context.Context, clusters rdsClusterClient, clusterArn string) error {
	out, err := clusters.DescribeDBClusters(ctx, &rds.DescribeDBClustersInput{
		DBClusterIdentifier: aws.String(clusterArn),
	})
	if err != nil {
		return fmt.Errorf("failed to describe cluster %q: %w", clusterArn, err)
	}
	if len(out.DBClusters) == 0 {
		return fmt.Errorf("cluster %q not found", clusterArn)
	}

	cluster := out.DBClusters[0]
	engine := aws.ToString(cluster.Engine)
	if engine != auroraPostgresEngine {
		return fmt.Errorf("cluster %q runs engine %q, but this resource supports only %q",
			clusterArn, engine, auroraPostgresEngine)
	}
	if !aws.ToBool(cluster.HttpEndpointEnabled) {
		return fmt.Errorf("cluster %q does not have the Data API enabled; enable the HTTP endpoint on the cluster", clusterArn)
	}

	return nil
}

// ensureOwnerMembership makes the admin a member of the owning role, which
// PostgreSQL requires before it will create or reassign a database owned by that
// role.
//
// The grant is deliberately never revoked. A revoke is a compensating action
// that cannot be made atomic — CREATE DATABASE cannot run in a transaction — so
// every way it fails leaves worse state than keeping it. It also confers nothing
// new: the GRANT only succeeds if the admin already holds ADMIN OPTION on the
// role, so it can re-grant itself membership at any time.
func ensureOwnerMembership(ctx context.Context, client dataAPIClient, settings *databaseSettings) error {
	out, err := execute(ctx, client, settings.clusterArn, settings.secretArn,
		"SELECT pg_has_role(current_user, :owner, 'MEMBER') AS has_role",
		[]rdsdatatypes.SqlParameter{stringParam("owner", settings.owner)})
	if err != nil {
		return fmt.Errorf("failed to check membership of role %q: %w", settings.owner, err)
	}

	records, err := decodeRecords(out)
	if err != nil {
		return err
	}
	if len(records) > 0 && recordBool(records[0], "has_role") {
		return nil
	}

	grant := fmt.Sprintf("GRANT %s TO CURRENT_USER", quoteIdentifier(settings.owner))
	if _, err := execute(ctx, client, settings.clusterArn, settings.secretArn, grant, nil); err != nil {
		return fmt.Errorf("failed to grant membership of role %q to the admin role, which PostgreSQL requires to own a database on its behalf: %w", settings.owner, err)
	}

	return nil
}

func (d *Database) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	client, err := d.newClient(ctx)
	if err != nil {
		return nil, err
	}
	return d.readWithClient(ctx, client, request)
}

func (d *Database) readWithClient(ctx context.Context, client dataAPIClient, request *resource.ReadRequest) (*resource.ReadResult, error) {
	result, err := d.read(ctx, client, request)
	if err != nil {
		if code, ok := recognizeDataAPIFault(err); ok {
			return &resource.ReadResult{ResourceType: databaseType, ErrorCode: code}, nil
		}
		return nil, err
	}
	return result, nil
}

func (d *Database) read(ctx context.Context, client dataAPIClient, request *resource.ReadRequest) (*resource.ReadResult, error) {
	clusterArn, secretArn, databaseName, err := parseNativeID(request.NativeID)
	if err != nil {
		return nil, err
	}

	owner, exists, err := lookupDatabaseOwner(ctx, client, clusterArn, secretArn, databaseName)
	if err != nil {
		return nil, err
	}
	if !exists {
		return &resource.ReadResult{
			ResourceType: databaseType,
			ErrorCode:    resource.OperationErrorCodeNotFound,
		}, nil
	}

	props := map[string]any{
		"ClusterArn":     clusterArn,
		"AdminSecretArn": secretArn,
		"DatabaseName":   databaseName,
		"Owner":          owner,
	}

	encoded, err := json.Marshal(props)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal properties: %w", err)
	}

	return &resource.ReadResult{
		ResourceType: databaseType,
		Properties:   string(encoded),
	}, nil
}

func (d *Database) Update(ctx context.Context, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	client, err := d.newClient(ctx)
	if err != nil {
		return nil, err
	}
	return d.updateWithClient(ctx, client, request)
}

func (d *Database) updateWithClient(ctx context.Context, client dataAPIClient, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	result, err := d.update(ctx, client, request)
	if err != nil {
		if progress, ok := dataAPIProgressFailure(resource.OperationUpdate, request.NativeID, err); ok {
			return &resource.UpdateResult{ProgressResult: progress}, nil
		}
		return nil, err
	}
	return result, nil
}

func (d *Database) update(ctx context.Context, client dataAPIClient, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	desired, err := d.parseSettings(request.DesiredProperties)
	if err != nil {
		return nil, err
	}
	prior, err := d.parseSettings(request.PriorProperties)
	if err != nil {
		return nil, err
	}

	plugin.LoggerFromContext(ctx).Info("rds: updating database",
		"cluster_arn", desired.clusterArn, "database_name", desired.databaseName)

	// Prove a replacement credential works before the NativeID depends on it.
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

	if desired.owner != prior.owner {
		if err := ensureOwnerMembership(ctx, client, desired); err != nil {
			return nil, err
		}
		statement := fmt.Sprintf("ALTER DATABASE %s OWNER TO %s",
			quoteIdentifier(desired.databaseName), quoteIdentifier(desired.owner))
		if _, err := execute(ctx, client, desired.clusterArn, desired.secretArn, statement, nil); err != nil {
			return nil, fmt.Errorf("failed to reassign database %q to owner %q: %w",
				desired.databaseName, desired.owner, err)
		}
	}

	nativeID := buildNativeID(desired.clusterArn, desired.secretArn, desired.databaseName)
	properties, err := d.readBack(ctx, client, nativeID)
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

func (d *Database) Delete(ctx context.Context, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	client, err := d.newClient(ctx)
	if err != nil {
		return nil, err
	}
	return d.deleteWithClient(ctx, client, request)
}

func (d *Database) deleteWithClient(ctx context.Context, client dataAPIClient, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	result, err := d.delete(ctx, client, request)
	if err != nil {
		if progress, ok := dataAPIProgressFailure(resource.OperationDelete, request.NativeID, err); ok {
			return &resource.DeleteResult{ProgressResult: progress}, nil
		}
		return nil, err
	}
	return result, nil
}

func (d *Database) delete(ctx context.Context, client dataAPIClient, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	clusterArn, secretArn, databaseName, err := parseNativeID(request.NativeID)
	if err != nil {
		return nil, err
	}
	if err := validateIdentifier(databaseName); err != nil {
		return nil, err
	}

	log := plugin.LoggerFromContext(ctx)
	log.Info("rds: dropping database", "cluster_arn", clusterArn, "database_name", databaseName)

	statement := fmt.Sprintf("DROP DATABASE %s", quoteIdentifier(databaseName))
	_, err = execute(ctx, client, clusterArn, secretArn, statement, nil)

	switch {
	case err == nil:
	case isUndefinedObjectError(err):
		// A committed DROP whose response was lost must not fail a retry.
	case isInUseError(err):
		// FORCE terminates the sessions holding the database open, so it is only
		// ever a response to this specific error — and only once.
		log.Warn("rds: database is in use; retrying the drop with FORCE, which terminates its open sessions",
			"cluster_arn", clusterArn, "database_name", databaseName)
		forced := fmt.Sprintf("DROP DATABASE %s WITH (FORCE)", quoteIdentifier(databaseName))
		if _, err := execute(ctx, client, clusterArn, secretArn, forced, nil); err != nil {
			if !isUndefinedObjectError(err) {
				return nil, fmt.Errorf("failed to drop database %q even with FORCE: %w", databaseName, err)
			}
		}
	default:
		return nil, fmt.Errorf("failed to drop database %q: %w", databaseName, err)
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
func (d *Database) Status(_ context.Context, request *resource.StatusRequest) (*resource.StatusResult, error) {
	return synchronousStatus(request), nil
}

// List is registered so an unsupported listing fails loudly here rather than
// falling through to CloudControl, which has never heard of this type.
func (d *Database) List(_ context.Context, _ *resource.ListRequest) (*resource.ListResult, error) {
	return nil, unsupportedListError(databaseType)
}

func (d *Database) newClient(ctx context.Context) (dataAPIClient, error) {
	cfg, err := d.cfg.ToAwsConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to load AWS config: %w", err)
	}
	return rdsdata.NewFromConfig(cfg), nil
}

// readBack re-reads the database so the agent persists actual state.
func (d *Database) readBack(ctx context.Context, client dataAPIClient, nativeID string) (json.RawMessage, error) {
	result, err := d.readWithClient(ctx, client, &resource.ReadRequest{NativeID: nativeID})
	if err != nil {
		return nil, fmt.Errorf("failed to read back database after write: %w", err)
	}
	if result.ErrorCode == resource.OperationErrorCodeNotFound {
		return nil, fmt.Errorf("database not found immediately after write: %s", nativeID)
	}
	return json.RawMessage(result.Properties), nil
}

// lookupDatabaseOwner returns the database's owning role, and whether the
// database exists at all.
func lookupDatabaseOwner(ctx context.Context, client dataAPIClient, clusterArn, secretArn, databaseName string) (owner string, exists bool, err error) {
	out, err := execute(ctx, client, clusterArn, secretArn,
		"SELECT d.datname, r.rolname FROM pg_database d JOIN pg_roles r ON r.oid = d.datdba WHERE d.datname = :name",
		[]rdsdatatypes.SqlParameter{stringParam("name", databaseName)})
	if err != nil {
		return "", false, fmt.Errorf("failed to look up database %q: %w", databaseName, err)
	}

	records, err := decodeRecords(out)
	if err != nil {
		return "", false, err
	}
	if len(records) == 0 {
		return "", false, nil
	}

	return recordString(records[0], "rolname"), true, nil
}
