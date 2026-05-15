// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package ses

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

// EventDestination is the AWS::SES::ConfigurationSetEventDestination provisioner.
//
// CloudControl returns only the EventDestinationName ("bounces") in
// ProgressEvent.Identifier after Create, but the resource's primary identifier
// is the composite "<ConfigurationSetName>|<EventDestinationName>". The
// post-Create Read in ccx (both the synchronous-success path in
// CreateResource and the async polling path in StatusResource) therefore
// fails with InternalFailure, and the agent persists with no properties.
// Every nested field of EventDestination then appears missing in the actual
// resource state.
//
// We work around this without keeping any per-request state on the
// provisioner — the registry rebuilds a fresh provisioner instance for every
// operation, so instance fields would not survive the Create→Status handoff.
// Instead we derive the ConfigurationSetName from data the request already
// carries:
//
//   - Create reads it from request.Properties and rewrites the response
//     NativeID into composite form. If CloudControl returned
//     OperationStatusSuccess synchronously, we also redo the post-create
//     Read with the composite identifier (ccx's internal Read with the
//     bare name silently fails and leaves ResourceProperties empty).
//   - Status receives request.NativeID — which is the composite that
//     Create persisted — and splits the ConfigurationSetName back out of
//     it for the post-success Read callback and for any final NativeID
//     rewrite.
//
// Read/Update/Delete just delegate to ccx; the agent passes the composite
// NativeID it persisted at Create time, which CloudControl accepts as the
// primary identifier.
type EventDestination struct {
	cfg *config.Config
}

var _ prov.Provisioner = &EventDestination{}

func init() {
	registry.Register("AWS::SES::ConfigurationSetEventDestination",
		[]resource.Operation{
			resource.OperationRead,
			resource.OperationCreate,
			resource.OperationUpdate,
			resource.OperationCheckStatus,
			resource.OperationDelete,
		},
		func(cfg *config.Config) prov.Provisioner {
			return &EventDestination{cfg: cfg}
		})
}

func (e *EventDestination) Create(ctx context.Context, request *resource.CreateRequest) (*resource.CreateResult, error) {
	var props struct {
		ConfigurationSetName string `json:"ConfigurationSetName"`
	}
	if err := json.Unmarshal(request.Properties, &props); err != nil {
		return nil, fmt.Errorf("ses eventdestination: parse properties: %w", err)
	}
	if props.ConfigurationSetName == "" {
		return nil, fmt.Errorf("ses eventdestination: ConfigurationSetName missing in Create request")
	}

	ccxClient, err := ccx.NewClient(e.cfg)
	if err != nil {
		return nil, err
	}

	result, err := ccxClient.CreateResource(ctx, request)
	if err != nil {
		return nil, err
	}

	rewriteToComposite(result.ProgressResult, props.ConfigurationSetName)

	// Sync-success case: ccx.CreateResource already attempted a post-create
	// Read with the bare identifier and got InternalFailure, so
	// ResourceProperties is empty. Redo the Read with the composite
	// identifier we just constructed.
	if result.ProgressResult != nil &&
		result.ProgressResult.OperationStatus == resource.OperationStatusSuccess &&
		len(result.ProgressResult.ResourceProperties) == 0 &&
		strings.Contains(result.ProgressResult.NativeID, "|") {
		readResult, readErr := ccxClient.ReadResource(ctx, &resource.ReadRequest{
			NativeID:     result.ProgressResult.NativeID,
			ResourceType: request.ResourceType,
			TargetConfig: request.TargetConfig,
		})
		if readErr == nil && readResult != nil && readResult.ErrorCode == "" && readResult.Properties != "" {
			result.ProgressResult.ResourceProperties = json.RawMessage(readResult.Properties)
		}
	}

	return result, nil
}

func (e *EventDestination) Update(ctx context.Context, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	ccxClient, err := ccx.NewClient(e.cfg)
	if err != nil {
		return nil, err
	}
	return ccxClient.UpdateResource(ctx, request)
}

func (e *EventDestination) Delete(ctx context.Context, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	ccxClient, err := ccx.NewClient(e.cfg)
	if err != nil {
		return nil, err
	}
	return ccxClient.DeleteResource(ctx, request)
}

func (e *EventDestination) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	ccxClient, err := ccx.NewClient(e.cfg)
	if err != nil {
		return nil, err
	}
	return ccxClient.ReadResource(ctx, request)
}

// Status wraps ccx.StatusResource so the post-success Read uses the
// composite "<ConfigurationSetName>|<EventDestinationName>" identifier
// instead of the bare name returned in CloudControl's ProgressEvent. It also
// rewrites any bare NativeID in the result back into composite form so
// future operations don't regress.
func (e *EventDestination) Status(ctx context.Context, request *resource.StatusRequest) (*resource.StatusResult, error) {
	ccxClient, err := ccx.NewClient(e.cfg)
	if err != nil {
		return nil, err
	}

	csName := configurationSetNameFromComposite(request.NativeID)

	read := func(rctx context.Context, rreq *resource.ReadRequest) (*resource.ReadResult, error) {
		if csName != "" && !strings.Contains(rreq.NativeID, "|") {
			rreqCopy := *rreq
			rreqCopy.NativeID = csName + "|" + rreq.NativeID
			return ccxClient.ReadResource(rctx, &rreqCopy)
		}
		return ccxClient.ReadResource(rctx, rreq)
	}

	result, err := ccxClient.StatusResource(ctx, request, read)
	if err != nil {
		return nil, err
	}

	rewriteToComposite(result.ProgressResult, csName)
	return result, nil
}

func (e *EventDestination) List(ctx context.Context, request *resource.ListRequest) (*resource.ListResult, error) {
	return nil, fmt.Errorf("list not implemented for EventDestination provisioner - cloudcontrol natively supports this operation")
}

// rewriteToComposite mutates pr in place: if NativeID is a bare
// EventDestinationName (no '|' separator) and we know the
// ConfigurationSetName, prefix it. No-op when pr is nil, csName is empty,
// or the NativeID is already composite.
//
// When pr.NativeID is empty (async create — CloudControl returned a
// RequestID with no identifier yet), we stash csName as a "csName|"
// placeholder so the subsequent Status poll's request.NativeID carries
// the csName context the readFunc needs to rewrite the eventually-
// returned bare identifier. The placeholder is overwritten with the
// actual composite once the post-success Read fires inside Status.
func rewriteToComposite(pr *resource.ProgressResult, csName string) {
	if pr == nil || csName == "" {
		return
	}
	if pr.NativeID == "" {
		pr.NativeID = csName + "|"
		return
	}
	if strings.Contains(pr.NativeID, "|") {
		return
	}
	pr.NativeID = csName + "|" + pr.NativeID
}

// configurationSetNameFromComposite returns the ConfigurationSetName segment
// of a "<csName>|<edName>" NativeID, or "" if the input isn't composite.
func configurationSetNameFromComposite(nativeID string) string {
	idx := strings.Index(nativeID, "|")
	if idx <= 0 {
		return ""
	}
	return nativeID[:idx]
}
