// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package networkfirewall

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/config"
)

const fwArn = "arn:aws:network-firewall:us-east-1:123456789012:firewall/test-fw"

func fwPropsJSON(t *testing.T, endpointIds []string) string {
	t.Helper()
	props := map[string]any{
		"FirewallName":      "test-fw",
		"FirewallArn":       fwArn,
		"FirewallId":        "11111111-2222-3333-4444-555555555555",
		"VpcId":             "vpc-0abc",
		"FirewallPolicyArn": "arn:aws:network-firewall:us-east-1:123456789012:firewall-policy/test-pol",
	}
	if endpointIds != nil {
		ids := make([]any, len(endpointIds))
		for i, e := range endpointIds {
			ids[i] = e
		}
		props["EndpointIds"] = ids
	}
	b, err := json.Marshal(props)
	require.NoError(t, err)
	return string(b)
}

func newFirewallWithMock(c *mockCCXClient) *Firewall {
	return &Firewall{cfg: &config.Config{Region: "us-east-1"}, client: c}
}

func readEndpointIdsByAz(t *testing.T, properties string) map[string]string {
	t.Helper()
	var p struct {
		EndpointIdsByAz map[string]string `json:"EndpointIdsByAz"`
	}
	require.NoError(t, json.Unmarshal([]byte(properties), &p))
	return p.EndpointIdsByAz
}

func TestFirewall_Read_FlattensMultipleAZs(t *testing.T) {
	c := &mockCCXClient{}
	c.On("ReadResource", mock.Anything, mock.Anything).Return(&resource.ReadResult{
		ResourceType: firewallType,
		Properties:   fwPropsJSON(t, []string{"us-east-1a:vpce-aaa", "us-east-1b:vpce-bbb"}),
	}, nil)
	fw := newFirewallWithMock(c)

	res, err := fw.Read(context.Background(), &resource.ReadRequest{NativeID: fwArn, ResourceType: firewallType})

	require.NoError(t, err)
	assert.Equal(t, map[string]string{"us-east-1a": "vpce-aaa", "us-east-1b": "vpce-bbb"}, readEndpointIdsByAz(t, res.Properties))
}

func TestFirewall_Read_SingleAZ(t *testing.T) {
	c := &mockCCXClient{}
	c.On("ReadResource", mock.Anything, mock.Anything).Return(&resource.ReadResult{
		ResourceType: firewallType,
		Properties:   fwPropsJSON(t, []string{"eu-west-1a:vpce-zzz"}),
	}, nil)
	fw := newFirewallWithMock(c)

	res, err := fw.Read(context.Background(), &resource.ReadRequest{NativeID: fwArn, ResourceType: firewallType})

	require.NoError(t, err)
	assert.Equal(t, map[string]string{"eu-west-1a": "vpce-zzz"}, readEndpointIdsByAz(t, res.Properties))
}

func TestFirewall_Read_EmptyEndpointIds(t *testing.T) {
	c := &mockCCXClient{}
	c.On("ReadResource", mock.Anything, mock.Anything).Return(&resource.ReadResult{
		ResourceType: firewallType,
		Properties:   fwPropsJSON(t, []string{}),
	}, nil)
	fw := newFirewallWithMock(c)

	res, err := fw.Read(context.Background(), &resource.ReadRequest{NativeID: fwArn, ResourceType: firewallType})

	require.NoError(t, err)
	assert.Empty(t, readEndpointIdsByAz(t, res.Properties))
}

func TestFirewall_Read_NoEndpointIdsKey(t *testing.T) {
	c := &mockCCXClient{}
	c.On("ReadResource", mock.Anything, mock.Anything).Return(&resource.ReadResult{
		ResourceType: firewallType,
		Properties:   fwPropsJSON(t, nil),
	}, nil)
	fw := newFirewallWithMock(c)

	res, err := fw.Read(context.Background(), &resource.ReadRequest{NativeID: fwArn, ResourceType: firewallType})

	require.NoError(t, err)
	assert.Empty(t, readEndpointIdsByAz(t, res.Properties))
}

func TestFirewall_Read_MalformedEntrySkipped(t *testing.T) {
	c := &mockCCXClient{}
	c.On("ReadResource", mock.Anything, mock.Anything).Return(&resource.ReadResult{
		ResourceType: firewallType,
		Properties:   fwPropsJSON(t, []string{"no-colon-here", "us-east-1a:vpce-aaa", ":vpce-nobad", "us-east-1b:"}),
	}, nil)
	fw := newFirewallWithMock(c)

	res, err := fw.Read(context.Background(), &resource.ReadRequest{NativeID: fwArn, ResourceType: firewallType})

	require.NoError(t, err)
	assert.Equal(t, map[string]string{"us-east-1a": "vpce-aaa"}, readEndpointIdsByAz(t, res.Properties))
}

func TestFirewall_Read_NativeEndpointIdsPreserved(t *testing.T) {
	c := &mockCCXClient{}
	c.On("ReadResource", mock.Anything, mock.Anything).Return(&resource.ReadResult{
		ResourceType: firewallType,
		Properties:   fwPropsJSON(t, []string{"us-east-1a:vpce-aaa"}),
	}, nil)
	fw := newFirewallWithMock(c)

	res, err := fw.Read(context.Background(), &resource.ReadRequest{NativeID: fwArn, ResourceType: firewallType})

	require.NoError(t, err)
	var p map[string]any
	require.NoError(t, json.Unmarshal([]byte(res.Properties), &p))
	assert.Contains(t, p, "EndpointIds")
}

