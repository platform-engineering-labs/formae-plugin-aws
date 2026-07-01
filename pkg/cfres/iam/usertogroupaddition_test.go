// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package iam

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"time"

	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/config"
)

func newTestUserToGroupAddition() *UserToGroupAddition {
	return &UserToGroupAddition{
		cfg:          &config.Config{},
		addAttempts:  3,
		readAttempts: 3,
		backoff:      0,
		sleep:        func(_ time.Duration) {},
	}
}

func noSuchEntityErr() error {
	return fmt.Errorf("api error NoSuchEntity: %w", &iamtypes.NoSuchEntityException{Message: stringPtr("not found")})
}

func userWithName(name string) iamtypes.User {
	return iamtypes.User{UserName: stringPtr(name)}
}

func TestUserToGroupAddition_Create_Success(t *testing.T) {
	ctx := context.Background()
	client := &mockUserToGroupAdditionClient{}

	client.On("AddUserToGroup", ctx, mock.MatchedBy(func(in *iam.AddUserToGroupInput) bool {
		return in.GroupName != nil && *in.GroupName == "devs" && in.UserName != nil && *in.UserName == "alice"
	})).Return(&iam.AddUserToGroupOutput{}, nil)
	client.On("GetGroup", ctx, mock.Anything).Return(&iam.GetGroupOutput{
		Users: []iamtypes.User{userWithName("alice")},
	}, nil)

	p := newTestUserToGroupAddition()
	props, _ := json.Marshal(map[string]any{"GroupName": "devs", "UserName": "alice"})
	result, err := p.createWithClient(ctx, client, &resource.CreateRequest{
		ResourceType: userToGroupAdditionType,
		Properties:   props,
	})

	assert.NoError(t, err)
	assert.Equal(t, resource.OperationStatusSuccess, result.ProgressResult.OperationStatus)
	assert.Equal(t, "devs|alice", result.ProgressResult.NativeID)

	var rp map[string]any
	_ = json.Unmarshal(result.ProgressResult.ResourceProperties, &rp)
	assert.Equal(t, "devs", rp["GroupName"])
	assert.Equal(t, "alice", rp["UserName"])
	client.AssertExpectations(t)
}

func TestUserToGroupAddition_Create_AlreadyMember_Idempotent(t *testing.T) {
	ctx := context.Background()
	client := &mockUserToGroupAdditionClient{}

	// AddUserToGroup succeeds (AWS is idempotent for existing members).
	client.On("AddUserToGroup", ctx, mock.Anything).Return(&iam.AddUserToGroupOutput{}, nil)
	client.On("GetGroup", ctx, mock.Anything).Return(&iam.GetGroupOutput{
		Users: []iamtypes.User{userWithName("alice")},
	}, nil)

	p := newTestUserToGroupAddition()
	props, _ := json.Marshal(map[string]any{"GroupName": "devs", "UserName": "alice"})
	result, err := p.createWithClient(ctx, client, &resource.CreateRequest{Properties: props})

	assert.NoError(t, err)
	assert.Equal(t, resource.OperationStatusSuccess, result.ProgressResult.OperationStatus)
	client.AssertExpectations(t)
}

func TestUserToGroupAddition_Create_StaleThenPresent_ReadAfterAdd(t *testing.T) {
	ctx := context.Background()
	client := &mockUserToGroupAdditionClient{}

	client.On("AddUserToGroup", ctx, mock.Anything).Return(&iam.AddUserToGroupOutput{}, nil)
	// First GetGroup: member not yet visible; second: present.
	client.On("GetGroup", ctx, mock.Anything).Return(&iam.GetGroupOutput{Users: []iamtypes.User{}}, nil).Once()
	client.On("GetGroup", ctx, mock.Anything).Return(&iam.GetGroupOutput{
		Users: []iamtypes.User{userWithName("alice")},
	}, nil).Once()

	p := newTestUserToGroupAddition()
	props, _ := json.Marshal(map[string]any{"GroupName": "devs", "UserName": "alice"})
	result, err := p.createWithClient(ctx, client, &resource.CreateRequest{Properties: props})

	assert.NoError(t, err)
	assert.Equal(t, resource.OperationStatusSuccess, result.ProgressResult.OperationStatus)
	client.AssertExpectations(t)
}

