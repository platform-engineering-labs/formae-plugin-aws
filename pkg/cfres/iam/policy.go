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
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres/prov"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres/registry"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/config"
)

type policyClientInterface interface {
	PutRolePolicy(ctx context.Context, params *iam.PutRolePolicyInput, optFns ...func(*iam.Options)) (*iam.PutRolePolicyOutput, error)
	GetRolePolicy(ctx context.Context, params *iam.GetRolePolicyInput, optFns ...func(*iam.Options)) (*iam.GetRolePolicyOutput, error)
	DeleteRolePolicy(ctx context.Context, params *iam.DeleteRolePolicyInput, optFns ...func(*iam.Options)) (*iam.DeleteRolePolicyOutput, error)
	PutUserPolicy(ctx context.Context, params *iam.PutUserPolicyInput, optFns ...func(*iam.Options)) (*iam.PutUserPolicyOutput, error)
	GetUserPolicy(ctx context.Context, params *iam.GetUserPolicyInput, optFns ...func(*iam.Options)) (*iam.GetUserPolicyOutput, error)
	DeleteUserPolicy(ctx context.Context, params *iam.DeleteUserPolicyInput, optFns ...func(*iam.Options)) (*iam.DeleteUserPolicyOutput, error)
	PutGroupPolicy(ctx context.Context, params *iam.PutGroupPolicyInput, optFns ...func(*iam.Options)) (*iam.PutGroupPolicyOutput, error)
	GetGroupPolicy(ctx context.Context, params *iam.GetGroupPolicyInput, optFns ...func(*iam.Options)) (*iam.GetGroupPolicyOutput, error)
	DeleteGroupPolicy(ctx context.Context, params *iam.DeleteGroupPolicyInput, optFns ...func(*iam.Options)) (*iam.DeleteGroupPolicyOutput, error)
}

type Policy struct {
	cfg *config.Config
}

var _ prov.Provisioner = &Policy{}

func init() {
	registry.Register("AWS::IAM::Policy",
		[]resource.Operation{
			resource.OperationCreate,
			resource.OperationRead,
			resource.OperationUpdate,
			resource.OperationDelete,
			resource.OperationCheckStatus,
		},
		func(cfg *config.Config) prov.Provisioner {
			return &Policy{cfg: cfg}
		})
}

func (p *Policy) Create(ctx context.Context, request *resource.CreateRequest) (*resource.CreateResult, error) {
	awsCfg, err := p.cfg.ToAwsConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading AWS config: %w", err)
	}
	return p.createWithClient(ctx, iam.NewFromConfig(awsCfg), request)
}

func (p *Policy) createWithClient(ctx context.Context, client policyClientInterface, request *resource.CreateRequest) (*resource.CreateResult, error) {
	policyName, policyDocJSON, roles, users, groups, err := parsePolicyProperties(request.Properties)
	if err != nil {
		return nil, err
	}

	if err := putPolicyOnTargets(ctx, client, policyName, policyDocJSON, roles, users, groups); err != nil {
		return nil, err
	}

	nativeID := buildPolicyNativeID(policyName, roles, users, groups)

	var policyDoc any
	_ = json.Unmarshal([]byte(policyDocJSON), &policyDoc)
	resultProps := map[string]any{
		"PolicyName":     policyName,
		"PolicyDocument": policyDoc,
	}
	if len(roles) > 0 {
		resultProps["Roles"] = roles
	}
	if len(users) > 0 {
		resultProps["Users"] = users
	}
	if len(groups) > 0 {
		resultProps["Groups"] = groups
	}
	resultJSON, _ := json.Marshal(resultProps)

	return &resource.CreateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationCreate,
			OperationStatus:    resource.OperationStatusSuccess,
			NativeID:           nativeID,
			ResourceProperties: resultJSON,
		},
	}, nil
}

func (p *Policy) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	awsCfg, err := p.cfg.ToAwsConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading AWS config: %w", err)
	}
	return p.readWithClient(ctx, iam.NewFromConfig(awsCfg), request)
}

