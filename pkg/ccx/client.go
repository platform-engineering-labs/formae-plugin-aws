// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package ccx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	"github.com/aws/aws-sdk-go-v2/service/cloudcontrol"
	cctypes "github.com/aws/aws-sdk-go-v2/service/cloudcontrol/types"
	"github.com/platform-engineering-labs/formae/pkg/plugin"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"

	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/config"
	helper "github.com/platform-engineering-labs/formae-plugin-aws/pkg/helper"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/props"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/ptr"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/status"
)

// tgNotAssociatedPattern matches the AWS error surfaced when an ECS service
// is created before its target group has been wired to a load balancer.
// Combined with HandlerErrorCodeInvalidRequest in StatusResource, this turns
// the error into a "keep polling" signal so the PluginOperator's existing
// retry budget can absorb the race.
var tgNotAssociatedPattern = regexp.MustCompile(`(?i)target group .* does not have an associated load balancer`)

// cloudControlAPI defines the CloudControl operations used by Client.
type cloudControlAPI interface {
	CreateResource(ctx context.Context, params *cloudcontrol.CreateResourceInput, optFns ...func(*cloudcontrol.Options)) (*cloudcontrol.CreateResourceOutput, error)
	UpdateResource(ctx context.Context, params *cloudcontrol.UpdateResourceInput, optFns ...func(*cloudcontrol.Options)) (*cloudcontrol.UpdateResourceOutput, error)
	DeleteResource(ctx context.Context, params *cloudcontrol.DeleteResourceInput, optFns ...func(*cloudcontrol.Options)) (*cloudcontrol.DeleteResourceOutput, error)
	GetResource(ctx context.Context, params *cloudcontrol.GetResourceInput, optFns ...func(*cloudcontrol.Options)) (*cloudcontrol.GetResourceOutput, error)
	GetResourceRequestStatus(ctx context.Context, params *cloudcontrol.GetResourceRequestStatusInput, optFns ...func(*cloudcontrol.Options)) (*cloudcontrol.GetResourceRequestStatusOutput, error)
	ListResources(ctx context.Context, params *cloudcontrol.ListResourcesInput, optFns ...func(*cloudcontrol.Options)) (*cloudcontrol.ListResourcesOutput, error)
}

type Client struct {
	api cloudControlAPI

	// now supplies the current time to the request-window tracker in
	// window.go. NewClient sets it to time.Now; the clock accessor falls
	// back to time.Now itself when it's nil, so a zero-value Client (as
	// existing unit tests construct directly, e.g. &Client{api: mockAPI})
	// keeps working without going through NewClient.
	now func() time.Time

	// watchedBudget overrides defaultWatchedCallBudget for the AWS calls made
	// inside a watchdog-observed RPC. It exists so tests can drive the
	// budget-exhaustion paths without spending seconds of wall clock; zero
	// means "use the constant", which is what production always does.
	watchedBudget time.Duration

	// windowsMu guards windows, the process-local, admission-controlled
	// tracker of per-RequestID enrichment/status-error stamps. See
	// window.go for the full contract.
	windowsMu sync.Mutex
	windows   map[string]window
}

// defaultWatchedCallBudget bounds the AWS calls a plugin RPC makes while the
// agent's missing-in-action watchdog is observing it. The watchdog gives an
// operator two status-check intervals (2 x 20s = 40s) to report progress, and
// an RPC that blocks inside a retry loop reports none — so a healthy operation
// gets killed for a transient AWS throttle.
//
// With this budget the worst-case heartbeat period becomes one status-check
// interval plus one budget (20s + 5s = 25s) against that 40s window, leaving
// roughly 15s of slack for operator scheduling, mailbox contention, GC,
// transport and SDK cancellation lag. Five seconds is still long enough to
// absorb the common single-blip retry without spending an extra poll round
// trip on it.
//
// Two caveats worth keeping in view: context deadlines are cooperative, so
// this bounds when the loop stops asking for more work rather than the instant
// an in-flight call unwinds — it is "~5s", not exactly 5s. And the value is
// coupled to the agent's status-check interval: if that interval changes, this
// budget has to be re-derived against it.
const defaultWatchedCallBudget = 5 * time.Second

// watchedCallBudget returns the wall-clock budget for AWS calls made inside a
// watchdog-observed RPC, honouring a test override and falling back to the
// constant when the Client is its zero value — existing unit tests build
// &Client{api: mockAPI} directly, without going through NewClient.
func (c *Client) watchedCallBudget() time.Duration {
	if c.watchedBudget > 0 {
		return c.watchedBudget
	}
	return defaultWatchedCallBudget
}

// watchedRPCDeadline returns the single instant that every AWS call made inside
// one watchdog-observed RPC shares. The budget bounds the RPC, not each call
// within it: an RPC that makes two calls and gives each its own budget can spend
// twice the budget, and the slack the constant is derived against disappears.
// Callers derive this once on entry and hand it to every retry loop they run.
//
// It reads the real clock rather than the injectable one: the retry loops measure
// against wall clock, while Client.now exists for the request-window tracker's
// much longer, test-driven windows.
func (c *Client) watchedRPCDeadline() time.Time {
	return time.Now().Add(c.watchedCallBudget())
}

var IgnoredFields = map[string][]string{
	"AWS::EC2::SecurityGroup":                      {"$.SecurityGroupEgress", "$.SecurityGroupIngress"},
	"AWS::IAM::Role":                               {"$.Policies"},
	"AWS::ElasticBeanstalk::ConfigurationTemplate": {"$.OptionSettings"},
	// Targets is populated at runtime by ECS Services (and other consumers
	// calling register-targets); LoadBalancerArns is populated when a
	// Listener attaches the TG to an ALB. Neither is meaningfully
	// user-settable, and tracking them in formae state makes the
	// Synchronizer write a new resource version on every task placement
	// or attach — which then trips the reconcile drift gate on the next
	// apply. Strip them in Read so they never enter the stored state.
	"AWS::ElasticLoadBalancingV2::TargetGroup": {"$.Targets", "$.LoadBalancerArns"},
}

