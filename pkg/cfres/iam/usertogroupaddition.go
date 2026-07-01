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
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres/prov"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres/registry"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres/utils"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/config"
)

// AWS::IAM::UserToGroupAddition is NON_PROVISIONABLE in Cloud Control, so it goes
// through a custom provisioner driving AddUserToGroup / RemoveUserFromGroup
// directly. It models a SINGLE membership (one user in one group); a change to
// either field is a replace. Current state is read from the group's member list
// via the paginated GetGroup call.
const userToGroupAdditionType = "AWS::IAM::UserToGroupAddition"

type userToGroupAdditionClientInterface interface {
	GetGroup(ctx context.Context, params *iam.GetGroupInput, optFns ...func(*iam.Options)) (*iam.GetGroupOutput, error)
	AddUserToGroup(ctx context.Context, params *iam.AddUserToGroupInput, optFns ...func(*iam.Options)) (*iam.AddUserToGroupOutput, error)
	RemoveUserFromGroup(ctx context.Context, params *iam.RemoveUserFromGroupInput, optFns ...func(*iam.Options)) (*iam.RemoveUserFromGroupOutput, error)
}

type UserToGroupAddition struct {
	cfg *config.Config

	// addAttempts / readAttempts bound the eventual-consistency polls on Create;
	// backoff is the wait between polls and sleep is injectable for tests. Both
	// the group and the user may be created in the same apply DAG, so the Add can
	// transiently fail with NoSuchEntity and a fresh member may not be visible to
	// GetGroup immediately.
	addAttempts  int
	readAttempts int
	backoff      time.Duration
	sleep        func(time.Duration)
}

var _ prov.Provisioner = &UserToGroupAddition{}

func init() {
	registry.Register(userToGroupAdditionType,
		[]resource.Operation{
			resource.OperationCreate,
			resource.OperationRead,
			resource.OperationDelete,
		},
		func(cfg *config.Config) prov.Provisioner {
			return &UserToGroupAddition{
				cfg:          cfg,
				addAttempts:  5,
				readAttempts: 10,
				backoff:      2 * time.Second,
				sleep:        time.Sleep,
			}
		})
}

// parseUserToGroupAdditionNativeID parses the composite NativeID groupName|userName.
// It requires exactly two non-empty parts, rejecting "", "|", "g|" and "|u". The
// delimiter is "|" because it is not a legal character in IAM names (comma is, so
// comma cannot be the separator).
func parseUserToGroupAdditionNativeID(nativeID string) (groupName, userName string, err error) {
	parts := strings.SplitN(nativeID, "|", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid NativeID format: expected groupName|userName, got: %q", nativeID)
	}
	return parts[0], parts[1], nil
}

// isNoSuchEntity reports whether err is the AWS NoSuchEntity error (group or user
// does not exist). Only this error is treated as not-found; any other AWS error
// (auth, throttle, validation, outage) is surfaced.
func isNoSuchEntity(err error) bool {
	var noSuchEntity *iamtypes.NoSuchEntityException
	return errors.As(err, &noSuchEntity)
}

// groupContainsUser reports whether userName is a member of groupName, paginating
// GetGroup because the managed user may be on a later page. A missing group is
// surfaced as (_, err) so callers can classify it; membership absence reads as
// (false, nil).
func groupContainsUser(ctx context.Context, client userToGroupAdditionClientInterface, groupName, userName string) (bool, error) {
	var marker *string
	for {
		out, err := client.GetGroup(ctx, &iam.GetGroupInput{
			GroupName: aws.String(groupName),
			Marker:    marker,
		})
		if err != nil {
			return false, err
		}
		for _, u := range out.Users {
			if u.UserName != nil && *u.UserName == userName {
				return true, nil
			}
		}
		if !out.IsTruncated || out.Marker == nil {
			return false, nil
		}
		marker = out.Marker
	}
}

func (p *UserToGroupAddition) Create(ctx context.Context, request *resource.CreateRequest) (*resource.CreateResult, error) {
	awsCfg, err := p.cfg.ToAwsConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading AWS config: %w", err)
	}
	return p.createWithClient(ctx, iam.NewFromConfig(awsCfg), request)
}

