// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package ses

import (
	"context"
	"encoding/json"
	"errors"
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

// Update uses the SESv2 SDK directly instead of CloudControl. The plugin's
// formae-core handoff (2026-05-15) confirmed that CCAPI's UpdateResource
// returns a synchronous Failure within ~4 ms for this resource type —
// consistent with CCAPI rejecting the composite "<csName>|<edName>"
// identifier the same way it does on GetResource (ValidationException:
// "not valid for identifier [/properties/Id]"). SESv2's
// UpdateConfigurationSetEventDestination accepts the parts separately and
// applies the update synchronously, so we issue the SDK call and then
// re-Read to populate ResourceProperties for the agent.
func (e *EventDestination) Update(ctx context.Context, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	csName, edName, ok := splitComposite(request.NativeID)
	if !ok || csName == "" || edName == "" {
		return nil, fmt.Errorf("ses eventdestination Update: NativeID %q is not a composite <csName>|<edName>", request.NativeID)
	}

	desired, err := parseEventDestinationFromDesired(request.DesiredProperties)
	if err != nil {
		return nil, fmt.Errorf("ses eventdestination Update: %w", err)
	}

	sesClient, err := e.sesClientFactory(e.cfg)
	if err != nil {
		return nil, fmt.Errorf("ses eventdestination Update: build SES client: %w", err)
	}

	_, err = sesClient.UpdateConfigurationSetEventDestination(ctx, &sesv2.UpdateConfigurationSetEventDestinationInput{
		ConfigurationSetName: &csName,
		EventDestinationName: &edName,
		EventDestination:     desired,
	})
	if err != nil {
		return nil, fmt.Errorf("ses eventdestination Update: UpdateConfigurationSetEventDestination(%q, %q): %w", csName, edName, err)
	}

	// SES doesn't return the updated state — re-Read via our own Read so
	// the agent gets ResourceProperties populated and any drift detected
	// post-update is based on the actual AWS-side response, not on what
	// we sent.
	readResult, err := e.Read(ctx, &resource.ReadRequest{
		NativeID:     request.NativeID,
		ResourceType: request.ResourceType,
		TargetConfig: request.TargetConfig,
	})
	updateResult := &resource.UpdateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:       resource.OperationUpdate,
			OperationStatus: resource.OperationStatusSuccess,
			NativeID:        request.NativeID,
		},
	}
	if err == nil && readResult != nil && readResult.ErrorCode == "" && readResult.Properties != "" {
		updateResult.ProgressResult.ResourceProperties = json.RawMessage(readResult.Properties)
	}
	return updateResult, nil
}

