// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package apigateway

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/arn"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	"github.com/platform-engineering-labs/formae/pkg/plugin"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/ccx"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres/prov"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres/registry"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/config"
)

// stsClientInterface is the narrow STS surface used to resolve the account ID
// and partition for the derived execute-api ARN. Defined here (rather than
// aliased from the SDK) so unit tests can inject a mock.
type stsClientInterface interface {
	GetCallerIdentity(ctx context.Context, params *sts.GetCallerIdentityInput, optFns ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error)
}

// RestApi wraps CloudControl's Read for AWS::ApiGateway::RestApi to derive an
// ExecuteApiArn. CloudControl's read model returns only RestApiId and
// RootResourceId — there is no ARN property — so a Lambda Permission that wants
// to scope its sourceArn to this API has to hand-build the ARN. The derived
// resolvable (schema/pkl/apigateway/restapi.pkl executeApiArn) closes that gap
// without the operator constructing it from account/region by hand.
//
// All other operations (Create / Update / Delete / List / Status) fall through
// to CloudControl because only the Read result needs enrichment.
type RestApi struct {
	cfg *config.Config
	// stsClientFactory builds the STS client used to resolve the account and
	// partition. Tests inject a fake; production uses the default factory.
	stsClientFactory func(cfg *config.Config) (stsClientInterface, error)
}

var _ prov.Provisioner = &RestApi{}

func init() {
	registry.Register("AWS::ApiGateway::RestApi",
		[]resource.Operation{resource.OperationRead},
		func(cfg *config.Config) prov.Provisioner {
			return &RestApi{cfg: cfg, stsClientFactory: defaultStsClientFactory}
		})
}

func defaultStsClientFactory(cfg *config.Config) (stsClientInterface, error) {
	awsCfg, err := cfg.ToAwsConfig(context.Background())
	if err != nil {
		return nil, fmt.Errorf("apigateway restapi: build AWS config: %w", err)
	}
	return sts.NewFromConfig(awsCfg), nil
}

func (r *RestApi) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	client, err := ccx.NewClient(r.cfg)
	if err != nil {
		return nil, err
	}
	result, err := client.ReadResource(ctx, request)
	if err != nil || result == nil || result.Properties == "" {
		return result, err
	}

	var props map[string]any
	if err := json.Unmarshal([]byte(result.Properties), &props); err != nil {
		// Pass through; CCAPI's representation is the source of truth.
		return result, nil
	}

	r.enrichWithExecuteApiArn(ctx, props)

	enriched, err := json.Marshal(props)
	if err != nil {
		return nil, fmt.Errorf("apigateway restapi: re-marshal enriched properties: %w", err)
	}
	result.Properties = string(enriched)
	return result, nil
}

// callerIdentity is the account ID and partition resolved from STS. Both are
// invariant for a given credential set, so they are memoized (see identityCache)
// to avoid a GetCallerIdentity round-trip on every Read.
type callerIdentity struct {
	account   string
	partition string
}

// identityCacheKey scopes the memo to a credential set. The plugin Config
// exposes only Profile and Region, which together determine the account
// (credentials) and partition (region) — so this is the finest credential
// distinction available, and keying by it keeps the cache multi-account-safe.
type identityCacheKey struct {
	profile string
	region  string
}

var (
	identityCacheMu sync.Mutex
	identityCache   = map[identityCacheKey]callerIdentity{}
)

// enrichWithExecuteApiArn resolves the account ID and partition (memoized per
// credential set) and derives the execute-api ARN onto props. On any failure
// (STS error or missing inputs) it logs a WARN naming the cause and leaves props
// unchanged — the resolvable then resolves to null, surfacing an apply-time
// error to a consumer that needs it rather than a silent pass-through.
func (r *RestApi) enrichWithExecuteApiArn(ctx context.Context, props map[string]any) {
	if _, exists := props["ExecuteApiArn"]; exists {
		// Never overwrite a value a future CloudControl schema might supply.
		return
	}
	identity, err := r.resolveCallerIdentity(ctx)
	if err != nil {
		plugin.LoggerFromContext(ctx).Warn("apigateway restapi: caller identity unavailable; execute-api ARN not derived", "error", err)
		return
	}
	if !synthesizeExecuteApiArn(props, identity.partition, r.cfg.Region, identity.account) {
		plugin.LoggerFromContext(ctx).Warn("apigateway restapi: execute-api ARN not derived (missing region/account/RestApiId)",
			"region", r.cfg.Region, "account", identity.account)
	}
}