func (p *UserToGroupAddition) createWithClient(ctx context.Context, client userToGroupAdditionClientInterface, request *resource.CreateRequest) (*resource.CreateResult, error) {
	var props map[string]any
	if err := json.Unmarshal(request.Properties, &props); err != nil {
		return nil, fmt.Errorf("parsing properties: %w", err)
	}
	groupName, err := utils.GetStringProperty(props, "GroupName")
	if err != nil {
		return nil, fmt.Errorf("invalid GroupName: %w", err)
	}
	userName, err := utils.GetStringProperty(props, "UserName")
	if err != nil {
		return nil, fmt.Errorf("invalid UserName: %w", err)
	}

	nativeID := fmt.Sprintf("%s|%s", groupName, userName)

	// AddUserToGroup is idempotent for an existing member, so re-create is safe.
	// The group or user may not be visible yet (both created in the same apply
	// DAG, IAM is eventually consistent), so retry on NoSuchEntity a few times.
	var addErr error
	for attempt := 0; attempt < p.addAttempts; attempt++ {
		_, addErr = client.AddUserToGroup(ctx, &iam.AddUserToGroupInput{
			GroupName: aws.String(groupName),
			UserName:  aws.String(userName),
		})
		if addErr == nil {
			break
		}
		if !isNoSuchEntity(addErr) {
			return nil, fmt.Errorf("adding user %s to group %s: %w", userName, groupName, addErr)
		}
		if attempt < p.addAttempts-1 {
			p.sleep(p.backoff)
		}
	}
	if addErr != nil {
		return nil, fmt.Errorf("adding user %s to group %s: %w", userName, groupName, addErr)
	}

	// Read-after-add: poll GetGroup until the member list contains the user so a
	// Read right after Create does not spuriously report NotFound.
	for attempt := 0; attempt < p.readAttempts; attempt++ {
		present, err := groupContainsUser(ctx, client, groupName, userName)
		if err != nil {
			return nil, fmt.Errorf("confirming group membership: %w", err)
		}
		if present {
			break
		}
		if attempt < p.readAttempts-1 {
			p.sleep(p.backoff)
		}
	}

	resultProps, err := json.Marshal(map[string]any{
		"GroupName": groupName,
		"UserName":  userName,
	})
	if err != nil {
		return nil, fmt.Errorf("marshaling properties: %w", err)
	}

	return &resource.CreateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationCreate,
			OperationStatus:    resource.OperationStatusSuccess,
			NativeID:           nativeID,
			ResourceProperties: resultProps,
		},
	}, nil
}

func (p *UserToGroupAddition) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	awsCfg, err := p.cfg.ToAwsConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading AWS config: %w", err)
	}
	return p.readWithClient(ctx, iam.NewFromConfig(awsCfg), request)
}

func (p *UserToGroupAddition) readWithClient(ctx context.Context, client userToGroupAdditionClientInterface, request *resource.ReadRequest) (*resource.ReadResult, error) {
	groupName, userName, err := parseUserToGroupAdditionNativeID(request.NativeID)
	if err != nil {
		return nil, err
	}

	present, err := groupContainsUser(ctx, client, groupName, userName)
	if err != nil {
		// Only a missing group maps to NotFound; any other error is surfaced.
		if isNoSuchEntity(err) {
			return &resource.ReadResult{
				ResourceType: request.ResourceType,
				ErrorCode:    resource.OperationErrorCodeNotFound,
			}, nil
		}
		return nil, fmt.Errorf("reading group %s: %w", groupName, err)
	}
	if !present {
		return &resource.ReadResult{
			ResourceType: request.ResourceType,
			ErrorCode:    resource.OperationErrorCodeNotFound,
		}, nil
	}

	propBytes, err := json.Marshal(map[string]any{
		"GroupName": groupName,
		"UserName":  userName,
	})
	if err != nil {
		return nil, fmt.Errorf("marshaling properties: %w", err)
	}
	return &resource.ReadResult{
		ResourceType: request.ResourceType,
		Properties:   string(propBytes),
	}, nil
}

func (p *UserToGroupAddition) Delete(ctx context.Context, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	awsCfg, err := p.cfg.ToAwsConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading AWS config: %w", err)
	}
	return p.deleteWithClient(ctx, iam.NewFromConfig(awsCfg), request)
}

func (p *UserToGroupAddition) deleteWithClient(ctx context.Context, client userToGroupAdditionClientInterface, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	groupName, userName, err := parseUserToGroupAdditionNativeID(request.NativeID)
	if err != nil {
		return nil, err
	}

	if _, err := client.RemoveUserFromGroup(ctx, &iam.RemoveUserFromGroupInput{
		GroupName: aws.String(groupName),
		UserName:  aws.String(userName),
	}); err != nil {
		// Idempotent: a missing group or user means the membership is already gone.
		if isNoSuchEntity(err) {
			return &resource.DeleteResult{
				ProgressResult: &resource.ProgressResult{
					Operation:       resource.OperationDelete,
					OperationStatus: resource.OperationStatusSuccess,
					NativeID:        request.NativeID,
				},
			}, nil
		}
		return nil, fmt.Errorf("removing user %s from group %s: %w", userName, groupName, err)
	}

	return &resource.DeleteResult{
		ProgressResult: &resource.ProgressResult{
			Operation:       resource.OperationDelete,
			OperationStatus: resource.OperationStatusSuccess,
			NativeID:        request.NativeID,
		},
	}, nil
}

// Update is never invoked: both schema fields are createOnly, so any change is a
// replace (Delete then Create). The method exists only to satisfy prov.Provisioner.
func (p *UserToGroupAddition) Update(_ context.Context, _ *resource.UpdateRequest) (*resource.UpdateResult, error) {
	return nil, fmt.Errorf("update is not supported for %s; a change is a replace (delete then create)", userToGroupAdditionType)
}

// Status is not registered: AddUserToGroup / RemoveUserFromGroup are synchronous,
// so Create and Delete return success directly with no status polling.
func (p *UserToGroupAddition) Status(_ context.Context, _ *resource.StatusRequest) (*resource.StatusResult, error) {
	return nil, fmt.Errorf("status check is not implemented for %s", userToGroupAdditionType)
}

// List is not registered: the resource is not discoverable.
func (p *UserToGroupAddition) List(_ context.Context, _ *resource.ListRequest) (*resource.ListResult, error) {
	return &resource.ListResult{
		NativeIDs: []string{},
	}, nil
}