func (p *Policy) readWithClient(ctx context.Context, client policyClientInterface, request *resource.ReadRequest) (*resource.ReadResult, error) {
	policyName, roles, users, groups, err := parsePolicyNativeID(request.NativeID)
	if err != nil {
		return nil, err
	}

	// Get the policy document from the first available target
	var policyDocJSON string
	var found bool

	if len(roles) > 0 {
		output, err := client.GetRolePolicy(ctx, &iam.GetRolePolicyInput{
			RoleName:   &roles[0],
			PolicyName: &policyName,
		})
		if err != nil {
			var noSuchEntity *iamtypes.NoSuchEntityException
			if errors.As(err, &noSuchEntity) {
				return &resource.ReadResult{
					ResourceType: request.ResourceType,
					ErrorCode:    resource.OperationErrorCodeNotFound,
				}, nil
			}
			return nil, fmt.Errorf("getting role policy: %w", err)
		}
		decoded, _ := url.QueryUnescape(*output.PolicyDocument)
		policyDocJSON = decoded
		found = true
	} else if len(users) > 0 {
		output, err := client.GetUserPolicy(ctx, &iam.GetUserPolicyInput{
			UserName:   &users[0],
			PolicyName: &policyName,
		})
		if err != nil {
			var noSuchEntity *iamtypes.NoSuchEntityException
			if errors.As(err, &noSuchEntity) {
				return &resource.ReadResult{
					ResourceType: request.ResourceType,
					ErrorCode:    resource.OperationErrorCodeNotFound,
				}, nil
			}
			return nil, fmt.Errorf("getting user policy: %w", err)
		}
		decoded, _ := url.QueryUnescape(*output.PolicyDocument)
		policyDocJSON = decoded
		found = true
	} else if len(groups) > 0 {
		output, err := client.GetGroupPolicy(ctx, &iam.GetGroupPolicyInput{
			GroupName:  &groups[0],
			PolicyName: &policyName,
		})
		if err != nil {
			var noSuchEntity *iamtypes.NoSuchEntityException
			if errors.As(err, &noSuchEntity) {
				return &resource.ReadResult{
					ResourceType: request.ResourceType,
					ErrorCode:    resource.OperationErrorCodeNotFound,
				}, nil
			}
			return nil, fmt.Errorf("getting group policy: %w", err)
		}
		decoded, _ := url.QueryUnescape(*output.PolicyDocument)
		policyDocJSON = decoded
		found = true
	}

	if !found {
		return &resource.ReadResult{
			ResourceType: request.ResourceType,
			ErrorCode:    resource.OperationErrorCodeNotFound,
		}, nil
	}

	var policyDoc any
	_ = json.Unmarshal([]byte(policyDocJSON), &policyDoc)

	props := map[string]any{
		"PolicyName":     policyName,
		"PolicyDocument": policyDoc,
	}
	if len(roles) > 0 {
		props["Roles"] = roles
	}
	if len(users) > 0 {
		props["Users"] = users
	}
	if len(groups) > 0 {
		props["Groups"] = groups
	}
	propsJSON, _ := json.Marshal(props)

	return &resource.ReadResult{
		ResourceType: request.ResourceType,
		Properties:   string(propsJSON),
	}, nil
}

func (p *Policy) Update(ctx context.Context, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	awsCfg, err := p.cfg.ToAwsConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading AWS config: %w", err)
	}
	return p.updateWithClient(ctx, iam.NewFromConfig(awsCfg), request)
}