func TestUserToGroupAddition_Create_TransientNoSuchEntity_Retried(t *testing.T) {
	ctx := context.Background()
	client := &mockUserToGroupAdditionClient{}

	// First Add fails with NoSuchEntity (group/user not visible yet), then succeeds.
	client.On("AddUserToGroup", ctx, mock.Anything).Return((*iam.AddUserToGroupOutput)(nil), noSuchEntityErr()).Once()
	client.On("AddUserToGroup", ctx, mock.Anything).Return(&iam.AddUserToGroupOutput{}, nil).Once()
	client.On("GetGroup", ctx, mock.Anything).Return(&iam.GetGroupOutput{
		Users: []iamtypes.User{userWithName("alice")},
	}, nil)

	p := newTestUserToGroupAddition()
	props, _ := json.Marshal(map[string]any{"GroupName": "devs", "UserName": "alice"})
	result, err := p.createWithClient(ctx, client, &resource.CreateRequest{Properties: props})

	assert.NoError(t, err)
	assert.Equal(t, resource.OperationStatusSuccess, result.ProgressResult.OperationStatus)
	client.AssertExpectations(t)
}

func TestUserToGroupAddition_Create_MissingGroupName(t *testing.T) {
	ctx := context.Background()
	p := newTestUserToGroupAddition()
	props, _ := json.Marshal(map[string]any{"UserName": "alice"})
	_, err := p.createWithClient(ctx, &mockUserToGroupAdditionClient{}, &resource.CreateRequest{Properties: props})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "GroupName")
}

func TestUserToGroupAddition_Read_Found(t *testing.T) {
	ctx := context.Background()
	client := &mockUserToGroupAdditionClient{}

	client.On("GetGroup", ctx, mock.MatchedBy(func(in *iam.GetGroupInput) bool {
		return in.GroupName != nil && *in.GroupName == "devs"
	})).Return(&iam.GetGroupOutput{
		Users: []iamtypes.User{userWithName("bob"), userWithName("alice")},
	}, nil)

	p := newTestUserToGroupAddition()
	result, err := p.readWithClient(ctx, client, &resource.ReadRequest{
		NativeID:     "devs|alice",
		ResourceType: userToGroupAdditionType,
	})

	assert.NoError(t, err)
	assert.Empty(t, result.ErrorCode)
	var props map[string]any
	_ = json.Unmarshal([]byte(result.Properties), &props)
	assert.Equal(t, "devs", props["GroupName"])
	assert.Equal(t, "alice", props["UserName"])
	client.AssertExpectations(t)
}

func TestUserToGroupAddition_Read_NotFound_GroupMissing(t *testing.T) {
	ctx := context.Background()
	client := &mockUserToGroupAdditionClient{}

	client.On("GetGroup", ctx, mock.Anything).Return((*iam.GetGroupOutput)(nil), noSuchEntityErr())

	p := newTestUserToGroupAddition()
	result, err := p.readWithClient(ctx, client, &resource.ReadRequest{
		NativeID:     "gone|alice",
		ResourceType: userToGroupAdditionType,
	})

	assert.NoError(t, err)
	assert.Equal(t, resource.OperationErrorCodeNotFound, result.ErrorCode)
	client.AssertExpectations(t)
}

func TestUserToGroupAddition_Read_NotFound_UserAbsent(t *testing.T) {
	ctx := context.Background()
	client := &mockUserToGroupAdditionClient{}

	client.On("GetGroup", ctx, mock.Anything).Return(&iam.GetGroupOutput{
		Users: []iamtypes.User{userWithName("bob")},
	}, nil)

	p := newTestUserToGroupAddition()
	result, err := p.readWithClient(ctx, client, &resource.ReadRequest{
		NativeID:     "devs|alice",
		ResourceType: userToGroupAdditionType,
	})

	assert.NoError(t, err)
	assert.Equal(t, resource.OperationErrorCodeNotFound, result.ErrorCode)
	client.AssertExpectations(t)
}

