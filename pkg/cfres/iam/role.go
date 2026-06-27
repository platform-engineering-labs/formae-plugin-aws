// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package iam

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"

	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/ccx"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres/prov"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres/registry"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/config"
)

const roleType = "AWS::IAM::Role"

// roleCCXReader is the generic CloudControl read the custom Role Read delegates to
// before enriching inline policies. *ccx.Client satisfies it.
type roleCCXReader interface {
	ReadResource(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error)
}

// roleClientInterface is the subset of the IAM API used to read a role's inline
// policies. *iam.Client satisfies it.
type roleClientInterface interface {
	ListRolePolicies(ctx context.Context, params *iam.ListRolePoliciesInput, optFns ...func(*iam.Options)) (*iam.ListRolePoliciesOutput, error)
	GetRolePolicy(ctx context.Context, params *iam.GetRolePolicyInput, optFns ...func(*iam.Options)) (*iam.GetRolePolicyOutput, error)
}

// Role provides a custom Read for AWS::IAM::Role that enriches the CloudControl
// read with the role's inline Policies. CloudControl's read model for a role does
// not return inline policies (AWS stores them separately and ccx strips $.Policies
// via IgnoredFields), so without enrichment a role declaring inline `policies`
// shows perpetual phantom drift (a spurious "add Policies" on every reconcile).
//
// Only Read is registered; Create/Update/Delete/List/Status fall through to the
// generic CloudControl path in aws.go. The status path's post-success read also
// routes through this enriched Read (aws.go's Plugin.Status delegates to
// StatusResource with Plugin.Read), so the inline policies are present whenever the
// role's state is persisted.
type Role struct {
	cfg *config.Config
	// ccxClient and iamClient are injectable for testing; nil means construct the
	// real clients.
	ccxClient roleCCXReader
	iamClient roleClientInterface
}

var _ prov.Provisioner = &Role{}

func init() {
	registry.Register(roleType,
		[]resource.Operation{resource.OperationRead},
		func(cfg *config.Config) prov.Provisioner {
			return &Role{cfg: cfg}
		})
}

func (r *Role) getCCXClient() (roleCCXReader, error) {
	if r.ccxClient != nil {
		return r.ccxClient, nil
	}
	return ccx.NewClient(r.cfg)
}

func (r *Role) getIAMClient(ctx context.Context) (roleClientInterface, error) {
	if r.iamClient != nil {
		return r.iamClient, nil
	}
	awsCfg, err := r.cfg.ToAwsConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading AWS config: %w", err)
	}
	return iam.NewFromConfig(awsCfg), nil
}

// Read reads the role via CloudControl and then enriches Properties.Policies with
// the role's inline policies from IAM. The ordering is load-bearing: the
// CloudControl read strips $.Policies (IgnoredFields), so enrichment must happen
// after that strip — otherwise the injected policies would be removed again.
func (r *Role) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	ccxClient, err := r.getCCXClient()
	if err != nil {
		return nil, fmt.Errorf("creating cloudcontrol client: %w", err)
	}
	iamClient, err := r.getIAMClient(ctx)
	if err != nil {
		return nil, err
	}
	return r.readWithClients(ctx, ccxClient, iamClient, request)
}

func (r *Role) readWithClients(ctx context.Context, ccxClient roleCCXReader, iamClient roleClientInterface, request *resource.ReadRequest) (*resource.ReadResult, error) {
	result, err := ccxClient.ReadResource(ctx, request)
	if err != nil {
		return nil, err
	}
	if result.ErrorCode != "" {
		return result, nil
	}

	var props map[string]any
	if err = json.Unmarshal([]byte(result.Properties), &props); err != nil {
		return nil, fmt.Errorf("unmarshal role properties: %w", err)
	}

	roleName, err := roleNameForRead(props, request.NativeID)
	if err != nil {
		return nil, err
	}

	policies, notFound, err := listInlinePolicies(ctx, iamClient, roleName)
	if err != nil {
		return nil, err
	}
	if notFound {
		return &resource.ReadResult{
			ResourceType: request.ResourceType,
			ErrorCode:    resource.OperationErrorCodeNotFound,
		}, nil
	}

	// Only inject Policies when the role actually carries inline policies. A role
	// with none must not gain an empty Policies key: an absent actual field against
	// an absent desired field produces no diff.
	if len(policies) > 0 {
		props["Policies"] = policies
	}

	out, err := json.Marshal(props)
	if err != nil {
		return nil, fmt.Errorf("marshal enriched role properties: %w", err)
	}
	result.Properties = string(out)
	return result, nil
}

// roleNameForRead resolves the role name to query IAM with. It prefers the
// read-back RoleName; for AWS::IAM::Role the CloudControl primary identifier (Ref)
// is the role name, so NativeID is a sound fallback. If neither yields a plain
// role name it fails rather than calling IAM with a guessed identifier.
func roleNameForRead(props map[string]any, nativeID string) (string, error) {
	if rn, ok := props["RoleName"].(string); ok && rn != "" {
		return rn, nil
	}
	if nativeID != "" {
		return nativeID, nil
	}
	return "", fmt.Errorf("cannot determine role name: read-back RoleName empty and NativeID empty")
}