// normalizeCompositeIdentifier fixes inconsistencies in CloudControl composite
// identifiers. CC Create/Status returns full ARNs in composite parts (e.g.
// "service-arn|cluster-arn") but CC List returns short names (e.g.
// "service-arn|cluster-name"). Normalize to short names to match List format,
// since that's what discovery uses and the identifier must match for inventory
// lookups.
func normalizeCompositeIdentifier(identifier string) string {
	parts := strings.Split(identifier, "|")
	if len(parts) <= 1 {
		return identifier
	}
	for i := 1; i < len(parts); i++ {
		if strings.HasPrefix(parts[i], "arn:aws:") {
			// Extract the resource name from the ARN (last segment after /)
			if idx := strings.LastIndex(parts[i], "/"); idx >= 0 {
				parts[i] = parts[i][idx+1:]
			}
		}
	}
	return strings.Join(parts, "|")
}

func NewClient(cfg *config.Config) (*Client, error) {
	awsCfg, err := cfg.ToAwsConfig(context.Background())
	if err != nil {
		return nil, fmt.Errorf("loading AWS config: %w", err)
	}

	// Create Cloud Control Client with custom retry configuration for throttling.
	// AWS CloudControl API has strict rate limits, so we use:
	// - Fewer max attempts (let PluginOperator handle retries at a higher level)
	// - Longer max backoff to give AWS time to recover from throttling
	retryer := retry.NewStandard(func(o *retry.StandardOptions) {
		o.MaxAttempts = 2               // Reduce from default 3 to fail faster to PluginOperator
		o.MaxBackoff = 30 * time.Second // Allow longer backoff for throttling
	})

	return &Client{
		api: cloudcontrol.NewFromConfig(awsCfg, func(o *cloudcontrol.Options) {
			o.Retryer = retryer
		}),
		now: time.Now,
	}, nil
}

// CreateResource creates a resource using CloudControl with full request handling
func (c *Client) CreateResource(ctx context.Context, request *resource.CreateRequest) (*resource.CreateResult, error) {
	resourceProps := request.Properties

	// Handle map tags transformation if required
	if props.RequiresMapTags(request.ResourceType) {
		var properties map[string]any
		if err := json.Unmarshal(request.Properties, &properties); err != nil {
			return nil, err
		}

		if err := props.TransformTagsToMap(properties); err != nil {
			return nil, err
		}

		transformedProps, err := json.Marshal(properties)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal transformed properties: %w", err)
		}
		resourceProps = transformedProps
	}

	// Strip empty arrays and maps from properties before sending to CloudControl.
	// The PKL schema renders nullable Listing/Mapping fields as [] and {} when the
	// user didn't set them. CloudControl may reject these (e.g. Lambda Architectures
	// requires min 1 item) or interpret them differently from an absent field.
	resourceProps, err := stripEmptyCollections(resourceProps)
	if err != nil {
		return nil, fmt.Errorf("failed to strip empty collections: %w", err)
	}

	result, err := c.api.CreateResource(ctx, &cloudcontrol.CreateResourceInput{
		DesiredState: ptr.Of(string(resourceProps)),
		TypeName:     &request.ResourceType,
	})
	if err != nil {
		if pr, ok := classifyCloudControlError(err, resource.OperationCreate); ok {
			return &resource.CreateResult{ProgressResult: pr}, nil
		}
		return nil, err
	}

	identifier := ""
	if result.ProgressEvent.Identifier != nil {
		identifier = normalizeCompositeIdentifier(*result.ProgressEvent.Identifier)
	} else if result.ProgressEvent.OperationStatus == cctypes.OperationStatusSuccess {
		return nil, fmt.Errorf("create succeeded but CloudControl returned no identifier for %s", request.ResourceType)
	}

	createResult := &resource.CreateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:       resource.OperationCreate,
			OperationStatus: status.FromOperationStatus(result.ProgressEvent.OperationStatus),
			RequestID:       *result.ProgressEvent.RequestToken,
			NativeID:        identifier,
			StatusMessage:   aws.ToString(result.ProgressEvent.StatusMessage),
			ErrorCode:       resource.OperationErrorCode(result.ProgressEvent.ErrorCode),
		},
	}

	if result.ProgressEvent.OperationStatus == cctypes.OperationStatusSuccess {
		if err := c.populateResourceProperties(ctx, createResult.ProgressResult, identifier, request.ResourceType); enrichmentDeferred(err) {
			// The create itself already succeeded — only the read-back is
			// outstanding. Reporting InProgress hands it to the operator's
			// poll loop, where each poll is a heartbeat, instead of blocking
			// this RPC until the watchdog gives up on the operation. The
			// request token and native ID are already on the result, so the
			// poll resumes against the same request.
			createResult.ProgressResult.OperationStatus = resource.OperationStatusInProgress
		}
	}

	return createResult, nil
}

