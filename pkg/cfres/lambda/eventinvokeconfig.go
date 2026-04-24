// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package lambda

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awslambda "github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres/prov"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres/registry"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/config"
)

// EventInvokeConfig is a custom provisioner for AWS::Lambda::EventInvokeConfig.
// CloudControl's Update handler is broken for this type: even a patch that
// only touches mutable scalar fields (MaximumRetryAttempts,
// MaximumEventAgeInSeconds) is rejected with a ValidationException against
// sub-objects the patch never addresses. The reason is that CC returns
// `DestinationConfig: {OnFailure:{}, OnSuccess:{}}` on Read whether or not
// the caller set it, and on Update validates the full post-patch state —
// where the empty OnFailure/OnSuccess fail the schema's
// "if present, Destination is required" rule.
//
// Routing Update through the native Lambda SDK's
// UpdateFunctionEventInvokeConfig sidesteps the CC validator entirely and
// updates only the fields the caller specified. Create / Read / Delete
// continue to use CloudControl.
//
// Scope of the custom path is intentionally narrow — only Update is
// registered; other operations fall back to the default CloudControl
// handler via the registry.
type EventInvokeConfig struct {
	cfg *config.Config
}

// eventInvokeConfigClient is the narrow Lambda-SDK subset used by Update.
// Keeping it behind an interface lets the unit tests mock it without
// spinning up an AWS session.
type eventInvokeConfigClient interface {
	UpdateFunctionEventInvokeConfig(ctx context.Context, input *awslambda.UpdateFunctionEventInvokeConfigInput, optFns ...func(*awslambda.Options)) (*awslambda.UpdateFunctionEventInvokeConfigOutput, error)
}

var _ prov.Provisioner = &EventInvokeConfig{}

func init() {
	registry.Register("AWS::Lambda::EventInvokeConfig",
		[]resource.Operation{resource.OperationUpdate},
		func(cfg *config.Config) prov.Provisioner {
			return &EventInvokeConfig{cfg: cfg}
		})
}

func (e *EventInvokeConfig) Update(ctx context.Context, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	awsCfg, err := e.cfg.ToAwsConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading AWS config: %w", err)
	}
	return e.updateWithClient(ctx, awslambda.NewFromConfig(awsCfg), request)
}

func (e *EventInvokeConfig) updateWithClient(ctx context.Context, client eventInvokeConfigClient, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	// NativeID is the composite <FunctionName>|<Qualifier> produced by
	// CloudControl on create. Use SplitN(..., 2) in case the qualifier
	// itself contains "|" (technically legal per Lambda's
	// [a-zA-Z0-9$_-]{1,129} qualifier regex — "|" is not in that set,
	// but guard against CC one day returning something unexpected).
	parts := strings.SplitN(request.NativeID, "|", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("AWS::Lambda::EventInvokeConfig NativeID must be composite FunctionName|Qualifier, got %q", request.NativeID)
	}
	functionName, qualifier := parts[0], parts[1]

	if len(request.DesiredProperties) == 0 {
		return nil, fmt.Errorf("AWS::Lambda::EventInvokeConfig update requires DesiredProperties")
	}

	var desired eventInvokeConfigProps
	if err := json.Unmarshal(request.DesiredProperties, &desired); err != nil {
		return nil, fmt.Errorf("parsing desired properties: %w", err)
	}

	input := &awslambda.UpdateFunctionEventInvokeConfigInput{
		FunctionName: aws.String(functionName),
		Qualifier:    aws.String(qualifier),
	}
	if desired.MaximumRetryAttempts != nil {
		input.MaximumRetryAttempts = desired.MaximumRetryAttempts
	}
	if desired.MaximumEventAgeInSeconds != nil {
		input.MaximumEventAgeInSeconds = desired.MaximumEventAgeInSeconds
	}
	if dc := buildDestinationConfig(desired.DestinationConfig); dc != nil {
		input.DestinationConfig = dc
	}

	output, err := client.UpdateFunctionEventInvokeConfig(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("updating function event invoke config: %w", err)
	}

	// Merge the updated values into the caller's desired properties so
	// downstream idempotency checks see what AWS actually stored.
	var props map[string]any
	if err := json.Unmarshal(request.DesiredProperties, &props); err != nil {
		return nil, fmt.Errorf("re-parsing desired properties for merge: %w", err)
	}
	if output.MaximumRetryAttempts != nil {
		props["MaximumRetryAttempts"] = *output.MaximumRetryAttempts
	}
	if output.MaximumEventAgeInSeconds != nil {
		props["MaximumEventAgeInSeconds"] = *output.MaximumEventAgeInSeconds
	}
	resultJSON, err := json.Marshal(props)
	if err != nil {
		return nil, fmt.Errorf("marshalling result properties: %w", err)
	}

	return &resource.UpdateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationUpdate,
			OperationStatus:    resource.OperationStatusSuccess,
			NativeID:           request.NativeID,
			ResourceProperties: resultJSON,
		},
	}, nil
}

