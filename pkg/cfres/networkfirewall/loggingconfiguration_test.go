// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package networkfirewall

import (
	"context"
	"testing"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

const testFirewallArn = "arn:aws:network-firewall:us-east-1:111122223333:firewall/fw-1"

func readReq(r *resource.ReadRequest) bool {
	return r.NativeID == testFirewallArn && r.ResourceType == loggingConfigurationType
}

func TestLoggingConfigurationList_ReturnsFirewallArnWhenLoggingConfigured(t *testing.T) {
	ctx := context.Background()
	client := &mockCCXClient{}
	client.On("ReadResource", ctx, mock.MatchedBy(readReq)).Return(&resource.ReadResult{
		Properties: `{"FirewallArn":"` + testFirewallArn + `","LoggingConfiguration":{"LogDestinationConfigs":[` +
			`{"LogType":"FLOW","LogDestinationType":"CloudWatchLogs","LogDestination":{"logGroup":"/x"}}]}}`,
	}, nil)

	l := &LoggingConfiguration{client: client}
	result, err := l.List(ctx, &resource.ListRequest{
		ResourceType:         loggingConfigurationType,
		AdditionalProperties: map[string]string{"FirewallArn": testFirewallArn},
	})

	require.NoError(t, err)
	require.Equal(t, []string{testFirewallArn}, result.NativeIDs)
	client.AssertExpectations(t)
}

func TestLoggingConfigurationList_EmptyWhenNoDestinations(t *testing.T) {
	ctx := context.Background()
	client := &mockCCXClient{}
	client.On("ReadResource", ctx, mock.MatchedBy(readReq)).Return(&resource.ReadResult{
		Properties: `{"FirewallArn":"` + testFirewallArn + `","LoggingConfiguration":{"LogDestinationConfigs":[]}}`,
	}, nil)

	l := &LoggingConfiguration{client: client}
	result, err := l.List(ctx, &resource.ListRequest{
		ResourceType:         loggingConfigurationType,
		AdditionalProperties: map[string]string{"FirewallArn": testFirewallArn},
	})

	require.NoError(t, err)
	require.Empty(t, result.NativeIDs)
	client.AssertExpectations(t)
}

func TestLoggingConfigurationList_EmptyWhenFirewallNotFound(t *testing.T) {
	ctx := context.Background()
	client := &mockCCXClient{}
	client.On("ReadResource", ctx, mock.MatchedBy(readReq)).Return(&resource.ReadResult{
		ErrorCode: resource.OperationErrorCodeNotFound,
	}, nil)

	l := &LoggingConfiguration{client: client}
	result, err := l.List(ctx, &resource.ListRequest{
		ResourceType:         loggingConfigurationType,
		AdditionalProperties: map[string]string{"FirewallArn": testFirewallArn},
	})

	require.NoError(t, err)
	require.Empty(t, result.NativeIDs)
	client.AssertExpectations(t)
}

func TestLoggingConfigurationList_ErrorsWithoutFirewallArn(t *testing.T) {
	l := &LoggingConfiguration{}
	_, err := l.List(context.Background(), &resource.ListRequest{
		ResourceType: loggingConfigurationType,
	})
	require.Error(t, err)
}
