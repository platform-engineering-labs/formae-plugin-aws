// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package elasticloadbalancingv2

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"

	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/ccx"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres/prov"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres/registry"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/config"
)

// resourceReader abstracts the ReadResource operation for testability.
type resourceReader interface {
	ReadResource(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error)
}

// Listener is a provisioner that enriches Read results with a computed Url property.
// The Url is constructed from the Listener's protocol/port and the parent ALB's DNSName.
type Listener struct {
	cfg *config.Config
	// reader is injectable for testing; nil means use a real ccx.Client.
	reader resourceReader
}

var _ prov.Provisioner = &Listener{}

func init() {
	registry.Register("AWS::ElasticLoadBalancingV2::Listener",
		[]resource.Operation{resource.OperationRead},
		func(cfg *config.Config) prov.Provisioner {
			return &Listener{cfg: cfg}
		})
}

func (l *Listener) getReader(ctx context.Context) (resourceReader, error) {
	if l.reader != nil {
		return l.reader, nil
	}
	return ccx.NewClient(l.cfg)
}

func (l *Listener) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	reader, err := l.getReader(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading AWS config: %w", err)
	}

	// Read the Listener via CloudControl
	result, err := reader.ReadResource(ctx, request)
	if err != nil {
		return nil, err
	}

	// If the read returned an error code (e.g., NotFound), pass it through
	if result.ErrorCode != "" {
		return result, nil
	}

	var propsMap map[string]any
	if err = json.Unmarshal([]byte(result.Properties), &propsMap); err != nil {
		return nil, fmt.Errorf("failed to unmarshal listener properties: %w", err)
	}

	// Enrich with computed Url
	enrichWithURL(ctx, reader, propsMap)

	transformedProps, err := json.Marshal(propsMap)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal enriched properties: %w", err)
	}

	result.Properties = string(transformedProps)
	return result, nil
}

// enrichWithURL computes and injects a Url property into the Listener's properties.
// The URL is: protocol.lower() + "://" + albDNSName [+ ":" + port if non-standard].
// Non-standard means port != 80 for HTTP or port != 443 for HTTPS.
// If the ALB cannot be read, the Url is silently omitted.
func enrichWithURL(ctx context.Context, reader resourceReader, propsMap map[string]any) {
	loadBalancerArn, ok := propsMap["LoadBalancerArn"].(string)
	if !ok || loadBalancerArn == "" {
		slog.Debug("Listener missing LoadBalancerArn, skipping URL enrichment")
		return
	}

	protocol, _ := propsMap["Protocol"].(string)
	if protocol == "" {
		slog.Debug("Listener missing Protocol, skipping URL enrichment")
		return
	}

	// Read the parent ALB to get its DNSName
	albResult, err := reader.ReadResource(ctx, &resource.ReadRequest{
		NativeID:     loadBalancerArn,
		ResourceType: "AWS::ElasticLoadBalancingV2::LoadBalancer",
	})
	if err != nil {
		slog.Warn("Failed to read parent ALB for URL enrichment",
			"loadBalancerArn", loadBalancerArn,
			"error", err)
		return
	}
	if albResult.ErrorCode != "" {
		slog.Warn("Parent ALB read returned error for URL enrichment",
			"loadBalancerArn", loadBalancerArn,
			"errorCode", albResult.ErrorCode)
		return
	}

	var albProps map[string]any
	if err = json.Unmarshal([]byte(albResult.Properties), &albProps); err != nil {
		slog.Warn("Failed to unmarshal ALB properties for URL enrichment",
			"error", err)
		return
	}

	dnsName, ok := albProps["DNSName"].(string)
	if !ok || dnsName == "" {
		slog.Debug("ALB missing DNSName, skipping URL enrichment")
		return
	}

	// Construct the URL
	scheme := strings.ToLower(protocol)
	url := scheme + "://" + dnsName

	// Add port only if non-standard
	if port, ok := getPort(propsMap); ok {
		if !isStandardPort(scheme, port) {
			url = fmt.Sprintf("%s:%d", url, port)
		}
	}

	propsMap["Url"] = url
}

// getPort extracts the port from the properties map, handling both float64 (JSON default)
// and int types.
func getPort(propsMap map[string]any) (int, bool) {
	switch v := propsMap["Port"].(type) {
	case float64:
		return int(v), true
	case int:
		return v, true
	case json.Number:
		n, err := v.Int64()
		if err != nil {
			return 0, false
		}
		return int(n), true
	default:
		return 0, false
	}
}

// isStandardPort returns true if the port is the standard port for the given scheme.
func isStandardPort(scheme string, port int) bool {
	return (scheme == "http" && port == 80) || (scheme == "https" && port == 443)
}

func (l *Listener) Create(_ context.Context, _ *resource.CreateRequest) (*resource.CreateResult, error) {
	return nil, fmt.Errorf("operation not implemented - cloudcontrol handles this")
}

func (l *Listener) Update(_ context.Context, _ *resource.UpdateRequest) (*resource.UpdateResult, error) {
	return nil, fmt.Errorf("operation not implemented - cloudcontrol handles this")
}

func (l *Listener) Delete(_ context.Context, _ *resource.DeleteRequest) (*resource.DeleteResult, error) {
	return nil, fmt.Errorf("operation not implemented - cloudcontrol handles this")
}

func (l *Listener) Status(_ context.Context, _ *resource.StatusRequest) (*resource.StatusResult, error) {
	return nil, fmt.Errorf("operation not implemented - cloudcontrol handles this")
}

func (l *Listener) List(_ context.Context, _ *resource.ListRequest) (*resource.ListResult, error) {
	return nil, fmt.Errorf("operation not implemented - cloudcontrol handles this")
}
