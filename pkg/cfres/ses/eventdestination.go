// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package ses

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	sesv2types "github.com/aws/aws-sdk-go-v2/service/sesv2/types"

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
	cfg              *config.Config
	sesClientFactory func(cfg *config.Config) (SesV2ClientInterface, error)
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
			return &EventDestination{
				cfg:              cfg,
				sesClientFactory: defaultSesV2ClientFactory,
			}
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
	// ResourceProperties is empty. Redo the Read via the plugin's own Read
	// (SES SDK) — CCAPI rejects the composite identifier with a
	// ValidationException for this resource type.
	if result.ProgressResult != nil &&
		result.ProgressResult.OperationStatus == resource.OperationStatusSuccess &&
		len(result.ProgressResult.ResourceProperties) == 0 &&
		strings.Contains(result.ProgressResult.NativeID, "|") {
		readResult, readErr := e.Read(ctx, &resource.ReadRequest{
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

// Read uses the SESv2 SDK directly instead of CloudControl. CCAPI's
// GetResource for AWS::SES::ConfigurationSetEventDestination rejects the
// composite "<csName>|<edName>" identifier as
// "ValidationException: not valid for identifier [/properties/Id]" — the
// resource isn't fully supported under the CCAPI Read path. SESv2's
// GetConfigurationSetEventDestinations works fine when given the
// ConfigurationSetName, so we walk the parent CS's destinations and
// pick the matching one by name.
//
// The placeholder NativeID format ("csName|") set by Create is intentionally
// accepted: when the bare segment after "|" is empty, we return an empty
// (but well-formed) result rather than failing — that lets ccx.StatusResource's
// post-success Read complete cleanly even though we don't yet have an
// EventDestinationName.
func (e *EventDestination) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	csName, edName, ok := splitComposite(request.NativeID)
	if !ok {
		// Bare identifier (no "|") — fall back to CCAPI; it'll likely fail with
		// the same ValidationException the docstring describes, but we keep
		// the path for parity with other custom Reads.
		ccxClient, err := ccx.NewClient(e.cfg)
		if err != nil {
			return nil, err
		}
		return ccxClient.ReadResource(ctx, request)
	}
	if edName == "" {
		// Placeholder ("csName|"): no event-destination yet, return empty.
		return &resource.ReadResult{ResourceType: request.ResourceType}, nil
	}

	sesClient, err := e.sesClientFactory(e.cfg)
	if err != nil {
		return nil, fmt.Errorf("ses eventdestination Read: build SES client: %w", err)
	}

	resp, err := sesClient.GetConfigurationSetEventDestinations(ctx, &sesv2.GetConfigurationSetEventDestinationsInput{
		ConfigurationSetName: &csName,
	})
	if err != nil {
		return nil, fmt.Errorf("ses eventdestination Read: GetConfigurationSetEventDestinations(%q): %w", csName, err)
	}

	for _, ed := range resp.EventDestinations {
		if ed.Name != nil && *ed.Name == edName {
			props, marshalErr := marshalEventDestinationToCFN(csName, ed)
			if marshalErr != nil {
				return nil, marshalErr
			}
			return &resource.ReadResult{
				ResourceType: request.ResourceType,
				Properties:   props,
			}, nil
		}
	}

	// Not found.
	return &resource.ReadResult{
		ResourceType: request.ResourceType,
		ErrorCode:    "NotFound",
	}, nil
}

// splitComposite splits "<csName>|<edName>" into its parts. ok is false if
// the identifier contains no "|" separator.
func splitComposite(nativeID string) (csName, edName string, ok bool) {
	idx := strings.Index(nativeID, "|")
	if idx < 0 {
		return "", "", false
	}
	return nativeID[:idx], nativeID[idx+1:], true
}

// marshalEventDestinationToCFN converts a SESv2 EventDestination into the
// CloudFormation property shape that the conformance harness and rest of
// the agent expect. Currently covers Name, Enabled, MatchingEventTypes,
// CloudWatchDestination, and the four destination-target sub-objects (Sns,
// Kinesis, EventBridge, Pinpoint) — the same set the schema/test exercise.
func marshalEventDestinationToCFN(csName string, ed sesv2types.EventDestination) (string, error) {
	edMap := map[string]any{
		"Enabled": ed.Enabled,
	}
	if ed.Name != nil {
		edMap["Name"] = *ed.Name
	}
	if len(ed.MatchingEventTypes) > 0 {
		types := make([]string, len(ed.MatchingEventTypes))
		for i, t := range ed.MatchingEventTypes {
			types[i] = string(t)
		}
		edMap["MatchingEventTypes"] = types
	}
	if ed.CloudWatchDestination != nil {
		dims := make([]map[string]any, 0, len(ed.CloudWatchDestination.DimensionConfigurations))
		for _, dc := range ed.CloudWatchDestination.DimensionConfigurations {
			dim := map[string]any{}
			if dc.DimensionName != nil {
				dim["DimensionName"] = *dc.DimensionName
			}
			if dc.DefaultDimensionValue != nil {
				dim["DefaultDimensionValue"] = *dc.DefaultDimensionValue
			}
			dim["DimensionValueSource"] = string(dc.DimensionValueSource)
			dims = append(dims, dim)
		}
		edMap["CloudWatchDestination"] = map[string]any{"DimensionConfigurations": dims}
	}
	if ed.SnsDestination != nil && ed.SnsDestination.TopicArn != nil {
		edMap["SnsDestination"] = map[string]any{"TopicARN": *ed.SnsDestination.TopicArn}
	}
	if ed.KinesisFirehoseDestination != nil {
		k := map[string]any{}
		if ed.KinesisFirehoseDestination.IamRoleArn != nil {
			k["IAMRoleARN"] = *ed.KinesisFirehoseDestination.IamRoleArn
		}
		if ed.KinesisFirehoseDestination.DeliveryStreamArn != nil {
			k["DeliveryStreamARN"] = *ed.KinesisFirehoseDestination.DeliveryStreamArn
		}
		edMap["KinesisFirehoseDestination"] = k
	}
	if ed.EventBridgeDestination != nil && ed.EventBridgeDestination.EventBusArn != nil {
		edMap["EventBridgeDestination"] = map[string]any{"EventBusArn": *ed.EventBridgeDestination.EventBusArn}
	}
	if ed.PinpointDestination != nil && ed.PinpointDestination.ApplicationArn != nil {
		edMap["PinpointDestination"] = map[string]any{"ApplicationArn": *ed.PinpointDestination.ApplicationArn}
	}

	out := map[string]any{
		"Id":                   csName + "|" + *ed.Name,
		"ConfigurationSetName": csName,
		"EventDestination":     edMap,
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", fmt.Errorf("ses eventdestination Read: marshal CFN response: %w", err)
	}
	return string(b), nil
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

	// Route the post-success Read through the plugin's own Read (which
	// uses SESv2.GetConfigurationSetEventDestinations) instead of
	// ccxClient.ReadResource directly. CCAPI's GetResource for this
	// resource type rejects the composite identifier with a
	// ValidationException, so we must avoid CCAPI on the read path.
	read := func(rctx context.Context, rreq *resource.ReadRequest) (*resource.ReadResult, error) {
		if csName != "" && !strings.Contains(rreq.NativeID, "|") {
			rreqCopy := *rreq
			rreqCopy.NativeID = csName + "|" + rreq.NativeID
			return e.Read(rctx, &rreqCopy)
		}
		return e.Read(rctx, rreq)
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