// UpdateResource updates a resource using CloudControl with full request handling
func (c *Client) UpdateResource(ctx context.Context, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	// Check if resource exists first
	_, err := c.api.GetResource(ctx, &cloudcontrol.GetResourceInput{
		Identifier: &request.NativeID,
		TypeName:   &request.ResourceType,
	})
	if err != nil {
		if pr, ok := classifyCloudControlError(err, resource.OperationUpdate); ok {
			return &resource.UpdateResult{ProgressResult: pr}, nil
		}
		return nil, err
	}

	// For resources where tags are maps, we do not support updates with patch documents
	if props.RequiresMapTags(request.ResourceType) && request.PatchDocument != nil {
		errMsg := "update operations for resources with map tags are not supported"
		return &resource.UpdateResult{
			ProgressResult: &resource.ProgressResult{
				Operation:       resource.OperationUpdate,
				OperationStatus: resource.OperationStatusFailure,
				StatusMessage:   errMsg,
				ErrorCode:       resource.OperationErrorCodeInternalFailure,
			},
		}, errors.New(errMsg)
	}

	patchDoc := request.PatchDocument

	// Filter out "add" operations with empty array/map values from the patch
	// document. These arise from the PKL schema rendering unset nullable
	// Listing/Mapping fields as []/{}. CloudControl may reject them (e.g.
	// "extraneous key" errors) or they may cause spurious updates.
	if patchDoc != nil {
		filtered, err := filterEmptyAddOps(*patchDoc)
		if err == nil && filtered != *patchDoc {
			patchDoc = &filtered
		}
	}

	if request.ResourceType == "AWS::SecretsManager::Secret" && patchDoc != nil {
		transformedPatch, err := transformSecretStringPatch([]byte(*patchDoc))
		if err != nil {
			return nil, fmt.Errorf("failed to transform SecretString patch: %w", err)
		}
		patchDoc = ptr.Of(string(transformedPatch))
	}

	result, err := c.api.UpdateResource(ctx, &cloudcontrol.UpdateResourceInput{
		Identifier:    &request.NativeID,
		PatchDocument: patchDoc,
		TypeName:      ptr.Of(request.ResourceType),
	})
	if err != nil {
		if pr, ok := classifyCloudControlError(err, resource.OperationUpdate); ok {
			return &resource.UpdateResult{ProgressResult: pr}, nil
		}
		return nil, err
	}

	identifier := request.NativeID
	if result.ProgressEvent.Identifier != nil {
		identifier = normalizeCompositeIdentifier(*result.ProgressEvent.Identifier)
	}

	updateResult := &resource.UpdateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:       resource.OperationUpdate,
			OperationStatus: status.FromOperationStatus(result.ProgressEvent.OperationStatus),
			RequestID:       *result.ProgressEvent.RequestToken,
			NativeID:        identifier,
			StatusMessage:   aws.ToString(result.ProgressEvent.StatusMessage),
			ErrorCode:       resource.OperationErrorCode(result.ProgressEvent.ErrorCode),
		},
	}

	if result.ProgressEvent.OperationStatus == cctypes.OperationStatusSuccess {
		if err := c.populateResourceProperties(ctx, updateResult.ProgressResult, identifier, request.ResourceType); enrichmentDeferred(err) {
			// As in CreateResource: the update landed, so defer the read-back
			// to the poll loop rather than blocking the watched RPC on it.
			updateResult.ProgressResult.OperationStatus = resource.OperationStatusInProgress
		}
	}

	return updateResult, nil
}

// DeleteResource deletes a resource using CloudControl with full request handling
func (c *Client) DeleteResource(ctx context.Context, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	result, err := c.api.DeleteResource(ctx, &cloudcontrol.DeleteResourceInput{
		Identifier: &request.NativeID,
		TypeName:   ptr.Of(request.ResourceType),
	})
	if err != nil {
		if pr, ok := classifyCloudControlError(err, resource.OperationDelete); ok {
			return &resource.DeleteResult{ProgressResult: pr}, nil
		}
		return nil, err
	}

	// If the resource is not found, we return a success status
	if result.ProgressEvent.ErrorCode == cctypes.HandlerErrorCodeNotFound {
		return &resource.DeleteResult{
			ProgressResult: &resource.ProgressResult{
				Operation:       resource.OperationDelete,
				OperationStatus: status.FromOperationStatus(cctypes.OperationStatusSuccess),
				RequestID:       *result.ProgressEvent.RequestToken,
				StatusMessage:   aws.ToString(result.ProgressEvent.StatusMessage),
				ErrorCode:       resource.OperationErrorCode(result.ProgressEvent.ErrorCode),
			},
		}, nil
	}

	return &resource.DeleteResult{
		ProgressResult: &resource.ProgressResult{
			Operation:       resource.OperationDelete,
			OperationStatus: status.FromOperationStatus(result.ProgressEvent.OperationStatus),
			RequestID:       *result.ProgressEvent.RequestToken,
			StatusMessage:   aws.ToString(result.ProgressEvent.StatusMessage),
			ErrorCode:       resource.OperationErrorCode(result.ProgressEvent.ErrorCode),
		},
	}, nil
}

// ReadResource reads a resource using CloudControl with full request handling
func (c *Client) ReadResource(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	result, err := c.api.GetResource(ctx, &cloudcontrol.GetResourceInput{
		Identifier: &request.NativeID,
		TypeName:   ptr.Of(request.ResourceType),
	})
	if err != nil {
		errorCode, isCloudControlError := helper.HandleCloudControlError(err)
		if isCloudControlError {
			return &resource.ReadResult{
				ResourceType: request.ResourceType,
				ErrorCode:    resource.OperationErrorCode(errorCode),
			}, nil
		}
		return nil, err
	}

	properties := *result.ResourceDescription.Properties
	var propsMap map[string]any
	if err = json.Unmarshal([]byte(properties), &propsMap); err != nil {
		return nil, fmt.Errorf("failed to unmarshal resource properties: %w", err)
	}

	if props.RequiresMapTags(request.ResourceType) {
		if err = props.TransformTagsToArray(propsMap); err != nil {
			return nil, fmt.Errorf("failed to transform tags: %w", err)
		}
	}

	if err = stripIgnoredFields(propsMap, IgnoredFields[request.ResourceType]); err != nil {
		return nil, fmt.Errorf("failed to strip ignored fields: %w", err)
	}

	// CloudControl injects DestinationConfig:{OnFailure:{},OnSuccess:{}} into
	// every AWS::Lambda::EventInvokeConfig read, even when the caller never set
	// it. AWS requires Destination inside OnFailure/OnSuccess, so an empty {}
	// carries no information; absorbing it makes formae's required-field
	// validation spuriously reject a valid resource. Strip the empty
	// sub-objects (and DestinationConfig if it ends up empty); genuine
	// user-set destinations are non-empty and pass through untouched.
	if request.ResourceType == "AWS::Lambda::EventInvokeConfig" {
		stripEmptyCollectionsFromMap(propsMap)
	}

	transformedProps, err := json.Marshal(propsMap)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal transformed properties: %w", err)
	}

	properties = string(transformedProps)

	return &resource.ReadResult{
		ResourceType: *result.TypeName,
		Properties:   properties,
	}, nil
}