func TestUserToGroupAddition_Read_Paginated_UserOnSecondPage(t *testing.T) {
	ctx := context.Background()
	client := &mockUserToGroupAdditionClient{}

	// Page 1: truncated, user not present.
	client.On("GetGroup", ctx, mock.MatchedBy(func(in *iam.GetGroupInput) bool {
		return in.Marker == nil
	})).Return(&iam.GetGroupOutput{
		Users:       []iamtypes.User{userWithName("bob")},
		IsTruncated: true,
		Marker:      stringPtr("page2"),
	}, nil)
	// Page 2: user present.
	client.On("GetGroup", ctx, mock.MatchedBy(func(in *iam.GetGroupInput) bool {
		return in.Marker != nil && *in.Marker == "page2"
	})).Return(&iam.GetGroupOutput{
		Users: []iamtypes.User{userWithName("alice")},
	}, nil)

	p := newTestUserToGroupAddition()
	result, err := p.readWithClient(ctx, client, &resource.ReadRequest{
		NativeID:     "devs|alice",
		ResourceType: userToGroupAdditionType,
	})

	assert.NoError(t, err)
	assert.Empty(t, result.ErrorCode)
	client.AssertExpectations(t)
}

func TestUserToGroupAddition_Read_GenericError_Surfaced(t *testing.T) {
	ctx := context.Background()
	client := &mockUserToGroupAdditionClient{}

	client.On("GetGroup", ctx, mock.Anything).Return((*iam.GetGroupOutput)(nil), fmt.Errorf("throttling: rate exceeded"))

	p := newTestUserToGroupAddition()
	result, err := p.readWithClient(ctx, client, &resource.ReadRequest{
		NativeID:     "devs|alice",
		ResourceType: userToGroupAdditionType,
	})

	assert.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "rate exceeded")
	client.AssertExpectations(t)
}

func TestUserToGroupAddition_Delete_Success(t *testing.T) {
	ctx := context.Background()
	client := &mockUserToGroupAdditionClient{}

	client.On("RemoveUserFromGroup", ctx, mock.MatchedBy(func(in *iam.RemoveUserFromGroupInput) bool {
		return in.GroupName != nil && *in.GroupName == "devs" && in.UserName != nil && *in.UserName == "alice"
	})).Return(&iam.RemoveUserFromGroupOutput{}, nil)

	p := newTestUserToGroupAddition()
	result, err := p.deleteWithClient(ctx, client, &resource.DeleteRequest{
		NativeID:     "devs|alice",
		ResourceType: userToGroupAdditionType,
	})

	assert.NoError(t, err)
	assert.Equal(t, resource.OperationStatusSuccess, result.ProgressResult.OperationStatus)
	client.AssertExpectations(t)
}

func TestUserToGroupAddition_Delete_AlreadyGone_Idempotent(t *testing.T) {
	ctx := context.Background()
	client := &mockUserToGroupAdditionClient{}

	client.On("RemoveUserFromGroup", ctx, mock.Anything).Return((*iam.RemoveUserFromGroupOutput)(nil), noSuchEntityErr())

	p := newTestUserToGroupAddition()
	result, err := p.deleteWithClient(ctx, client, &resource.DeleteRequest{
		NativeID:     "devs|alice",
		ResourceType: userToGroupAdditionType,
	})

	assert.NoError(t, err)
	assert.Equal(t, resource.OperationStatusSuccess, result.ProgressResult.OperationStatus)
	client.AssertExpectations(t)
}

func TestUserToGroupAddition_ParseNativeID(t *testing.T) {
	cases := []struct {
		in      string
		wantErr bool
		group   string
		user    string
	}{
		{in: "", wantErr: true},
		{in: "|", wantErr: true},
		{in: "g|", wantErr: true},
		{in: "|u", wantErr: true},
		{in: "devs|alice", wantErr: false, group: "devs", user: "alice"},
	}
	for _, c := range cases {
		g, u, err := parseUserToGroupAdditionNativeID(c.in)
		if c.wantErr {
			assert.Error(t, err, "input %q should error", c.in)
			continue
		}
		assert.NoError(t, err, "input %q", c.in)
		assert.Equal(t, c.group, g)
		assert.Equal(t, c.user, u)
	}
}
