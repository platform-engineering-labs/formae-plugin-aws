// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package ec2

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	ec2sdk "github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/smithy-go"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres/prov"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres/registry"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/config"
)

// errCodeNetworkInterfacePermissionNotFound is the EC2 error code returned when
// a permission id does not exist; it is the only code that maps to NotFound.
const errCodeNetworkInterfacePermissionNotFound = "InvalidNetworkInterfacePermissionID.NotFound"

type networkInterfacePermissionClientInterface interface {
	CreateNetworkInterfacePermission(ctx context.Context, params *ec2sdk.CreateNetworkInterfacePermissionInput, optFns ...func(*ec2sdk.Options)) (*ec2sdk.CreateNetworkInterfacePermissionOutput, error)
	DescribeNetworkInterfacePermissions(ctx context.Context, params *ec2sdk.DescribeNetworkInterfacePermissionsInput, optFns ...func(*ec2sdk.Options)) (*ec2sdk.DescribeNetworkInterfacePermissionsOutput, error)
	DeleteNetworkInterfacePermission(ctx context.Context, params *ec2sdk.DeleteNetworkInterfacePermissionInput, optFns ...func(*ec2sdk.Options)) (*ec2sdk.DeleteNetworkInterfacePermissionOutput, error)
}

type NetworkInterfacePermission struct {
	cfg *config.Config
}

var _ prov.Provisioner = &NetworkInterfacePermission{}

func init() {
	registry.Register("AWS::EC2::NetworkInterfacePermission",
		[]resource.Operation{
			resource.OperationCreate,
			resource.OperationRead,
			resource.OperationDelete,
		},
		func(cfg *config.Config) prov.Provisioner {
			return &NetworkInterfacePermission{cfg: cfg}
		})
}

// parseNetworkInterfacePermissionNativeID returns the single permission id
// (eni-perm-...). The id is server-assigned, so there is no composite key.
func parseNetworkInterfacePermissionNativeID(nativeID string) (string, error) {
	if nativeID == "" {
		return "", fmt.Errorf("invalid NativeID: permission id is empty")
	}
	return nativeID, nil
}

// isNetworkInterfacePermissionNotFound reports whether err is the EC2
// "permission id does not exist" error. Every other AWS error is surfaced.
func isNetworkInterfacePermissionNotFound(err error) bool {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		return apiErr.ErrorCode() == errCodeNetworkInterfacePermissionNotFound
	}
	return false
}

func (nip *NetworkInterfacePermission) Create(ctx context.Context, request *resource.CreateRequest) (*resource.CreateResult, error) {
	awsCfg, err := nip.cfg.ToAwsConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading AWS config: %w", err)
	}
	return nip.createWithClient(ctx, ec2sdk.NewFromConfig(awsCfg), request)
}

func (nip *NetworkInterfacePermission) createWithClient(ctx context.Context, client networkInterfacePermissionClientInterface, request *resource.CreateRequest) (*resource.CreateResult, error) {
	var props map[string]any
	if err := json.Unmarshal(request.Properties, &props); err != nil {
		return nil, fmt.Errorf("parsing properties: %w", err)
	}

	networkInterfaceID, _ := props["NetworkInterfaceId"].(string)
	if networkInterfaceID == "" {
		return nil, fmt.Errorf("NetworkInterfaceId is required")
	}
	awsAccountID, _ := props["AwsAccountId"].(string)
	if awsAccountID == "" {
		return nil, fmt.Errorf("AwsAccountId is required")
	}
	permission, _ := props["Permission"].(string)
	if permission == "" {
		return nil, fmt.Errorf("permission is required")
	}

	// Pre-create lookup: the permission id is server-assigned, so a Create
	// retried after a transient-but-actually-successful call has no idempotency
	// key. Adopt an existing matching grant instead of creating a duplicate.
	existing, err := client.DescribeNetworkInterfacePermissions(ctx, &ec2sdk.DescribeNetworkInterfacePermissionsInput{
		Filters: []ec2types.Filter{
			{Name: aws.String("network-interface-permission.network-interface-id"), Values: []string{networkInterfaceID}},
			{Name: aws.String("network-interface-permission.aws-account-id"), Values: []string{awsAccountID}},
			{Name: aws.String("network-interface-permission.permission"), Values: []string{permission}},
		},
	})
	if err != nil && !isNetworkInterfacePermissionNotFound(err) {
		return nil, fmt.Errorf("looking up existing network interface permission: %w", err)
	}
	if err == nil {
		for _, p := range existing.NetworkInterfacePermissions {
			if p.NetworkInterfacePermissionId != nil {
				return networkInterfacePermissionResult(&p), nil
			}
		}
	}

	output, err := client.CreateNetworkInterfacePermission(ctx, &ec2sdk.CreateNetworkInterfacePermissionInput{
		NetworkInterfaceId: aws.String(networkInterfaceID),
		AwsAccountId:       aws.String(awsAccountID),
		Permission:         ec2types.InterfacePermissionType(permission),
	})
	if err != nil {
		return nil, fmt.Errorf("creating network interface permission: %w", err)
	}
	if output.InterfacePermission == nil || output.InterfacePermission.NetworkInterfacePermissionId == nil {
		return nil, fmt.Errorf("creating network interface permission: response did not include a permission id")
	}

	return networkInterfacePermissionResult(output.InterfacePermission), nil
}