// listInlinePolicies returns the role's inline policies as CloudControl-shaped
// entries ({PolicyName, PolicyDocument}), walking every page of ListRolePolicies
// and resolving each document via GetRolePolicy. notFound is true when the role
// itself no longer exists (a legitimate not-found).
func listInlinePolicies(ctx context.Context, client roleClientInterface, roleName string) (policies []map[string]any, notFound bool, err error) {
	var names []string
	var marker *string
	for {
		input := &iam.ListRolePoliciesInput{RoleName: &roleName}
		if marker != nil {
			input.Marker = marker
		}
		out, err := client.ListRolePolicies(ctx, input)
		if err != nil {
			var noSuchEntity *iamtypes.NoSuchEntityException
			if errors.As(err, &noSuchEntity) {
				return nil, true, nil
			}
			return nil, false, fmt.Errorf("listing inline policies for role %s: %w", roleName, err)
		}
		names = append(names, out.PolicyNames...)
		if !out.IsTruncated || out.Marker == nil {
			break
		}
		marker = out.Marker
	}

	seen := make(map[string]struct{}, len(names))
	policies = make([]map[string]any, 0, len(names))
	for _, name := range names {
		if _, dup := seen[name]; dup {
			return nil, false, fmt.Errorf("duplicate inline policy name %q returned for role %s", name, roleName)
		}
		seen[name] = struct{}{}

		doc, err := getInlinePolicyDocument(ctx, client, roleName, name)
		if err != nil {
			return nil, false, err
		}
		policies = append(policies, map[string]any{
			"PolicyName":     name,
			"PolicyDocument": doc,
		})
	}

	sort.Slice(policies, func(i, j int) bool {
		return policies[i]["PolicyName"].(string) < policies[j]["PolicyName"].(string)
	})
	return policies, false, nil
}

// getInlinePolicyDocument fetches and structurally decodes a single inline policy
// document. IAM returns the document percent-encoded (path style); it is decoded
// with url.PathUnescape (not QueryUnescape, which would corrupt a literal '+') and
// parsed to a structured value so the comparison is on parsed JSON, not strings.
// Decode and parse errors are surfaced rather than swallowed: a nil document
// injected into actual state would be destructive drift, not a benign miss.
func getInlinePolicyDocument(ctx context.Context, client roleClientInterface, roleName, policyName string) (any, error) {
	out, err := client.GetRolePolicy(ctx, &iam.GetRolePolicyInput{
		RoleName:   &roleName,
		PolicyName: &policyName,
	})
	if err != nil {
		var noSuchEntity *iamtypes.NoSuchEntityException
		if errors.As(err, &noSuchEntity) {
			// The policy was named by ListRolePolicies but is not retrievable: a
			// mid-read race. Fail so the read is retried rather than returning a
			// partial actual that would emit a spurious add/remove.
			return nil, fmt.Errorf("inline policy %q listed for role %s but not retrievable (mid-read race): %w", policyName, roleName, err)
		}
		return nil, fmt.Errorf("getting inline policy %q for role %s: %w", policyName, roleName, err)
	}
	if out.PolicyDocument == nil {
		return nil, fmt.Errorf("inline policy %q for role %s returned a nil document", policyName, roleName)
	}

	decoded, err := url.PathUnescape(*out.PolicyDocument)
	if err != nil {
		return nil, fmt.Errorf("decoding inline policy document %q for role %s: %w", policyName, roleName, err)
	}
	var doc any
	if err := json.Unmarshal([]byte(decoded), &doc); err != nil {
		return nil, fmt.Errorf("parsing inline policy document %q for role %s: %w", policyName, roleName, err)
	}
	return doc, nil
}

// The remaining Provisioner methods are unreachable: only Read is registered, so
// Create/Update/Delete/List/Status always route to CloudControl in aws.go.
func (r *Role) Create(_ context.Context, _ *resource.CreateRequest) (*resource.CreateResult, error) {
	return nil, fmt.Errorf("create not implemented - cloudcontrol handles this operation")
}

func (r *Role) Update(_ context.Context, _ *resource.UpdateRequest) (*resource.UpdateResult, error) {
	return nil, fmt.Errorf("update not implemented - cloudcontrol handles this operation")
}

func (r *Role) Delete(_ context.Context, _ *resource.DeleteRequest) (*resource.DeleteResult, error) {
	return nil, fmt.Errorf("delete not implemented - cloudcontrol handles this operation")
}

func (r *Role) Status(_ context.Context, _ *resource.StatusRequest) (*resource.StatusResult, error) {
	return nil, fmt.Errorf("status not implemented - cloudcontrol handles this operation")
}

func (r *Role) List(_ context.Context, _ *resource.ListRequest) (*resource.ListResult, error) {
	return nil, fmt.Errorf("list not implemented - cloudcontrol handles this operation")
}