// enrichmentWindow bounds how long StatusResource may keep reporting a finished
// mutation as InProgress purely because the post-success read-back hasn't
// returned properties yet. The operator polls roughly every 20s, so two minutes
// is about six further attempts — comfortably more than the eventual-consistency
// and throttle-recovery delays that make a just-mutated resource briefly
// unreadable (AWS typically recovers from a throttle in tens of seconds), while
// still guaranteeing the operation ends.
//
// When the window elapses the poll commits a Success carrying the native ID.
// The inventory row is written only on the success path, so ending the wait as a
// failure would throw away formae's only record of a resource that really
// exists, and the next apply would create a second one.
const enrichmentWindow = 2 * time.Minute

// enrichmentClockSkew is how far a progress event's EventTime may sit in this
// host's future before it stops being believable. EventTime is stamped by the
// AWS control plane and compared against the plugin host's clock, so a little
// disagreement is ordinary and must not cost an operation its window; a lot of
// it means the timestamp cannot bound anything and the poll commits immediately
// instead. Thirty seconds is a quarter of the window: wide enough for ordinary
// clock drift, narrow enough that a skewed clock cannot materially extend the
// wait.
const enrichmentClockSkew = 30 * time.Second

// statusOutageWindow bounds how long StatusResource may keep answering "ask me
// again" to polls that could not observe the request's status at all — the
// status call failed recoverably, or came back without a progress event. Unlike
// the enrichment window there is no event to measure against on this path: an
// unobserved poll carries no timestamp, so the window is measured from the
// first poll of the outage.
//
// It is deliberately the same length as the enrichment window and derived the
// same way: the operator polls roughly every 20s, so two minutes is about six
// further attempts — more than AWS typically needs to recover from a throttle,
// while still guaranteeing the operation ends.
//
// What it bounds, precisely: a run of *consecutive* unobserved polls, seen by
// one plugin process. Any successful status call clears the stamp, so a request
// that is being observed keeps polling for as long as it needs; and a restarted
// plugin process starts the window again from its first failed poll. It is not
// a bound on the operation's total running time.
const statusOutageWindow = 2 * time.Minute

// StatusResource gets the status of a resource request using CloudControl with full request handling
func (c *Client) StatusResource(ctx context.Context, request *resource.StatusRequest, readFunc func(context.Context, *resource.ReadRequest) (*resource.ReadResult, error)) (*resource.StatusResult, error) {
	// This is the RPC the agent's missing-in-action watchdog observes most
	// often, and the SDK retryer behind the status call can back off for longer
	// than the watchdog is willing to wait. One deadline, derived here, bounds
	// every AWS call this RPC goes on to make — the status call and the
	// post-success read-back share it, so the RPC as a whole is bounded by one
	// budget rather than one per call.
	deadline := c.watchedRPCDeadline()

	result, err := retryCallable(ctx, retryOpts{Deadline: deadline}, "StatusResource:"+request.RequestID,
		func(ctx context.Context) (*cloudcontrol.GetResourceRequestStatusOutput, error) {
			return c.api.GetResourceRequestStatus(ctx, &cloudcontrol.GetResourceRequestStatusInput{
				RequestToken: &request.RequestID,
			})
		})
	if err == nil && (result == nil || result.ProgressEvent == nil) {
		err = fmt.Errorf("%w for request %s", errNoProgressEvent, request.RequestID)
	}
	if err != nil {
		// The poll learned nothing about the request. Reporting that as a bare
		// Go error ends the operation with no explanation of what went wrong,
		// so classify what can be classified first.
		statusResult, classified := c.classifyUnobservedStatus(ctx, request, err)
		if !classified {
			return nil, err
		}
		c.forgetIfResolved(request.RequestID, statusResult.ProgressResult.OperationStatus)
		return statusResult, nil
	}

	// The request was observed, so any run of consecutive status-call failures
	// for it has ended.
	c.clearStatusError(request.RequestID)

	operation, operationStatus := status.FromProgress(result.ProgressEvent)

	// CloudControl may omit the identifier from a progress event; the native ID
	// the caller polled with names the same resource. Success is the path that
	// writes the inventory row, so an empty identifier would persist a row
	// nothing can be read back against — and would aim the read-back below at
	// an identifier no read could satisfy.
	identifier := request.NativeID
	if result.ProgressEvent.Identifier != nil {
		identifier = normalizeCompositeIdentifier(*result.ProgressEvent.Identifier)
	}

	// NOT_STABILIZED means the resource is still being provisioned — CloudControl's
	// internal stabilization window expired but the operation is still in progress.
	// Treat as InProgress so the PluginOperator keeps polling rather than consuming
	// a retry attempt by re-invoking the entire CRUD operation.
	if result.ProgressEvent.ErrorCode == cctypes.HandlerErrorCodeNotStabilized {
		operationStatus = resource.OperationStatusInProgress
	}

	// NOT_FOUND during a Create operation means the resource hasn't propagated yet
	// in AWS's control plane. Treat as InProgress to keep polling rather than
	// retrying the create (which could cause AlreadyExists errors).
	if result.ProgressEvent.Operation == cctypes.OperationCreate && result.ProgressEvent.ErrorCode == cctypes.HandlerErrorCodeNotFound {
		operationStatus = resource.OperationStatusInProgress
	}

	// TG not associated with LB during ECS Service create. The Listener that
	// wires the TG↔LB association may still be in flight in the same apply;
	// keep polling so the next CloudControl retry succeeds once Listener.create
	// completes. Pairing the error code with a status-message substring means a
	// wording change alone produces a missed remap (graceful) and a code change
	// fails loudly (caught by the dedicated unit test).
	if result.ProgressEvent.Operation == cctypes.OperationCreate &&
		result.ProgressEvent.ErrorCode == cctypes.HandlerErrorCodeInvalidRequest &&
		tgNotAssociatedPattern.MatchString(aws.ToString(result.ProgressEvent.StatusMessage)) {
		operationStatus = resource.OperationStatusInProgress
	}

	// If the resource is not found, we return a success status when it is a delete operation
	if result.ProgressEvent.Operation == cctypes.OperationDelete && result.ProgressEvent.ErrorCode == cctypes.HandlerErrorCodeNotFound {
		c.forgetRequest(request.RequestID)
		return &resource.StatusResult{
			ProgressResult: &resource.ProgressResult{
				Operation:       operation,
				OperationStatus: resource.OperationStatusSuccess,
				RequestID:       request.RequestID,
				NativeID:        identifier,
				StatusMessage:   aws.ToString(result.ProgressEvent.StatusMessage),
				ErrorCode:       resource.OperationErrorCode(result.ProgressEvent.ErrorCode)},
		}, nil
	}

	// All remap rules above have had their chance. If we still have a Failure at
	// this point it is reaching the caller as-is and will be surfaced as a real
	// failure / retry trigger. Dump the full ProgressEvent so future investigations
	// (notably the ECS Service Delete drain timeout) have the actual ErrorCode and
	// StatusMessage from prod runs without needing to reproduce them.
	if operationStatus == resource.OperationStatusFailure {
		plugin.LoggerFromContext(ctx).Warn("StatusResource: CCAPI ProgressEvent reports Failure",
			"operation", string(result.ProgressEvent.Operation),
			"errorCode", string(result.ProgressEvent.ErrorCode),
			"statusMessage", aws.ToString(result.ProgressEvent.StatusMessage),
			"typeName", aws.ToString(result.ProgressEvent.TypeName),
			"requestToken", aws.ToString(result.ProgressEvent.RequestToken),
			"identifier", identifier)
	}

	statusResult := &resource.StatusResult{
		ProgressResult: &resource.ProgressResult{
			Operation:       operation,
			OperationStatus: operationStatus,
			RequestID:       request.RequestID,
			NativeID:        identifier,
			StatusMessage:   aws.ToString(result.ProgressEvent.StatusMessage),
			ErrorCode:       resource.OperationErrorCode(result.ProgressEvent.ErrorCode),
		},
	}

	if operationStatus == resource.OperationStatusSuccess && result.ProgressEvent.Operation != cctypes.OperationDelete {
		c.enrichStatusResult(ctx, statusResult.ProgressResult, request, result.ProgressEvent, identifier, deadline, readFunc)
	}

	c.forgetIfResolved(request.RequestID, statusResult.ProgressResult.OperationStatus)

	return statusResult, nil
}