// networkInterfacePermissionResult builds a successful CreateResult straight
// from the permission, so success and the NativeID never depend on a follow-up
// read.
func networkInterfacePermissionResult(p *ec2types.NetworkInterfacePermission) *resource.CreateResult {
	id := *p.NetworkInterfacePermissionId
	resultJSON, _ := json.Marshal(networkInterfacePermissionProps(p))
	return &resource.CreateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationCreate,
			OperationStatus:    resource.OperationStatusSuccess,
			NativeID:           id,
			ResourceProperties: resultJSON,
		},
	}
}

func networkInterfacePermissionProps(p *ec2types.NetworkInterfacePermission) map[string]any {
	props := map[string]any{
		"Id":         *p.NetworkInterfacePermissionId,
		"Permission": string(p.Permission),
	}
	if p.NetworkInterfaceId != nil {
		props["NetworkInterfaceId"] = *p.NetworkInterfaceId
	}
	if p.AwsAccountId != nil {
		props["AwsAccountId"] = *p.AwsAccountId
	}
	return props
}

func (nip *NetworkInterfacePermission) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	awsCfg, err := nip.cfg.ToAwsConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading AWS config: %w", err)
	}
	return nip.readWithClient(ctx, ec2sdk.NewFromConfig(awsCfg), request)
}

func (nip *NetworkInterfacePermission) readWithClient(ctx context.Context, client networkInterfacePermissionClientInterface, request *resource.ReadRequest) (*resource.ReadResult, error) {
	id, err := parseNetworkInterfacePermissionNativeID(request.NativeID)
	if err != nil {
		return nil, err
	}

	resp, err := client.DescribeNetworkInterfacePermissions(ctx, &ec2sdk.DescribeNetworkInterfacePermissionsInput{
		NetworkInterfacePermissionIds: []string{id},
	})
	if err != nil {
		if isNetworkInterfacePermissionNotFound(err) {
			return &resource.ReadResult{
				ResourceType: request.ResourceType,
				ErrorCode:    resource.OperationErrorCodeNotFound,
			}, nil
		}
		return nil, fmt.Errorf("describing network interface permission %s: %w", id, err)
	}

	if len(resp.NetworkInterfacePermissions) == 0 {
		return &resource.ReadResult{
			ResourceType: request.ResourceType,
			ErrorCode:    resource.OperationErrorCodeNotFound,
		}, nil
	}

	p := resp.NetworkInterfacePermissions[0]
	propsJSON, err := json.Marshal(networkInterfacePermissionProps(&p))
	if err != nil {
		return nil, fmt.Errorf("marshaling properties: %w", err)
	}
	return &resource.ReadResult{
		ResourceType: request.ResourceType,
		Properties:   string(propsJSON),
	}, nil
}

func (nip *NetworkInterfacePermission) Delete(ctx context.Context, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	awsCfg, err := nip.cfg.ToAwsConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading AWS config: %w", err)
	}
	return nip.deleteWithClient(ctx, ec2sdk.NewFromConfig(awsCfg), request)
}

func (nip *NetworkInterfacePermission) deleteWithClient(ctx context.Context, client networkInterfacePermissionClientInterface, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	id, err := parseNetworkInterfacePermissionNativeID(request.NativeID)
	if err != nil {
		return nil, err
	}

	// Read-before-delete: an already-absent grant is a successful (idempotent)
	// delete.
	readResult, err := nip.readWithClient(ctx, client, &resource.ReadRequest{
		NativeID:     request.NativeID,
		ResourceType: request.ResourceType,
	})
	if err != nil {
		return nil, err
	}
	if readResult.ErrorCode == resource.OperationErrorCodeNotFound {
		return networkInterfacePermissionDeleteSuccess(request.NativeID), nil
	}

	if _, err := client.DeleteNetworkInterfacePermission(ctx, &ec2sdk.DeleteNetworkInterfacePermissionInput{
		NetworkInterfacePermissionId: aws.String(id),
	}); err != nil {
		if isNetworkInterfacePermissionNotFound(err) {
			return networkInterfacePermissionDeleteSuccess(request.NativeID), nil
		}
		return nil, fmt.Errorf("deleting network interface permission %s: %w", id, err)
	}

	return networkInterfacePermissionDeleteSuccess(request.NativeID), nil
}

func networkInterfacePermissionDeleteSuccess(nativeID string) *resource.DeleteResult {
	return &resource.DeleteResult{
		ProgressResult: &resource.ProgressResult{
			Operation:       resource.OperationDelete,
			OperationStatus: resource.OperationStatusSuccess,
			NativeID:        nativeID,
		},
	}
}

func (nip *NetworkInterfacePermission) Update(_ context.Context, _ *resource.UpdateRequest) (*resource.UpdateResult, error) {
	return nil, fmt.Errorf("update is not implemented for AWS::EC2::NetworkInterfacePermission: all fields are createOnly, so a change is a replace")
}

func (nip *NetworkInterfacePermission) Status(_ context.Context, _ *resource.StatusRequest) (*resource.StatusResult, error) {
	return nil, fmt.Errorf("status check is not implemented for AWS::EC2::NetworkInterfacePermission")
}

func (nip *NetworkInterfacePermission) List(_ context.Context, _ *resource.ListRequest) (*resource.ListResult, error) {
	return &resource.ListResult{
		NativeIDs: []string{},
	}, nil
}