func TestFirewall_Read_ErrorCodePassthrough(t *testing.T) {
	c := &mockCCXClient{}
	c.On("ReadResource", mock.Anything, mock.Anything).Return(&resource.ReadResult{
		ResourceType: firewallType,
		ErrorCode:    resource.OperationErrorCodeNotFound,
	}, nil)
	fw := newFirewallWithMock(c)

	res, err := fw.Read(context.Background(), &resource.ReadRequest{NativeID: fwArn, ResourceType: firewallType})

	require.NoError(t, err)
	assert.Equal(t, resource.OperationErrorCodeNotFound, res.ErrorCode)
}

func TestFirewall_Read_ReaderError(t *testing.T) {
	c := &mockCCXClient{}
	c.On("ReadResource", mock.Anything, mock.Anything).Return(nil, errors.New("boom"))
	fw := newFirewallWithMock(c)

	_, err := fw.Read(context.Background(), &resource.ReadRequest{NativeID: fwArn, ResourceType: firewallType})

	require.Error(t, err)
}

func statusResultWith(status resource.OperationStatus, props string) *resource.StatusResult {
	return statusResultForOp(resource.OperationCreate, status, props)
}

func statusResultForOp(op resource.Operation, status resource.OperationStatus, props string) *resource.StatusResult {
	pr := &resource.ProgressResult{
		Operation:       op,
		OperationStatus: status,
		RequestID:       "req-1",
		NativeID:        fwArn,
	}
	if props != "" {
		pr.ResourceProperties = json.RawMessage(props)
	}
	return &resource.StatusResult{ProgressResult: pr}
}

func TestFirewall_Status_InProgressPassthrough(t *testing.T) {
	c := &mockCCXClient{}
	c.On("StatusResource", mock.Anything, mock.Anything, mock.Anything).
		Return(statusResultWith(resource.OperationStatusInProgress, ""), nil)
	fw := newFirewallWithMock(c)

	res, err := fw.Status(context.Background(), &resource.StatusRequest{RequestID: "req-1", NativeID: fwArn, ResourceType: firewallType})

	require.NoError(t, err)
	assert.Equal(t, resource.OperationStatusInProgress, res.ProgressResult.OperationStatus)
}

func TestFirewall_Status_SuccessButEmptyEndpoints_StaysInProgress(t *testing.T) {
	c := &mockCCXClient{}
	props := fwPropsJSON(t, []string{})
	c.On("StatusResource", mock.Anything, mock.Anything, mock.Anything).
		Return(statusResultWith(resource.OperationStatusSuccess, props), nil)
	fw := newFirewallWithMock(c)

	res, err := fw.Status(context.Background(), &resource.StatusRequest{RequestID: "req-1", NativeID: fwArn, ResourceType: firewallType})

	require.NoError(t, err)
	assert.Equal(t, resource.OperationStatusInProgress, res.ProgressResult.OperationStatus)
	assert.Equal(t, "req-1", res.ProgressResult.RequestID)
	assert.Equal(t, fwArn, res.ProgressResult.NativeID)
}

func TestFirewall_Status_SuccessWithEndpoints(t *testing.T) {
	c := &mockCCXClient{}
	props := `{"FirewallArn":"` + fwArn + `","EndpointIdsByAz":{"us-east-1a":"vpce-aaa"}}`
	c.On("StatusResource", mock.Anything, mock.Anything, mock.Anything).
		Return(statusResultWith(resource.OperationStatusSuccess, props), nil)
	fw := newFirewallWithMock(c)

	res, err := fw.Status(context.Background(), &resource.StatusRequest{RequestID: "req-1", NativeID: fwArn, ResourceType: firewallType})

	require.NoError(t, err)
	assert.Equal(t, resource.OperationStatusSuccess, res.ProgressResult.OperationStatus)
}

func TestFirewall_Status_DeleteSuccessPassthrough(t *testing.T) {
	// A completed delete returns Success with no ResourceProperties (ccx does not
	// read deleted resources). The endpoint-readiness gate must NOT apply to
	// deletes, otherwise a finished delete is flipped back to InProgress and polls
	// until timeout.
	c := &mockCCXClient{}
	c.On("StatusResource", mock.Anything, mock.Anything, mock.Anything).
		Return(statusResultForOp(resource.OperationDelete, resource.OperationStatusSuccess, ""), nil)
	fw := newFirewallWithMock(c)

	res, err := fw.Status(context.Background(), &resource.StatusRequest{RequestID: "req-1", NativeID: fwArn, ResourceType: firewallType})

	require.NoError(t, err)
	assert.Equal(t, resource.OperationStatusSuccess, res.ProgressResult.OperationStatus)
}

func TestFirewall_Status_FailurePassthrough(t *testing.T) {
	c := &mockCCXClient{}
	c.On("StatusResource", mock.Anything, mock.Anything, mock.Anything).
		Return(statusResultWith(resource.OperationStatusFailure, ""), nil)
	fw := newFirewallWithMock(c)

	res, err := fw.Status(context.Background(), &resource.StatusRequest{RequestID: "req-1", NativeID: fwArn, ResourceType: firewallType})

	require.NoError(t, err)
	assert.Equal(t, resource.OperationStatusFailure, res.ProgressResult.OperationStatus)
}