func (p *Policy) updateWithClient(ctx context.Context, client policyClientInterface, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	currentPolicyName, currentRoles, currentUsers, currentGroups, err := parsePolicyNativeID(request.NativeID)
	if err != nil {
		return nil, err
	}

	newPolicyName, policyDocJSON, newRoles, newUsers, newGroups, err := parsePolicyProperties(request.DesiredProperties)
	if err != nil {
		return nil, err
	}

	// Remove policy from targets no longer in the desired list
	removedRoles := diff(currentRoles, newRoles)
	for _, role := range removedRoles {
		if _, err := client.DeleteRolePolicy(ctx, &iam.DeleteRolePolicyInput{
			RoleName:   &role,
			PolicyName: &currentPolicyName,
		}); err != nil {
			var noSuchEntity *iamtypes.NoSuchEntityException
			if !errors.As(err, &noSuchEntity) {
				return nil, fmt.Errorf("removing policy from role %s: %w", role, err)
			}
		}
	}

	removedUsers := diff(currentUsers, newUsers)
	for _, user := range removedUsers {
		if _, err := client.DeleteUserPolicy(ctx, &iam.DeleteUserPolicyInput{
			UserName:   &user,
			PolicyName: &currentPolicyName,
		}); err != nil {
			var noSuchEntity *iamtypes.NoSuchEntityException
			if !errors.As(err, &noSuchEntity) {
				return nil, fmt.Errorf("removing policy from user %s: %w", user, err)
			}
		}
	}

	removedGroups := diff(currentGroups, newGroups)
	for _, group := range removedGroups {
		if _, err := client.DeleteGroupPolicy(ctx, &iam.DeleteGroupPolicyInput{
			GroupName:  &group,
			PolicyName: &currentPolicyName,
		}); err != nil {
			var noSuchEntity *iamtypes.NoSuchEntityException
			if !errors.As(err, &noSuchEntity) {
				return nil, fmt.Errorf("removing policy from group %s: %w", group, err)
			}
		}
	}

	// Apply policy to all desired targets
	if err := putPolicyOnTargets(ctx, client, newPolicyName, policyDocJSON, newRoles, newUsers, newGroups); err != nil {
		return nil, err
	}

	newNativeID := buildPolicyNativeID(newPolicyName, newRoles, newUsers, newGroups)

	// Post-update Read
	readResult, err := p.readWithClient(ctx, client, &resource.ReadRequest{
		NativeID:     newNativeID,
		ResourceType: request.ResourceType,
	})
	var resultProps json.RawMessage
	if err == nil && readResult.ErrorCode == "" {
		resultProps = json.RawMessage(readResult.Properties)
	}

	return &resource.UpdateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationUpdate,
			OperationStatus:    resource.OperationStatusSuccess,
			NativeID:           newNativeID,
			ResourceProperties: resultProps,
		},
	}, nil
}

func (p *Policy) Delete(ctx context.Context, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	awsCfg, err := p.cfg.ToAwsConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading AWS config: %w", err)
	}
	return p.deleteWithClient(ctx, iam.NewFromConfig(awsCfg), request)
}