// eventInvokeConfigProps mirrors the subset of Lambda::EventInvokeConfig
// fields we need to read off the desired state. Anything not listed here
// is ignored — the AWS UpdateFunctionEventInvokeConfig API takes a very
// small set of fields and extras would be irrelevant.
type eventInvokeConfigProps struct {
	MaximumRetryAttempts     *int32                        `json:"MaximumRetryAttempts,omitempty"`
	MaximumEventAgeInSeconds *int32                        `json:"MaximumEventAgeInSeconds,omitempty"`
	DestinationConfig        *eventInvokeConfigDestination `json:"DestinationConfig,omitempty"`
}

type eventInvokeConfigDestination struct {
	OnFailure *eventInvokeConfigTarget `json:"OnFailure,omitempty"`
	OnSuccess *eventInvokeConfigTarget `json:"OnSuccess,omitempty"`
}

type eventInvokeConfigTarget struct {
	Destination *string `json:"Destination,omitempty"`
}

// buildDestinationConfig returns a DestinationConfig only when it carries
// at least one real target — i.e. an OnFailure or OnSuccess entry with a
// non-empty Destination. CloudControl's Read returns empty OnFailure:{}
// and OnSuccess:{} sub-objects even when the caller never set them, and
// forwarding those to UpdateFunctionEventInvokeConfig earns an
// InvalidParameterValueException ("destination is required"). Returning
// nil here causes the SDK call to omit DestinationConfig entirely, which
// is the correct no-op.
func buildDestinationConfig(src *eventInvokeConfigDestination) *lambdatypes.DestinationConfig {
	if src == nil {
		return nil
	}
	out := &lambdatypes.DestinationConfig{}
	hasAny := false
	if src.OnFailure != nil && src.OnFailure.Destination != nil && *src.OnFailure.Destination != "" {
		out.OnFailure = &lambdatypes.OnFailure{Destination: src.OnFailure.Destination}
		hasAny = true
	}
	if src.OnSuccess != nil && src.OnSuccess.Destination != nil && *src.OnSuccess.Destination != "" {
		out.OnSuccess = &lambdatypes.OnSuccess{Destination: src.OnSuccess.Destination}
		hasAny = true
	}
	if !hasAny {
		return nil
	}
	return out
}

// The remaining Provisioner methods are not implemented because this
// provisioner is only registered for OperationUpdate. The registry routes
// other operations to the default CloudControl path.

func (e *EventInvokeConfig) Create(_ context.Context, _ *resource.CreateRequest) (*resource.CreateResult, error) {
	return nil, fmt.Errorf("AWS::Lambda::EventInvokeConfig custom provisioner only implements Update")
}

func (e *EventInvokeConfig) Read(_ context.Context, _ *resource.ReadRequest) (*resource.ReadResult, error) {
	return nil, fmt.Errorf("AWS::Lambda::EventInvokeConfig custom provisioner only implements Update")
}

func (e *EventInvokeConfig) Delete(_ context.Context, _ *resource.DeleteRequest) (*resource.DeleteResult, error) {
	return nil, fmt.Errorf("AWS::Lambda::EventInvokeConfig custom provisioner only implements Update")
}

func (e *EventInvokeConfig) Status(_ context.Context, _ *resource.StatusRequest) (*resource.StatusResult, error) {
	return nil, fmt.Errorf("AWS::Lambda::EventInvokeConfig custom provisioner only implements Update")
}

func (e *EventInvokeConfig) List(_ context.Context, _ *resource.ListRequest) (*resource.ListResult, error) {
	return nil, fmt.Errorf("AWS::Lambda::EventInvokeConfig custom provisioner only implements Update")
}
