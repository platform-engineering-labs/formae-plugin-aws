// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package networkfirewall

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"

	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/ccx"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres/prov"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres/registry"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/config"
)

const loggingConfigurationType = "AWS::NetworkFirewall::LoggingConfiguration"

// loggingConfigClient abstracts the CloudControl read this List synthesizes from,
// so it can be mocked in unit tests. *ccx.Client satisfies this interface.
type loggingConfigClient interface {
	ReadResource(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error)
}

// LoggingConfiguration supplies a custom List for AWS::NetworkFirewall::LoggingConfiguration.
// CloudControl does not support the LIST action for this type (ListResources returns
// UnsupportedActionException), so discovery cannot enumerate it the generic way. A
// logging configuration is a per-firewall singleton, though, and CloudControl's GetResource
// (keyed by the firewall ARN) does work. Discovery scopes this List to one firewall via the
// FirewallArn list parameter; we read that firewall's logging configuration and return its
// identifier when destinations are configured. Only List is registered — Create/Read/Update/
// Delete/Status fall through to the generic CloudControl path in aws.go.
type LoggingConfiguration struct {
	cfg *config.Config
	// client is injectable for testing; nil means construct a real ccx.Client.
	client loggingConfigClient
}

var _ prov.Provisioner = &LoggingConfiguration{}

func init() {
	registry.Register(loggingConfigurationType,
		[]resource.Operation{resource.OperationList},
		func(cfg *config.Config) prov.Provisioner {
			return &LoggingConfiguration{cfg: cfg}
		})
}

func (l *LoggingConfiguration) getClient() (loggingConfigClient, error) {
	if l.client != nil {
		return l.client, nil
	}
	return ccx.NewClient(l.cfg)
}

// List returns the firewall's ARN (the logging configuration's identifier) when the
// firewall named by the FirewallArn list parameter has at least one log destination
// configured. A firewall with no logging, or one that no longer exists, contributes
// nothing. Discovery reads each returned identifier back through the generic Read path.
func (l *LoggingConfiguration) List(ctx context.Context, request *resource.ListRequest) (*resource.ListResult, error) {
	firewallArn := request.AdditionalProperties["FirewallArn"]
	if firewallArn == "" {
		return nil, fmt.Errorf("FirewallArn is required to list network firewall logging configurations")
	}

	client, err := l.getClient()
	if err != nil {
		return nil, fmt.Errorf("creating cloudcontrol client: %w", err)
	}

	result, err := client.ReadResource(ctx, &resource.ReadRequest{
		NativeID:     firewallArn,
		ResourceType: loggingConfigurationType,
		TargetConfig: request.TargetConfig,
	})
	if err != nil {
		return nil, fmt.Errorf("reading logging configuration for firewall %s: %w", firewallArn, err)
	}
	if result.ErrorCode == resource.OperationErrorCodeNotFound {
		return &resource.ListResult{}, nil
	}
	if result.ErrorCode != "" {
		return nil, fmt.Errorf("reading logging configuration for firewall %s: %s", firewallArn, result.ErrorCode)
	}
	if !hasLogDestinations(result.Properties) {
		return &resource.ListResult{}, nil
	}
	return &resource.ListResult{NativeIDs: []string{firewallArn}}, nil
}

// hasLogDestinations reports whether a read logging configuration has at least one
// destination configured. An empty configuration is not a discoverable resource.
func hasLogDestinations(properties string) bool {
	if properties == "" {
		return false
	}
	var p struct {
		LoggingConfiguration struct {
			LogDestinationConfigs []json.RawMessage `json:"LogDestinationConfigs"`
		} `json:"LoggingConfiguration"`
	}
	if err := json.Unmarshal([]byte(properties), &p); err != nil {
		return false
	}
	return len(p.LoggingConfiguration.LogDestinationConfigs) > 0
}

// The remaining Provisioner methods are unreachable: only List is registered, so
// Create/Read/Update/Delete/Status always route to CloudControl in aws.go.
func (l *LoggingConfiguration) Create(_ context.Context, _ *resource.CreateRequest) (*resource.CreateResult, error) {
	return nil, fmt.Errorf("create not implemented - cloudcontrol handles this")
}

func (l *LoggingConfiguration) Read(_ context.Context, _ *resource.ReadRequest) (*resource.ReadResult, error) {
	return nil, fmt.Errorf("read not implemented - cloudcontrol handles this")
}

func (l *LoggingConfiguration) Update(_ context.Context, _ *resource.UpdateRequest) (*resource.UpdateResult, error) {
	return nil, fmt.Errorf("update not implemented - cloudcontrol handles this")
}

func (l *LoggingConfiguration) Delete(_ context.Context, _ *resource.DeleteRequest) (*resource.DeleteResult, error) {
	return nil, fmt.Errorf("delete not implemented - cloudcontrol handles this")
}

func (l *LoggingConfiguration) Status(_ context.Context, _ *resource.StatusRequest) (*resource.StatusResult, error) {
	return nil, fmt.Errorf("status not implemented - cloudcontrol handles this")
}