func (p *Policy) deleteWithClient(ctx context.Context, client policyClientInterface, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	policyName, roles, users, groups, err := parsePolicyNativeID(request.NativeID)
	if err != nil {
		return nil, err
	}

	for _, role := range roles {
		if _, err := client.DeleteRolePolicy(ctx, &iam.DeleteRolePolicyInput{
			RoleName:   &role,
			PolicyName: &policyName,
		}); err != nil {
			var noSuchEntity *iamtypes.NoSuchEntityException
			if !errors.As(err, &noSuchEntity) {
				return nil, fmt.Errorf("deleting role policy from %s: %w", role, err)
			}
		}
	}

	for _, user := range users {
		if _, err := client.DeleteUserPolicy(ctx, &iam.DeleteUserPolicyInput{
			UserName:   &user,
			PolicyName: &policyName,
		}); err != nil {
			var noSuchEntity *iamtypes.NoSuchEntityException
			if !errors.As(err, &noSuchEntity) {
				return nil, fmt.Errorf("deleting user policy from %s: %w", user, err)
			}
		}
	}

	for _, group := range groups {
		if _, err := client.DeleteGroupPolicy(ctx, &iam.DeleteGroupPolicyInput{
			GroupName:  &group,
			PolicyName: &policyName,
		}); err != nil {
			var noSuchEntity *iamtypes.NoSuchEntityException
			if !errors.As(err, &noSuchEntity) {
				return nil, fmt.Errorf("deleting group policy from %s: %w", group, err)
			}
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

func (p *Policy) Status(_ context.Context, _ *resource.StatusRequest) (*resource.StatusResult, error) {
	return nil, fmt.Errorf("policy operations are synchronous - status polling not needed")
}

func (p *Policy) List(_ context.Context, _ *resource.ListRequest) (*resource.ListResult, error) {
	return nil, fmt.Errorf("operation not implemented - policy is not discoverable")
}

// parsePolicyNativeID parses "policyName|R:role1,role2|U:user1|G:group1,group2"
func parsePolicyNativeID(nativeID string) (policyName string, roles, users, groups []string, err error) {
	parts := strings.Split(nativeID, "|")
	if len(parts) < 2 {
		return "", nil, nil, nil, fmt.Errorf("invalid NativeID %q: expected policyName|R:roles|U:users|G:groups", nativeID)
	}

	policyName = parts[0]
	for _, part := range parts[1:] {
		prefix, value, ok := strings.Cut(part, ":")
		if !ok {
			continue
		}
		targets := strings.Split(value, ",")
		switch prefix {
		case "R":
			roles = targets
		case "U":
			users = targets
		case "G":
			groups = targets
		}
	}

	return policyName, roles, users, groups, nil
}

// buildPolicyNativeID constructs "policyName|R:role1,role2|U:user1|G:group1"
func buildPolicyNativeID(policyName string, roles, users, groups []string) string {
	parts := []string{policyName}
	if len(roles) > 0 {
		parts = append(parts, "R:"+strings.Join(roles, ","))
	}
	if len(users) > 0 {
		parts = append(parts, "U:"+strings.Join(users, ","))
	}
	if len(groups) > 0 {
		parts = append(parts, "G:"+strings.Join(groups, ","))
	}
	return strings.Join(parts, "|")
}

func parsePolicyProperties(raw json.RawMessage) (policyName string, policyDocJSON string, roles, users, groups []string, err error) {
	var props map[string]any
	if err := json.Unmarshal(raw, &props); err != nil {
		return "", "", nil, nil, nil, fmt.Errorf("parsing properties: %w", err)
	}

	policyName, _ = props["PolicyName"].(string)
	if policyName == "" {
		return "", "", nil, nil, nil, fmt.Errorf("PolicyName is required")
	}

	policyDoc, ok := props["PolicyDocument"]
	if !ok {
		return "", "", nil, nil, nil, fmt.Errorf("PolicyDocument is required")
	}
	docJSON, err := json.Marshal(policyDoc)
	if err != nil {
		return "", "", nil, nil, nil, fmt.Errorf("marshalling policy document: %w", err)
	}

	roles = toStringSlice(props["Roles"])
	users = toStringSlice(props["Users"])
	groups = toStringSlice(props["Groups"])

	if len(roles) == 0 && len(users) == 0 && len(groups) == 0 {
		return "", "", nil, nil, nil, fmt.Errorf("at least one of Roles, Users, or Groups must be specified")
	}

	return policyName, string(docJSON), roles, users, groups, nil
}

func putPolicyOnTargets(ctx context.Context, client policyClientInterface, policyName, policyDocJSON string, roles, users, groups []string) error {
	for _, role := range roles {
		if _, err := client.PutRolePolicy(ctx, &iam.PutRolePolicyInput{
			RoleName:       &role,
			PolicyName:     &policyName,
			PolicyDocument: &policyDocJSON,
		}); err != nil {
			return fmt.Errorf("putting policy on role %s: %w", role, err)
		}
	}
	for _, user := range users {
		if _, err := client.PutUserPolicy(ctx, &iam.PutUserPolicyInput{
			UserName:       &user,
			PolicyName:     &policyName,
			PolicyDocument: &policyDocJSON,
		}); err != nil {
			return fmt.Errorf("putting policy on user %s: %w", user, err)
		}
	}
	for _, group := range groups {
		if _, err := client.PutGroupPolicy(ctx, &iam.PutGroupPolicyInput{
			GroupName:      &group,
			PolicyName:     &policyName,
			PolicyDocument: &policyDocJSON,
		}); err != nil {
			return fmt.Errorf("putting policy on group %s: %w", group, err)
		}
	}
	return nil
}

func toStringSlice(v any) []string {
	if v == nil {
		return nil
	}
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(arr))
	for _, item := range arr {
		if s, ok := item.(string); ok {
			result = append(result, s)
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

// diff returns items in a that are not in b.
func diff(a, b []string) []string {
	bSet := make(map[string]bool, len(b))
	for _, item := range b {
		bSet[item] = true
	}
	var result []string
	for _, item := range a {
		if !bSet[item] {
			result = append(result, item)
		}
	}
	return result
}