// forgetIfResolved drops a request's tracker entry once a poll has resolved it.
// A resolved request has nothing left for either stamp to bound. A poll still
// waiting — for its read-back, or for a status call that can be observed at all
// — has not resolved, and keeps its stamps so the next poll measures against
// the same windows.
func (c *Client) forgetIfResolved(requestID string, operationStatus resource.OperationStatus) {
	switch operationStatus {
	case resource.OperationStatusSuccess, resource.OperationStatusFailure:
		c.forgetRequest(requestID)
	}
}

// errNoProgressEvent marks a status response that came back without a progress
// event. Like a status call that failed outright it leaves the request's
// outcome unobserved, so it is classified the same way.
var errNoProgressEvent = errors.New("status poll returned no progress event")

// classifyUnobservedStatus turns a poll that could not observe its request into
// a progress result the operator can act on, reporting false when it cannot —
// in which case the caller keeps today's behaviour and returns the raw error.
//
// Nothing here can say whether the mutation applied. The two honest answers are
// "ask again" and "this ended and nobody knows how", and the whole point of the
// classification is that the operator answers each of those very differently:
//
//   - InProgress reschedules a poll against the same request token, and each
//     poll is a heartbeat for the agent's missing-in-action watchdog. The
//     original CRUD is never re-invoked.
//   - A Failure carrying a recoverable code — the obvious alternative — would
//     instead make the operator re-invoke the CRUD from the original request,
//     re-running a create that may already have succeeded.
//
// So a status outage that may clear becomes InProgress, and only an outcome
// that will never be observed becomes a Failure, with a code the operator will
// not retry.
func (c *Client) classifyUnobservedStatus(ctx context.Context, request *resource.StatusRequest, cause error) (*resource.StatusResult, bool) {
	log := plugin.LoggerFromContext(ctx)

	// An unknown request token needs its own answer, because neither default is
	// right: the shared error helper maps it to NotFound, which the operator
	// treats as recoverable and would re-invoke the CRUD on, while this layer's
	// own predicate calls it non-recoverable and would never reach the branch
	// below. Checked before the budget sentinel because a status call that
	// crossed its deadline returns this exception wrapped in it, and re-polling
	// a token CloudControl does not know only defers the same answer.
	//
	// A direct Read cannot settle it either: a Read proves the resource exists,
	// not that this request applied — for an update it says nothing about
	// whether the patch landed, for a create it cannot rule out a pre-existing
	// or concurrent resource, and for a delete it inverts the answer. Once the
	// progress event is gone the plugin cannot even tell which verb it is
	// observing. Indeterminate is the truthful answer, so it is the one given.
	var tokenNotFound *cctypes.RequestTokenNotFoundException
	if errors.As(cause, &tokenNotFound) {
		log.Error("StatusResource: CloudControl does not recognise the request token; the outcome cannot be determined",
			"error", cause,
			"identifier", request.NativeID,
			"requestID", request.RequestID)
		return unobservedResult(request, resource.OperationStatusFailure,
			resource.OperationErrorCodeUnforeseenError,
			fmt.Sprintf("CloudControl does not recognise the request token for %s, so whether the operation was applied cannot be determined",
				request.NativeID)), true
	}

	// Anything else that is not a spent budget is a real answer about the call
	// itself (bad request, denied, cancelled); polling cannot improve on it.
	if !errors.Is(cause, errRetryBudgetExhausted) && !errors.Is(cause, errNoProgressEvent) {
		return nil, false
	}

	first, ok := c.stampStatusError(ctx, request.RequestID)
	if !ok {
		// No request token to key the outage window on, or the tracker is at
		// its admission cap. Converting without a bound would poll forever, so
		// this keeps today's behaviour and surfaces the error.
		return nil, false
	}

	// The window bounds a run of consecutive unobserved polls for this process:
	// any successful status call clears the stamp, and a restart starts it
	// again. Past it the operation ends — unobserved, which is not a safe
	// outcome, only the honest one: the mutation may or may not have happened,
	// and no protocol status means "I could not see it".
	if elapsed := c.clock().Sub(first); elapsed >= statusOutageWindow {
		log.Error("StatusResource: status could not be observed within the outage window, ending the operation",
			"error", cause,
			"identifier", request.NativeID,
			"requestID", request.RequestID,
			"elapsed", elapsed)
		return unobservedResult(request, resource.OperationStatusFailure,
			resource.OperationErrorCodeUnforeseenError,
			fmt.Sprintf("the status of %s could not be read for %s, so whether the operation was applied cannot be determined",
				diagnosticIdentifier(request), elapsed)), true
	}

	log.Info("StatusResource: status could not be read, deferring to the next poll",
		"error", cause,
		"identifier", request.NativeID,
		"requestID", request.RequestID)
	return unobservedResult(request, resource.OperationStatusInProgress,
		resource.OperationErrorCodeNotSet,
		fmt.Sprintf("the status of %s could not be read; retrying on the next poll", diagnosticIdentifier(request))), true
}

