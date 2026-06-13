// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package networkfirewall

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"

	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/ccx"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres/prov"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres/registry"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/config"
)

const firewallType = "AWS::NetworkFirewall::Firewall"

// ccxClient abstracts the CloudControl operations this reconciler delegates to,
// so they can be mocked in unit tests. *ccx.Client satisfies this interface.
type ccxClient interface {
	ReadResource(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error)
	StatusResource(ctx context.Context, request *resource.StatusRequest, readFunc func(context.Context, *resource.ReadRequest) (*resource.ReadResult, error)) (*resource.StatusResult, error)
}

// Firewall enriches CloudControl reads with a computed EndpointIdsByAz map and
// gates create-success on endpoint readiness. All other operations fall through
// to the generic CloudControl path in aws.go (only Read + CheckStatus are
// registered).
type Firewall struct {
	cfg *config.Config
	// client is injectable for testing; nil means construct a real ccx.Client.
	client ccxClient
}

var _ prov.Provisioner = &Firewall{}

func init() {
	registry.Register(firewallType,
		[]resource.Operation{resource.OperationRead, resource.OperationCheckStatus},
		func(cfg *config.Config) prov.Provisioner {
			return &Firewall{cfg: cfg}
		})
}

func (f *Firewall) getClient() (ccxClient, error) {
	if f.client != nil {
		return f.client, nil
	}
	return ccx.NewClient(f.cfg)
}

// Read reads the firewall via CloudControl and injects a computed EndpointIdsByAz
// object mapping availability zone -> VPC endpoint id, derived from the read-only
// EndpointIds list (each entry "<az>:<endpoint-id>"). The native EndpointIds list
// is left intact.
func (f *Firewall) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	client, err := f.getClient()
	if err != nil {
		return nil, fmt.Errorf("creating cloudcontrol client: %w", err)
	}

	result, err := client.ReadResource(ctx, request)
	if err != nil {
		return nil, err
	}
	if result.ErrorCode != "" {
		return result, nil
	}

	var props map[string]any
	if err = json.Unmarshal([]byte(result.Properties), &props); err != nil {
		return nil, fmt.Errorf("unmarshal firewall properties: %w", err)
	}

	props["EndpointIdsByAz"] = endpointIdsByAz(props)

	out, err := json.Marshal(props)
	if err != nil {
		return nil, fmt.Errorf("marshal enriched firewall properties: %w", err)
	}
	result.Properties = string(out)
	return result, nil
}

// endpointIdsByAz converts the read-only EndpointIds list (["<az>:<endpoint-id>"])
// into a map keyed by availability zone. Malformed entries (missing the colon, or
// an empty az/endpoint) are skipped.
func endpointIdsByAz(props map[string]any) map[string]string {
	byAz := map[string]string{}
	raw, ok := props["EndpointIds"].([]any)
	if !ok {
		return byAz
	}
	for _, e := range raw {
		s, ok := e.(string)
		if !ok {
			continue
		}
		az, endpoint, found := strings.Cut(s, ":")
		if !found || az == "" || endpoint == "" {
			continue
		}
		byAz[az] = endpoint
	}
	return byAz
}

// The remaining Provisioner methods are unreachable: only Read and CheckStatus are
// registered, so Create/Update/Delete/List always route to CloudControl in aws.go.
func (f *Firewall) Create(_ context.Context, _ *resource.CreateRequest) (*resource.CreateResult, error) {
	return nil, fmt.Errorf("create not implemented - cloudcontrol handles this")
}

func (f *Firewall) Update(_ context.Context, _ *resource.UpdateRequest) (*resource.UpdateResult, error) {
	return nil, fmt.Errorf("update not implemented - cloudcontrol handles this")
}

func (f *Firewall) Delete(_ context.Context, _ *resource.DeleteRequest) (*resource.DeleteResult, error) {
	return nil, fmt.Errorf("delete not implemented - cloudcontrol handles this")
}

func (f *Firewall) List(_ context.Context, _ *resource.ListRequest) (*resource.ListResult, error) {
	return nil, fmt.Errorf("list not implemented - cloudcontrol handles this")
}

// Status drives the CloudControl create/update status check, but withholds
// Success until the firewall's per-AZ VPC endpoints are populated. Core
// ResolveCache treats a property that is missing after a successful Read as a
// terminal resolve failure (not a retry), so reporting Success before
// EndpointIdsByAz is populated would make dependent routes fail late and
// terminally. Returning InProgress keeps the operator polling instead.
func (f *Firewall) Status(ctx context.Context, request *resource.StatusRequest) (*resource.StatusResult, error) {
	client, err := f.getClient()
	if err != nil {
		return nil, fmt.Errorf("creating cloudcontrol client: %w", err)
	}

	result, err := client.StatusResource(ctx, request, f.Read)
	if err != nil {
		return nil, err
	}
	if result == nil || result.ProgressResult == nil {
		return result, nil
	}

	pr := result.ProgressResult
	if pr.OperationStatus != resource.OperationStatusSuccess {
		return result, nil
	}
	// Only create/update success carries (and needs) populated endpoints. Delete
	// success has no ResourceProperties (ccx does not read deleted resources), so
	// gating it would flip a finished delete back to InProgress and poll until
	// timeout. Pass non-create/update success through unchanged.
	if pr.Operation != resource.OperationCreate && pr.Operation != resource.OperationUpdate {
		return result, nil
	}
	if endpointsPopulated(pr.ResourceProperties) {
		return result, nil
	}

	return &resource.StatusResult{
		ProgressResult: &resource.ProgressResult{
			Operation:       pr.Operation,
			OperationStatus: resource.OperationStatusInProgress,
			RequestID:       request.RequestID,
			NativeID:        request.NativeID,
			StatusMessage:   "waiting for firewall VPC endpoints to be assigned",
		},
	}, nil
}

// endpointsPopulated reports whether the enriched properties carry at least one
// per-AZ endpoint id.
func endpointsPopulated(properties json.RawMessage) bool {
	if len(properties) == 0 {
		return false
	}
	var p struct {
		EndpointIdsByAz map[string]string `json:"EndpointIdsByAz"`
	}
	if err := json.Unmarshal(properties, &p); err != nil {
		return false
	}
	return len(p.EndpointIdsByAz) > 0
}
