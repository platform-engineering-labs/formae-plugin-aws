// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package iam

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/service/iam"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres/prov"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres/registry"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/config"
)

type instanceProfileClientInterface interface {
	AddRoleToInstanceProfile(ctx context.Context, params *iam.AddRoleToInstanceProfileInput, optFns ...func(*iam.Options)) (*iam.AddRoleToInstanceProfileOutput, error)
	RemoveRoleFromInstanceProfile(ctx context.Context, params *iam.RemoveRoleFromInstanceProfileInput, optFns ...func(*iam.Options)) (*iam.RemoveRoleFromInstanceProfileOutput, error)
}

type InstanceProfile struct {
	cfg *config.Config
}

var _ prov.Provisioner = &InstanceProfile{}

func init() {
	registry.Register("AWS::IAM::InstanceProfile",
		[]resource.Operation{resource.OperationUpdate},
		func(cfg *config.Config) prov.Provisioner {
			return &InstanceProfile{cfg: cfg}
		})
}

type instanceProfileProperties struct {
	InstanceProfileName string   `json:"InstanceProfileName"`
	Roles               []string `json:"Roles"`
}

func (ip *InstanceProfile) Update(ctx context.Context, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	awsCfg, err := ip.cfg.ToAwsConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading AWS config: %w", err)
	}
	client := iam.NewFromConfig(awsCfg)
	return ip.updateWithClient(ctx, client, request)
}

func (ip *InstanceProfile) updateWithClient(ctx context.Context, client instanceProfileClientInterface, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	profileName := request.NativeID

	var previous instanceProfileProperties
	if err := json.Unmarshal(request.PriorProperties, &previous); err != nil {
		return nil, fmt.Errorf("parsing prior properties: %w", err)
	}

	var desired instanceProfileProperties
	if err := json.Unmarshal(request.DesiredProperties, &desired); err != nil {
		return nil, fmt.Errorf("parsing desired properties: %w", err)
	}

	currentRoles := toSet(previous.Roles)
	desiredRoles := toSet(desired.Roles)

	// Remove roles that are no longer desired
	for role := range currentRoles {
		if !desiredRoles[role] {
			if _, err := client.RemoveRoleFromInstanceProfile(ctx, &iam.RemoveRoleFromInstanceProfileInput{
				InstanceProfileName: &profileName,
				RoleName:            strPtr(role),
			}); err != nil {
				return nil, fmt.Errorf("removing role %s from instance profile %s: %w", role, profileName, err)
			}
		}
	}

	// Add roles that are newly desired
	for role := range desiredRoles {
		if !currentRoles[role] {
			if _, err := client.AddRoleToInstanceProfile(ctx, &iam.AddRoleToInstanceProfileInput{
				InstanceProfileName: &profileName,
				RoleName:            strPtr(role),
			}); err != nil {
				return nil, fmt.Errorf("adding role %s to instance profile %s: %w", role, profileName, err)
			}
		}
	}

	return &resource.UpdateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationUpdate,
			OperationStatus:    resource.OperationStatusSuccess,
			NativeID:           request.NativeID,
			ResourceProperties: json.RawMessage(request.DesiredProperties),
		},
	}, nil
}

func (ip *InstanceProfile) Create(_ context.Context, _ *resource.CreateRequest) (*resource.CreateResult, error) {
	return nil, fmt.Errorf("operation not implemented - cloudcontrol handles this")
}

func (ip *InstanceProfile) Read(_ context.Context, _ *resource.ReadRequest) (*resource.ReadResult, error) {
	return nil, fmt.Errorf("operation not implemented - cloudcontrol handles this")
}

func (ip *InstanceProfile) Delete(_ context.Context, _ *resource.DeleteRequest) (*resource.DeleteResult, error) {
	return nil, fmt.Errorf("operation not implemented - cloudcontrol handles this")
}

func (ip *InstanceProfile) Status(_ context.Context, _ *resource.StatusRequest) (*resource.StatusResult, error) {
	return nil, fmt.Errorf("operation not implemented - cloudcontrol handles this")
}

func (ip *InstanceProfile) List(_ context.Context, _ *resource.ListRequest) (*resource.ListResult, error) {
	return nil, fmt.Errorf("operation not implemented - cloudcontrol handles this")
}

func toSet(items []string) map[string]bool {
	s := make(map[string]bool, len(items))
	for _, item := range items {
		s[item] = true
	}
	return s
}

func strPtr(s string) *string {
	return &s
}