// Delete uses the SESv2 SDK directly. Same root cause as the custom Read
// and Update: CCAPI's DeleteResource rejects the composite identifier with
// a ValidationException for this resource type. SESv2's
// DeleteConfigurationSetEventDestination accepts csName and edName
// separately and applies the delete synchronously.
//
// AWS treats deleting a non-existent destination as a NotFound error; we
// translate that to a success ProgressResult so the agent's destroy flow
// is idempotent (matches the ccx.DeleteResource behavior for the same
// case at pkg/ccx/client.go).
func (e *EventDestination) Delete(ctx context.Context, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	csName, edName, ok := splitComposite(request.NativeID)
	if !ok || csName == "" || edName == "" {
		return nil, fmt.Errorf("ses eventdestination Delete: NativeID %q is not a composite <csName>|<edName>", request.NativeID)
	}

	sesClient, err := e.sesClientFactory(e.cfg)
	if err != nil {
		return nil, fmt.Errorf("ses eventdestination Delete: build SES client: %w", err)
	}

	_, err = sesClient.DeleteConfigurationSetEventDestination(ctx, &sesv2.DeleteConfigurationSetEventDestinationInput{
		ConfigurationSetName: &csName,
		EventDestinationName: &edName,
	})
	if err != nil {
		var notFound *sesv2types.NotFoundException
		if errors.As(err, &notFound) {
			return &resource.DeleteResult{
				ProgressResult: &resource.ProgressResult{
					Operation:       resource.OperationDelete,
					OperationStatus: resource.OperationStatusSuccess,
					NativeID:        request.NativeID,
					ErrorCode:       resource.OperationErrorCodeNotFound,
				},
			}, nil
		}
		return nil, fmt.Errorf("ses eventdestination Delete: DeleteConfigurationSetEventDestination(%q, %q): %w", csName, edName, err)
	}

	return &resource.DeleteResult{
		ProgressResult: &resource.ProgressResult{
			Operation:       resource.OperationDelete,
			OperationStatus: resource.OperationStatusSuccess,
			NativeID:        request.NativeID,
		},
	}, nil
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

// parseEventDestinationFromDesired extracts the EventDestination sub-object
// from the CFN-shape DesiredProperties JSON and converts it into the SESv2
// SDK's EventDestinationDefinition. The inverse of marshalEventDestinationToCFN —
// keep the two in sync. CFN renders some ARN fields with uppercase suffixes
// (TopicARN vs SDK TopicArn) so we cannot just rely on Go struct-tag JSON
// unmarshaling against the SDK types directly.
func parseEventDestinationFromDesired(desired json.RawMessage) (*sesv2types.EventDestinationDefinition, error) {
	if len(desired) == 0 {
		return nil, fmt.Errorf("DesiredProperties is empty")
	}

	var wrapper struct {
		EventDestination *cfnEventDestination `json:"EventDestination"`
	}
	if err := json.Unmarshal(desired, &wrapper); err != nil {
		return nil, fmt.Errorf("parse DesiredProperties: %w", err)
	}
	if wrapper.EventDestination == nil {
		return nil, fmt.Errorf("DesiredProperties has no EventDestination object")
	}
	return wrapper.EventDestination.toSDK(), nil
}

type cfnEventDestination struct {
	Enabled                    bool                          `json:"Enabled"`
	MatchingEventTypes         []string                      `json:"MatchingEventTypes,omitempty"`
	CloudWatchDestination      *cfnCloudWatchDestination     `json:"CloudWatchDestination,omitempty"`
	SnsDestination             *cfnSnsDestination            `json:"SnsDestination,omitempty"`
	KinesisFirehoseDestination *cfnKinesisFirehoseDestination `json:"KinesisFirehoseDestination,omitempty"`
	EventBridgeDestination     *cfnEventBridgeDestination    `json:"EventBridgeDestination,omitempty"`
	PinpointDestination        *cfnPinpointDestination       `json:"PinpointDestination,omitempty"`
}

type cfnCloudWatchDestination struct {
	DimensionConfigurations []cfnDimensionConfig `json:"DimensionConfigurations,omitempty"`
}

type cfnDimensionConfig struct {
	DimensionName         string `json:"DimensionName"`
	DefaultDimensionValue string `json:"DefaultDimensionValue"`
	DimensionValueSource  string `json:"DimensionValueSource"`
}

type cfnSnsDestination struct {
	TopicARN string `json:"TopicARN"`
}

type cfnKinesisFirehoseDestination struct {
	IAMRoleARN        string `json:"IAMRoleARN"`
	DeliveryStreamARN string `json:"DeliveryStreamARN"`
}

type cfnEventBridgeDestination struct {
	EventBusArn string `json:"EventBusArn"`
}

type cfnPinpointDestination struct {
	ApplicationArn string `json:"ApplicationArn"`
}

func (c *cfnEventDestination) toSDK() *sesv2types.EventDestinationDefinition {
	out := &sesv2types.EventDestinationDefinition{
		Enabled: c.Enabled,
	}
	if len(c.MatchingEventTypes) > 0 {
		types := make([]sesv2types.EventType, len(c.MatchingEventTypes))
		for i, t := range c.MatchingEventTypes {
			types[i] = sesv2types.EventType(t)
		}
		out.MatchingEventTypes = types
	}
	if c.CloudWatchDestination != nil {
		dims := make([]sesv2types.CloudWatchDimensionConfiguration, len(c.CloudWatchDestination.DimensionConfigurations))
		for i, dc := range c.CloudWatchDestination.DimensionConfigurations {
			name := dc.DimensionName
			def := dc.DefaultDimensionValue
			dims[i] = sesv2types.CloudWatchDimensionConfiguration{
				DimensionName:         &name,
				DefaultDimensionValue: &def,
				DimensionValueSource:  sesv2types.DimensionValueSource(dc.DimensionValueSource),
			}
		}
		out.CloudWatchDestination = &sesv2types.CloudWatchDestination{DimensionConfigurations: dims}
	}
	if c.SnsDestination != nil {
		arn := c.SnsDestination.TopicARN
		out.SnsDestination = &sesv2types.SnsDestination{TopicArn: &arn}
	}
	if c.KinesisFirehoseDestination != nil {
		role := c.KinesisFirehoseDestination.IAMRoleARN
		stream := c.KinesisFirehoseDestination.DeliveryStreamARN
		out.KinesisFirehoseDestination = &sesv2types.KinesisFirehoseDestination{
			IamRoleArn:        &role,
			DeliveryStreamArn: &stream,
		}
	}
	if c.EventBridgeDestination != nil {
		bus := c.EventBridgeDestination.EventBusArn
		out.EventBridgeDestination = &sesv2types.EventBridgeDestination{EventBusArn: &bus}
	}
	if c.PinpointDestination != nil {
		app := c.PinpointDestination.ApplicationArn
		out.PinpointDestination = &sesv2types.PinpointDestination{ApplicationArn: &app}
	}
	return out
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