// resolveCallerIdentity returns the account and partition for the plugin's
// credentials, memoized by (profile, region). On a cache miss it builds the STS
// client and calls GetCallerIdentity once; the result is invariant for the
// credential set, so every subsequent Read reuses it. A failed lookup is not
// cached, so a transient STS error can be retried on the next Read.
func (r *RestApi) resolveCallerIdentity(ctx context.Context) (callerIdentity, error) {
	key := identityCacheKey{profile: r.cfg.Profile, region: r.cfg.Region}

	identityCacheMu.Lock()
	cached, ok := identityCache[key]
	identityCacheMu.Unlock()
	if ok {
		return cached, nil
	}

	stsClient, err := r.stsClientFactory(r.cfg)
	if err != nil {
		return callerIdentity{}, fmt.Errorf("build STS client: %w", err)
	}
	caller, err := stsClient.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return callerIdentity{}, err
	}
	identity := callerIdentity{
		account:   aws.ToString(caller.Account),
		partition: partitionFromArn(aws.ToString(caller.Arn)),
	}

	identityCacheMu.Lock()
	identityCache[key] = identity
	identityCacheMu.Unlock()
	return identity, nil
}

// synthesizeExecuteApiArn builds the execute-api ARN
// arn:<partition>:execute-api:<region>:<account>:<restApiId>/*/* from props'
// RestApiId plus the supplied partition/region/account, and writes it to props
// as ExecuteApiArn. It sets the key only when every component is non-empty and
// the key is not already present (never overwriting a CloudControl-supplied
// value). Reports whether the key was set. The trailing /*/* scopes all
// stages, methods, and resource paths.
func synthesizeExecuteApiArn(props map[string]any, partition, region, account string) bool {
	if _, exists := props["ExecuteApiArn"]; exists {
		return false
	}
	restApiID, _ := props["RestApiId"].(string)
	if partition == "" || region == "" || account == "" || restApiID == "" {
		return false
	}
	props["ExecuteApiArn"] = fmt.Sprintf("arn:%s:execute-api:%s:%s:%s/*/*", partition, region, account, restApiID)
	return true
}

// partitionFromArn extracts the AWS partition (e.g. aws, aws-us-gov, aws-cn)
// from an ARN, defaulting to "aws" when the value can't be parsed.
func partitionFromArn(s string) string {
	parsed, err := arn.Parse(s)
	if err != nil || parsed.Partition == "" {
		return "aws"
	}
	return parsed.Partition
}

// The remaining operations fall through to CloudControl; they are unimplemented
// here so the dispatcher in aws.go bypasses this provisioner for them.

func (r *RestApi) Create(ctx context.Context, request *resource.CreateRequest) (*resource.CreateResult, error) {
	return nil, fmt.Errorf("apigateway restapi: create handled by cloudcontrol")
}

func (r *RestApi) Update(ctx context.Context, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	return nil, fmt.Errorf("apigateway restapi: update handled by cloudcontrol")
}

func (r *RestApi) Delete(ctx context.Context, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	return nil, fmt.Errorf("apigateway restapi: delete handled by cloudcontrol")
}

func (r *RestApi) Status(ctx context.Context, request *resource.StatusRequest) (*resource.StatusResult, error) {
	return nil, fmt.Errorf("apigateway restapi: status handled by cloudcontrol")
}

func (r *RestApi) List(ctx context.Context, request *resource.ListRequest) (*resource.ListResult, error) {
	return nil, fmt.Errorf("apigateway restapi: list handled by cloudcontrol")
}