// diagnosticIdentifier names the resource an unobserved-outcome StatusMessage
// is about. NativeID is what an operator recognises, but CloudControl may not
// have echoed one back yet; falling back to the RequestID keeps the message
// naming something rather than interpolating an empty string.
func diagnosticIdentifier(request *resource.StatusRequest) string {
	if request.NativeID != "" {
		return request.NativeID
	}
	return request.RequestID
}

// unobservedResult builds the progress result for a poll that observed nothing,
// echoing back the request token so the operator can poll the same request
// again and the native ID so its diagnostics name a resource.
//
// The operation is reported as the status check, not as a CRUD verb: a status
// call carries no verb of its own, and with no progress event to read one from
// there is nothing to report but what the plugin was actually doing. The
// operator branches on the status and the error code, so this field is
// reporting only.
func unobservedResult(
	request *resource.StatusRequest,
	operationStatus resource.OperationStatus,
	errorCode resource.OperationErrorCode,
	statusMessage string,
) *resource.StatusResult {
	return &resource.StatusResult{
		ProgressResult: &resource.ProgressResult{
			Operation:       resource.OperationCheckStatus,
			OperationStatus: operationStatus,
			RequestID:       request.RequestID,
			NativeID:        request.NativeID,
			StatusMessage:   statusMessage,
			ErrorCode:       errorCode,
		},
	}
}

// enrichStatusResult runs the post-success Read that populates the latest
// properties on a finished mutation and folds the outcome into pr. Some
// resources (like DynamoDB tables) are not immediately readable after
// CloudControl reports the operation as successful, and AWS throttling can flap
// during high-concurrency periods; retryRead absorbs both with exponential
// backoff so the agent doesn't persist a stale snapshot.
//
// The read shares the RPC's deadline with the status call that preceded it, so it
// can stop before it has anything to report — and stops sooner the more of the
// budget that status call spent. The mutation itself has already succeeded by
// then, and the only remaining question is whether to spend another poll on the
// read-back or to commit the row without it — enrichmentDecision answers that,
// and bounds how long the answer can stay "wait".
func (c *Client) enrichStatusResult(
	ctx context.Context,
	pr *resource.ProgressResult,
	request *resource.StatusRequest,
	event *cctypes.ProgressEvent,
	identifier string,
	deadline time.Time,
	readFunc func(context.Context, *resource.ReadRequest) (*resource.ReadResult, error),
) {
	log := plugin.LoggerFromContext(ctx)

	typeName := aws.ToString(event.TypeName)
	if typeName == "" {
		// Nothing to read back against; commit what the event already carries.
		log.Error("StatusResource: progress event carries no resource type, skipping read",
			"identifier", identifier)
		return
	}

	readResult, readErr := retryRead(ctx, retryOpts{Deadline: deadline}, "StatusResource:"+typeName,
		func(ctx context.Context) (*resource.ReadResult, error) {
			return readFunc(ctx, &resource.ReadRequest{
				NativeID:     identifier,
				ResourceType: typeName,
				TargetConfig: request.TargetConfig,
			})
		})

	switch {
	case readErr != nil:
		log.Error("StatusResource: Read failed after retry budget exhausted",
			"error", readErr,
			"identifier", identifier,
			"resourceType", typeName)
	case readResult != nil && readResult.ErrorCode != "":
		log.Error("StatusResource: Read returned CloudControl error after retry budget",
			"errorCode", readResult.ErrorCode,
			"identifier", identifier,
			"resourceType", typeName)
	case readResult != nil && readResult.Properties != "":
		pr.ResourceProperties = json.RawMessage(readResult.Properties)
	}

	if pr.ResourceProperties != nil {
		return
	}

	if enrichmentDeferred(readErr) {
		// Whichever branch this takes, no ErrorCode is set: the underlying event
		// is a Success event and the resource exists. What is lost is the
		// read-back, so the row is committed with whatever the event carried.
		switch verdict, elapsed := c.enrichmentDecision(ctx, request.RequestID, event.EventTime); verdict {
		case enrichmentPendingRetry:
			log.Info("StatusResource: read-back still pending, deferring to the next poll",
				"identifier", identifier,
				"resourceType", typeName,
				"elapsed", elapsed)
			pr.OperationStatus = resource.OperationStatusInProgress
			pr.StatusMessage = fmt.Sprintf(
				"the mutation succeeded; enrichment pending, %s is not readable yet", identifier)

		case enrichmentWindowElapsed:
			log.Error("StatusResource: enrichment window elapsed, reporting success without properties",
				"identifier", identifier,
				"resourceType", typeName,
				"elapsed", elapsed,
				"eventTime", aws.ToTime(event.EventTime),
				"requestID", request.RequestID)
			pr.StatusMessage = fmt.Sprintf(
				"the mutation succeeded but %s could not be read back within the enrichment window; the recorded properties may be incomplete",
				identifier)

		case enrichmentUnbounded:
			log.Error("StatusResource: no bounded retry window could be established for the read-back, reporting success without properties",
				"identifier", identifier,
				"resourceType", typeName,
				"elapsed", elapsed,
				"eventTime", aws.ToTime(event.EventTime),
				"requestID", request.RequestID)
			pr.StatusMessage = fmt.Sprintf(
				"the mutation succeeded but %s could not be read back, and no bounded retry window could be established; the recorded properties may be incomplete",
				identifier)
		}
		return
	}

	// Polling cannot fix whatever else stopped the read, so there is nothing to
	// wait for: report the mutation's own outcome with what is available.
	log.Error("StatusResource: Failed to read properties after retries",
		"identifier", identifier,
		"resourceType", typeName)
}

