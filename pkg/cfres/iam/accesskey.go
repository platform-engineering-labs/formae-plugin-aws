// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package iam

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres/prov"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres/registry"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/config"
)

type accessKeyClientInterface interface {
	CreateAccessKey(ctx context.Context, params *iam.CreateAccessKeyInput, optFns ...func(*iam.Options)) (*iam.CreateAccessKeyOutput, error)
	UpdateAccessKey(ctx context.Context, params *iam.UpdateAccessKeyInput, optFns ...func(*iam.Options)) (*iam.UpdateAccessKeyOutput, error)
	DeleteAccessKey(ctx context.Context, params *iam.DeleteAccessKeyInput, optFns ...func(*iam.Options)) (*iam.DeleteAccessKeyOutput, error)
	ListAccessKeys(ctx context.Context, params *iam.ListAccessKeysInput, optFns ...func(*iam.Options)) (*iam.ListAccessKeysOutput, error)
}

type AccessKey struct {
	cfg *config.Config
}

var _ prov.Provisioner = &AccessKey{}

func init() {
	registry.Register("AWS::IAM::AccessKey",
		[]resource.Operation{
			resource.OperationCreate,
			resource.OperationRead,
			resource.OperationUpdate,
			resource.OperationDelete,
			resource.OperationCheckStatus,
		},
		func(cfg *config.Config) prov.Provisioner {
			return &AccessKey{cfg: cfg}
		})
}

func (ak *AccessKey) Create(ctx context.Context, request *resource.CreateRequest) (*resource.CreateResult, error) {
	awsCfg, err := ak.cfg.ToAwsConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading AWS config: %w", err)
	}
	return ak.createWithClient(ctx, iam.NewFromConfig(awsCfg), request)
}

func (ak *AccessKey) createWithClient(ctx context.Context, client accessKeyClientInterface, request *resource.CreateRequest) (*resource.CreateResult, error) {
	var props map[string]any
	if err := json.Unmarshal(request.Properties, &props); err != nil {
		return nil, fmt.Errorf("parsing properties: %w", err)
	}

	userName, _ := props["UserName"].(string)
	if userName == "" {
		return nil, fmt.Errorf("UserName is required")
	}

	output, err := client.CreateAccessKey(ctx, &iam.CreateAccessKeyInput{
		UserName: &userName,
	})
	if err != nil {
		return nil, fmt.Errorf("creating access key for user %s: %w", userName, err)
	}

	key := output.AccessKey
	nativeID := fmt.Sprintf("%s|%s", *key.AccessKeyId, *key.UserName)

	resultProps := map[string]any{
		"AccessKeyId":     *key.AccessKeyId,
		"SecretAccessKey": *key.SecretAccessKey,
		"Status":          string(key.Status),
		"UserName":        *key.UserName,
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

func (ak *AccessKey) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	awsCfg, err := ak.cfg.ToAwsConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading AWS config: %w", err)
	}
	return ak.readWithClient(ctx, iam.NewFromConfig(awsCfg), request)
}

func (ak *AccessKey) readWithClient(ctx context.Context, client accessKeyClientInterface, request *resource.ReadRequest) (*resource.ReadResult, error) {
	accessKeyID, userName, err := parseAccessKeyNativeID(request.NativeID)
	if err != nil {
		return nil, err
	}

	output, err := client.ListAccessKeys(ctx, &iam.ListAccessKeysInput{
		UserName: &userName,
	})
	if err != nil {
		return nil, fmt.Errorf("listing access keys for user %s: %w", userName, err)
	}

	for _, meta := range output.AccessKeyMetadata {
		if meta.AccessKeyId != nil && *meta.AccessKeyId == accessKeyID {
			props := map[string]any{
				"AccessKeyId": *meta.AccessKeyId,
				"Status":      string(meta.Status),
				"UserName":    *meta.UserName,
			}
			propsJSON, _ := json.Marshal(props)
			return &resource.ReadResult{
				ResourceType: request.ResourceType,
				Properties:   string(propsJSON),
			}, nil
		}
	}

	return &resource.ReadResult{
		ResourceType: request.ResourceType,
		ErrorCode:    resource.OperationErrorCodeNotFound,
	}, nil
}

func (ak *AccessKey) Update(ctx context.Context, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	awsCfg, err := ak.cfg.ToAwsConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading AWS config: %w", err)
	}
	return ak.updateWithClient(ctx, iam.NewFromConfig(awsCfg), request)
}

func (ak *AccessKey) updateWithClient(ctx context.Context, client accessKeyClientInterface, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	accessKeyID, userName, err := parseAccessKeyNativeID(request.NativeID)
	if err != nil {
		return nil, err
	}

	var desired map[string]any
	if err := json.Unmarshal(request.DesiredProperties, &desired); err != nil {
		return nil, fmt.Errorf("parsing desired properties: %w", err)
	}

	status := iamtypes.StatusTypeActive
	if s, ok := desired["Status"].(string); ok {
		status = iamtypes.StatusType(s)
	}

	if _, err := client.UpdateAccessKey(ctx, &iam.UpdateAccessKeyInput{
		AccessKeyId: &accessKeyID,
		UserName:    &userName,
		Status:      status,
	}); err != nil {
		return nil, fmt.Errorf("updating access key %s: %w", accessKeyID, err)
	}

	// Post-update Read to populate ResourceProperties
	readResult, err := ak.readWithClient(ctx, client, &resource.ReadRequest{
		NativeID:     request.NativeID,
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
			NativeID:           request.NativeID,
			ResourceProperties: resultProps,
		},
	}, nil
}

func (ak *AccessKey) Delete(ctx context.Context, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	awsCfg, err := ak.cfg.ToAwsConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading AWS config: %w", err)
	}
	return ak.deleteWithClient(ctx, iam.NewFromConfig(awsCfg), request)
}

func (ak *AccessKey) deleteWithClient(ctx context.Context, client accessKeyClientInterface, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	accessKeyID, userName, err := parseAccessKeyNativeID(request.NativeID)
	if err != nil {
		return nil, err
	}

	if _, err := client.DeleteAccessKey(ctx, &iam.DeleteAccessKeyInput{
		AccessKeyId: &accessKeyID,
		UserName:    &userName,
	}); err != nil {
		var noSuchEntity *iamtypes.NoSuchEntityException
		if errors.As(err, &noSuchEntity) {
			return &resource.DeleteResult{
				ProgressResult: &resource.ProgressResult{
					Operation:       resource.OperationDelete,
					OperationStatus: resource.OperationStatusSuccess,
					NativeID:        request.NativeID,
				},
			}, nil
		}
		return nil, fmt.Errorf("deleting access key %s: %w", accessKeyID, err)
	}

	return &resource.DeleteResult{
		ProgressResult: &resource.ProgressResult{
			Operation:       resource.OperationDelete,
			OperationStatus: resource.OperationStatusSuccess,
			NativeID:        request.NativeID,
		},
	}, nil
}

func (ak *AccessKey) Status(_ context.Context, _ *resource.StatusRequest) (*resource.StatusResult, error) {
	return nil, fmt.Errorf("access key operations are synchronous - status polling not needed")
}

func (ak *AccessKey) List(_ context.Context, _ *resource.ListRequest) (*resource.ListResult, error) {
	return nil, fmt.Errorf("operation not implemented - cloudcontrol handles this")
}

func parseAccessKeyNativeID(nativeID string) (string, string, error) {
	parts := strings.SplitN(nativeID, "|", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("invalid NativeID %q: expected accessKeyId|userName", nativeID)
	}
	return parts[0], parts[1], nil
}