// enrichmentVerdict says what a poll whose read-back ran out of budget should do
// about it.
type enrichmentVerdict int

const (
	// enrichmentPendingRetry: the mutation completed recently enough that the
	// read-back is worth another poll.
	enrichmentPendingRetry enrichmentVerdict = iota
	// enrichmentWindowElapsed: the read-back has had its window and did not
	// come good; commit what we have.
	enrichmentWindowElapsed
	// enrichmentUnbounded: no bounded retry window could be established for
	// the read-back -- either EventTime itself can't be trusted (missing or
	// wildly skewed), or a trustworthy EventTime exists but the backstop
	// stamp that would guard it against creeping forward isn't available
	// (empty RequestID, or the tracker at its admission cap). Either way,
	// don't start a wait that cannot be bounded; commit what we have.
	enrichmentUnbounded
)

// enrichmentDecision decides whether to spend another poll on a read-back that
// ran out of budget, and reports how long the mutation has been complete for the
// caller's diagnostics.
//
// The event's own timestamp is the primary clock because it is stateless: an
// operator restart, a plugin-process restart, or a poll served by a different
// process all reach the same verdict from the same event. The process-local
// stamp only backs it up, for a provider timestamp that creeps forward on every
// poll and would otherwise keep the window perpetually young. Whichever fires
// first ends the wait, so the backstop can only shorten the window, never
// extend it.
//
// Every branch that cannot be bounded ends the wait rather than starting one:
// with no usable timestamp, or no stamp to fall back on, committing a sparse row
// now beats polling forever. That trade is only the right way round because the
// fallback is a Success — the operation's outcome is not in doubt, only its
// properties are.
func (c *Client) enrichmentDecision(ctx context.Context, requestID string, eventTime *time.Time) (enrichmentVerdict, time.Duration) {
	if eventTime == nil {
		return enrichmentUnbounded, 0
	}

	now := c.clock()
	elapsed := now.Sub(*eventTime)

	// Far enough into this host's future that the timestamp is not measuring
	// anything. Reported unclamped, since how far out it is is the diagnostic.
	if elapsed < -enrichmentClockSkew {
		return enrichmentUnbounded, elapsed
	}
	// Within the skew allowance: ordinary disagreement between the control
	// plane's clock and this host's, so read it as a just-completed event.
	if elapsed < 0 {
		elapsed = 0
	}

	// Past the window, including an implausibly old event.
	if elapsed >= enrichmentWindow {
		return enrichmentWindowElapsed, elapsed
	}

	first, ok := c.stampEnrichmentPending(ctx, requestID)
	if !ok {
		// No request token to key a stamp on, or the tracker is at its
		// admission cap: without a backstop there is nothing to shorten a
		// creeping EventTime with, so don't start a wait at all.
		return enrichmentUnbounded, elapsed
	}
	if now.Sub(first) >= enrichmentWindow {
		return enrichmentWindowElapsed, elapsed
	}
	return enrichmentPendingRetry, elapsed
}

// populateResourceProperties performs a post-success Read to populate
// ResourceProperties on a ProgressResult. Used when CloudControl returns
// synchronous Success (no async polling, so StatusResource's Read loop
// doesn't run). Wraps the call in retryRead so transient throttling
// surfaced as ErrorCode doesn't leave the agent with a stale snapshot, under
// the watched-call budget so the retries can't outlast the RPC's watchdog
// window.
//
// It returns whatever stopped the retry loop, or nil. An error wrapping
// errRetryBudgetExhausted means the budget — not a terminal outcome — is why
// enrichment didn't finish, so the caller can hand the remaining work to the
// operator's poll loop. A nil return does not by itself mean properties were
// populated: a non-recoverable CloudControl error code is logged and reported
// as nil, keeping the caller's existing behaviour of returning Success with
// whatever is available, since polling cannot fix it.
func (c *Client) populateResourceProperties(ctx context.Context, pr *resource.ProgressResult, identifier, resourceType string) error {
	readResult, err := retryRead(ctx, retryOpts{Budget: c.watchedCallBudget()}, "populateResourceProperties:"+resourceType,
		func(ctx context.Context) (*resource.ReadResult, error) {
			return c.ReadResource(ctx, &resource.ReadRequest{
				NativeID:     identifier,
				ResourceType: resourceType,
			})
		})
	if err != nil {
		plugin.LoggerFromContext(ctx).Error("post-success Read failed after retries",
			"error", err,
			"identifier", identifier,
			"resourceType", resourceType)
		return err
	}
	if readResult != nil && readResult.ErrorCode != "" {
		plugin.LoggerFromContext(ctx).Error("post-success Read returned error after retries",
			"errorCode", readResult.ErrorCode,
			"identifier", identifier,
			"resourceType", resourceType)
		return nil
	}
	if readResult != nil && readResult.Properties != "" {
		pr.ResourceProperties = json.RawMessage(readResult.Properties)
	}
	return nil
}

// enrichmentDeferred reports whether a post-success enrichment Read stopped
// because it ran out of its wall-clock budget, which is the one outcome the
// mutation paths convert on.
//
// The sentinel is authoritative here rather than the returned result: on a
// budget exit retryRead may have nothing to report. It can also carry a
// terminal cause, when the attempt that crossed the deadline happened to fail
// non-recoverably — that resolves itself, because the next poll starts a fresh
// budget and surfaces the real error immediately.
func enrichmentDeferred(err error) bool {
	return errors.Is(err, errRetryBudgetExhausted)
}

// ListResources lists resources using CloudControl. Wrapped in
// retryCallable because Discovery has no PluginOperator layer to absorb
// transient throttling / handler-failure errors; a single 5xx in a scan
// loop drops the resource type for the tick and the conformance test
// wait window typically expires before the next periodic scan.
func (c *Client) ListResources(ctx context.Context, input *cloudcontrol.ListResourcesInput) (*cloudcontrol.ListResourcesOutput, error) {
	typeName := ""
	if input != nil && input.TypeName != nil {
		typeName = *input.TypeName
	}
	return retryCallable(ctx, retryOpts{}, "ListResources:"+typeName,
		func(ctx context.Context) (*cloudcontrol.ListResourcesOutput, error) {
			return c.api.ListResources(ctx, input)
		})
}

// transformSecretStringPatch transforms replace operations to add operations for SecretString
// AWS CloudControl requires writeOnlyProperties like SecretString to use 'add' operation
func transformSecretStringPatch(patchDoc []byte) ([]byte, error) {
	if len(patchDoc) == 0 {
		return patchDoc, nil
	}

	var patches []map[string]any
	if err := json.Unmarshal(patchDoc, &patches); err != nil {
		return patchDoc, err
	}

	modified := false
	for i, patch := range patches {
		if op, ok := patch["op"].(string); ok && op == "replace" {
			if path, ok := patch["path"].(string); ok && path == "/SecretString" {
				patches[i]["op"] = "add"
				modified = true
			}
		}
	}

	if !modified {
		return patchDoc, nil
	}

	return json.Marshal(patches)
}

func stripIgnoredFields(data map[string]any, fields []string) error {
	for _, field := range fields {
		if strings.HasPrefix(field, "$") {
			field = strings.TrimPrefix(field, "$")
			field = strings.TrimPrefix(field, ".")
		}

		components := strings.Split(field, ".")
		if len(components) == 0 {
			continue
		}

		parent, keyToRemove, err := findParentAndKey(data, components)
		if err != nil {
			return err
		}

		if parentMap, ok := parent.(map[string]any); ok {
			delete(parentMap, keyToRemove)
		}
	}
	return nil
}

func findParentAndKey(data map[string]any, components []string) (any, string, error) {
	current := data

	for _, key := range components[:len(components)-1] {
		if next, found := current[key]; found {
			if m, ok := next.(map[string]any); ok {
				current = m
				continue
			}
		}
		return nil, "", fmt.Errorf("path not found: '%s'", key)
	}

	keyToRemove := components[len(components)-1]
	return current, keyToRemove, nil
}

// filterEmptyAddOps removes "add" operations from a JSON Patch document where
// the value is an empty array or empty map, and strips empty collections from
// nested values inside "replace" operations. These are phantom values from the
// PKL schema rendering unset nullable fields as []/{}. CloudControl rejects
// them as "extraneous key" errors or they trigger unnecessary replacements.
func filterEmptyAddOps(patchDoc string) (string, error) {
	var ops []map[string]any
	if err := json.Unmarshal([]byte(patchDoc), &ops); err != nil {
		return patchDoc, err
	}

	filtered := make([]map[string]any, 0, len(ops))
	for _, op := range ops {
		// For replace/add operations with map or array values, recursively
		// strip empty collections from the value
		if val, ok := op["value"].(map[string]any); ok {
			stripEmptyCollectionsFromMap(val)
		}
		// Skip add/replace operations whose value is an empty collection.
		// These arise from provider-default nested objects (e.g.
		// DestinationConfig with empty OnSuccess/OnFailure) that become
		// empty after stripping, or were empty from the start.
		if op["op"] == "add" || op["op"] == "replace" {
			switch val := op["value"].(type) {
			case []any:
				if len(val) == 0 {
					continue
				}
			case map[string]any:
				if len(val) == 0 {
					continue
				}
			}
		}
		filtered = append(filtered, op)
	}

	result, err := json.Marshal(filtered)
	if err != nil {
		return patchDoc, err
	}
	return string(result), nil
}

// stripEmptyCollections recursively removes properties with empty array or map
// values from a JSON object. These arise from the PKL schema rendering nullable
// Listing/Mapping fields as [] and {} when the user didn't set them.
func stripEmptyCollections(data json.RawMessage) (json.RawMessage, error) {
	var props map[string]any
	if err := json.Unmarshal(data, &props); err != nil {
		return data, nil
	}

	stripEmptyCollectionsFromMap(props)

	return json.Marshal(props)
}

// stripEmptyCollectionsFromMap recursively removes empty arrays and maps from
// a map structure. Also recurses into non-empty maps and array elements.
func stripEmptyCollectionsFromMap(m map[string]any) {
	for k, v := range m {
		switch val := v.(type) {
		case []any:
			if len(val) == 0 {
				delete(m, k)
			} else {
				for _, elem := range val {
					if elemMap, ok := elem.(map[string]any); ok {
						stripEmptyCollectionsFromMap(elemMap)
					}
				}
			}
		case map[string]any:
			if len(val) == 0 {
				delete(m, k)
			} else {
				stripEmptyCollectionsFromMap(val)
				// Re-check after recursive stripping — the map may now be empty
				if len(val) == 0 {
					delete(m, k)
				}
			}
		}
	}
}
