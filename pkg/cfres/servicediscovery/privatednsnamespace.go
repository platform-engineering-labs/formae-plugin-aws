// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package servicediscovery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	servicediscoverysdk "github.com/aws/aws-sdk-go-v2/service/servicediscovery"
	servicediscoverytypes "github.com/aws/aws-sdk-go-v2/service/servicediscovery/types"

	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres/prov"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres/registry"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres/utils"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/config"
	"github.com/platform-engineering-labs/formae/pkg/plugin"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
)

const resourceType = "AWS::ServiceDiscovery::PrivateDnsNamespace"

// namespaceOperationTimeout bounds how long the engine keeps polling a Cloud Map
// namespace operation before the provisioner reports it as failed. Creating a
// namespace also creates a Route 53 private hosted zone, which completes in well
// under a minute in practice.
const namespaceOperationTimeout = 15 * time.Minute

// namespaceDeleteTimeout bounds a namespace delete as a whole — the operation
// behind it and any re-issues while the namespace still holds resources. It
// travels in the RequestID from the first attempt onwards and is never renewed,
// so contention that keeps recurring runs the wait out rather than retrying
// forever. ECS deregisters a service's instances only once the ECS service
// itself is gone, so a destroy reaches the namespace while deregistration is
// still settling.
const namespaceDeleteTimeout = 15 * time.Minute

// CreatePrivateDnsNamespace returns only an operation id, and the namespace id
// shows up on that operation's NAMESPACE target shortly afterwards — while the
// operation is still PENDING. Create polls for it because the SDK requires every
// create to return a NativeID.
const (
	namespaceTargetPollInterval = 500 * time.Millisecond
	namespaceTargetPollAttempts = 20
)

// namespaceTargetKey is the key under which a namespace operation reports the id
// of the namespace it acts on.
const namespaceTargetKey = string(servicediscoverytypes.OperationTargetTypeNamespace)

// resourceOwnerSelf restricts a namespace lookup to namespaces this account
// created, excluding namespaces other accounts have shared with it.
const resourceOwnerSelf = "SELF"

// resourceInUseErrorCode is what a namespace operation reports when the delete
// it carried was blocked by the resources still registered in the namespace.
const resourceInUseErrorCode = "RESOURCE_IN_USE"

// creatorRequestIDPrefix identifies the CreatorRequestId values this plugin
// derives. Cloud Map caps CreatorRequestId at 64 characters, which the prefix
// plus the truncated digest stays within.
const creatorRequestIDPrefix = "formae-"

// PrivateDnsNamespace is the AWS::ServiceDiscovery::PrivateDnsNamespace
// provisioner. The type is NON_PROVISIONABLE in the CFN registry — CloudControl
// has no handlers for it at all — so every operation goes through the Cloud Map
// SDK directly.
//
// Namespace create, update and delete are all asynchronous: each returns an
// operation id and completes later. The operation id and the poll deadline
// travel in the RequestID, and Status polls the operation until it settles.
type PrivateDnsNamespace struct {
	cfg           *config.Config
	clientFactory func(cfg *config.Config) (serviceDiscoveryClientInterface, error)

	// namespaceTargetPollInterval is the gap between GetOperation polls during
	// the Create-time wait for the namespace id. Zero means use the default.
	namespaceTargetPollInterval time.Duration
	// namespaceTargetPollAttempts is the upper bound on the number of polls.
	// Zero means use the default.
	namespaceTargetPollAttempts int

	now func() time.Time
}

var _ prov.Provisioner = &PrivateDnsNamespace{}

func init() {
	registry.Register(resourceType,
		[]resource.Operation{
			resource.OperationCreate,
			resource.OperationRead,
			resource.OperationUpdate,
			resource.OperationDelete,
			resource.OperationCheckStatus,
			resource.OperationList,
		},
		func(cfg *config.Config) prov.Provisioner {
			return &PrivateDnsNamespace{
				cfg:           cfg,
				clientFactory: defaultServiceDiscoveryClientFactory,
				now:           func() time.Time { return time.Now().UTC() },
			}
		})
}

func defaultServiceDiscoveryClientFactory(cfg *config.Config) (serviceDiscoveryClientInterface, error) {
	awsCfg, err := cfg.ToAwsConfig(context.Background())
	if err != nil {
		return nil, fmt.Errorf("servicediscovery: build AWS config: %w", err)
	}
	return servicediscoverysdk.NewFromConfig(awsCfg), nil
}

// ----- Create -----

func (n *PrivateDnsNamespace) Create(ctx context.Context, request *resource.CreateRequest) (*resource.CreateResult, error) {
	client, err := n.clientFactory(n.cfg)
	if err != nil {
		return nil, err
	}

	var properties map[string]any
	if err := json.Unmarshal(request.Properties, &properties); err != nil {
		return nil, fmt.Errorf("servicediscovery: parse properties: %w", err)
	}
	name, err := utils.GetStringProperty(properties, "Name")
	if err != nil {
		return nil, fmt.Errorf("servicediscovery: invalid Name: %w", err)
	}
	vpc, err := utils.GetStringProperty(properties, "Vpc")
	if err != nil {
		return nil, fmt.Errorf("servicediscovery: invalid Vpc: %w", err)
	}

	input := &servicediscoverysdk.CreatePrivateDnsNamespaceInput{
		Name:             aws.String(name),
		Vpc:              aws.String(vpc),
		CreatorRequestId: aws.String(creatorRequestID(request.ResourceType, request.Label, name, vpc)),
	}
	if _, declared := properties["Description"]; declared {
		description, err := utils.GetStringProperty(properties, "Description")
		if err != nil {
			return nil, fmt.Errorf("servicediscovery: invalid Description: %w", err)
		}
		if description != "" {
			input.Description = aws.String(description)
		}
	}
	ttl, declared, err := soaTTL(properties)
	if err != nil {
		return nil, err
	}
	if declared {
		input.Properties = &servicediscoverytypes.PrivateDnsNamespaceProperties{
			DnsProperties: &servicediscoverytypes.PrivateDnsPropertiesMutable{
				SOA: &servicediscoverytypes.SOA{TTL: aws.Int64(ttl)},
			},
		}
	}
	if tags := tagsFromProperties(properties); len(tags) > 0 {
		input.Tags = tags
	}

	resp, err := client.CreatePrivateDnsNamespace(ctx, input)
	if err != nil {
		var duplicate *servicediscoverytypes.DuplicateRequest
		if errors.As(err, &duplicate) {
			return n.adoptDuplicatedCreate(ctx, client, duplicate, name)
		}
		return nil, fmt.Errorf("servicediscovery: CreatePrivateDnsNamespace: %w", err)
	}
	operationID := ""
	if resp != nil {
		operationID = aws.ToString(resp.OperationId)
	}
	if operationID == "" {
		return nil, errors.New("servicediscovery: CreatePrivateDnsNamespace returned no operation id")
	}

	namespaceID, err := n.awaitNamespaceID(ctx, client, operationID, name)
	if err != nil {
		return nil, err
	}
	plugin.LoggerFromContext(ctx).Info("servicediscovery: creating private DNS namespace",
		"name", name, "namespaceId", namespaceID, "operationId", operationID)

	return n.createInProgress(namespaceID, operationID), nil
}

// adoptDuplicatedCreate recovers the namespace behind a create rejected with
// DuplicateRequest, whose response carries no namespace id of its own. The id is
// taken from the operation the error names and, failing that, by resolving the
// namespace by name.
func (n *PrivateDnsNamespace) adoptDuplicatedCreate(
	ctx context.Context,
	client serviceDiscoveryClientInterface,
	duplicate *servicediscoverytypes.DuplicateRequest,
	name string,
) (*resource.CreateResult, error) {
	log := plugin.LoggerFromContext(ctx)

	if operationID := aws.ToString(duplicate.DuplicateOperationId); operationID != "" {
		namespaceID, err := n.awaitNamespaceID(ctx, client, operationID, name)
		if err == nil {
			log.Info("servicediscovery: adopted the namespace of a duplicated create request",
				"name", name, "namespaceId", namespaceID, "operationId", operationID)
			return n.createInProgress(namespaceID, operationID), nil
		}
		log.Warn("servicediscovery: duplicated create request named an operation that yielded no namespace id; resolving the namespace by name",
			"name", name, "operationId", operationID, "error", err.Error())
	}

	namespaceID, err := findNamespaceIDByName(ctx, client, name)
	if err != nil {
		return nil, fmt.Errorf("servicediscovery: create of namespace %q was rejected as a duplicate request and the existing namespace could not be resolved: %w", name, err)
	}
	log.Info("servicediscovery: adopted the existing namespace of a duplicated create request",
		"name", name, "namespaceId", namespaceID)

	// The operation that created this namespace is unknown, so the RequestID
	// carries no operation id and Status confirms the namespace itself.
	return n.createInProgress(namespaceID, ""), nil
}

func (n *PrivateDnsNamespace) createInProgress(namespaceID, operationID string) *resource.CreateResult {
	return &resource.CreateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:       resource.OperationCreate,
			OperationStatus: resource.OperationStatusInProgress,
			NativeID:        namespaceID,
			RequestID: encodeRequestID(requestState{
				OperationID: operationID,
				Deadline:    n.now().Add(namespaceOperationTimeout),
			}),
		},
	}
}

// awaitNamespaceID polls a namespace operation until it reports the id of the
// namespace it acts on. Cloud Map populates the NAMESPACE target while the
// operation is still running, so this is a short wait rather than a wait for the
// operation to complete — completion is the engine's Status polls to observe.
//
// The namespace record exists before the operation reports it, so an operation
// that reports no target falls back to resolving the namespace by name.
func (n *PrivateDnsNamespace) awaitNamespaceID(
	ctx context.Context,
	client serviceDiscoveryClientInterface,
	operationID string,
	name string,
) (string, error) {
	interval := n.namespaceTargetPollInterval
	if interval <= 0 {
		interval = namespaceTargetPollInterval
	}
	attempts := n.namespaceTargetPollAttempts
	if attempts <= 0 {
		attempts = namespaceTargetPollAttempts
	}

	for attempt := 1; attempt <= attempts; attempt++ {
		out, err := client.GetOperation(ctx, &servicediscoverysdk.GetOperationInput{
			OperationId: aws.String(operationID),
		})
		if err != nil {
			return "", fmt.Errorf("servicediscovery: GetOperation %s: %w", operationID, err)
		}
		if out != nil && out.Operation != nil {
			if namespaceID := out.Operation.Targets[namespaceTargetKey]; namespaceID != "" {
				return namespaceID, nil
			}
			if out.Operation.Status == servicediscoverytypes.OperationStatusFail {
				return "", fmt.Errorf("servicediscovery: namespace operation %s failed: %s",
					operationID, operationFailureMessage(out.Operation))
			}
		}
		if attempt == attempts {
			break
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(interval):
		}
	}

	namespaceID, err := findNamespaceIDByName(ctx, client, name)
	if err != nil {
		return "", fmt.Errorf("servicediscovery: namespace operation %s reported no %s target after %d attempts and namespace %q could not be resolved by name: %w",
			operationID, namespaceTargetKey, attempts, name, err)
	}
	plugin.LoggerFromContext(ctx).Warn("servicediscovery: namespace operation reported no namespace target; resolved the namespace by name",
		"name", name, "namespaceId", namespaceID, "operationId", operationID)
	return namespaceID, nil
}

// findNamespaceIDByName resolves a private DNS namespace this account owns by
// its name. Cloud Map has no lookup by name, so the namespaces are listed and
// matched.
func findNamespaceIDByName(ctx context.Context, client serviceDiscoveryClientInterface, name string) (string, error) {
	var nextToken *string
	for {
		out, err := client.ListNamespaces(ctx, &servicediscoverysdk.ListNamespacesInput{
			Filters:   privateDnsNamespaceFilters(),
			NextToken: nextToken,
		})
		if err != nil {
			return "", fmt.Errorf("ListNamespaces: %w", err)
		}
		if out == nil {
			return "", errors.New("ListNamespaces returned no response")
		}
		for _, summary := range out.Namespaces {
			if aws.ToString(summary.Name) == name {
				return aws.ToString(summary.Id), nil
			}
		}
		if aws.ToString(out.NextToken) == "" {
			return "", fmt.Errorf("no private DNS namespace named %q", name)
		}
		nextToken = out.NextToken
	}
}

// privateDnsNamespaceFilters narrows a namespace listing to the private DNS
// namespaces this account created. Namespaces shared from other accounts are
// excluded: they cannot be managed from here.
func privateDnsNamespaceFilters() []servicediscoverytypes.NamespaceFilter {
	return []servicediscoverytypes.NamespaceFilter{
		{
			Name:      servicediscoverytypes.NamespaceFilterNameType,
			Values:    []string{string(servicediscoverytypes.NamespaceTypeDnsPrivate)},
			Condition: servicediscoverytypes.FilterConditionEq,
		},
		{
			Name:      servicediscoverytypes.NamespaceFilterNameResourceOwner,
			Values:    []string{resourceOwnerSelf},
			Condition: servicediscoverytypes.FilterConditionEq,
		},
	}
}

// ----- Status -----

func (n *PrivateDnsNamespace) Status(ctx context.Context, request *resource.StatusRequest) (*resource.StatusResult, error) {
	state, err := decodeRequestID(request.RequestID)
	if err != nil {
		return nil, err
	}
	client, err := n.clientFactory(n.cfg)
	if err != nil {
		return nil, err
	}
	switch {
	case state.Phase == phaseRetryDelete:
		return n.statusFromDeleteRetry(ctx, client, request, state)
	case state.OperationID == "":
		return n.statusFromNamespace(ctx, client, request, state)
	default:
		return n.statusFromOperation(ctx, client, request, state)
	}
}

// statusFromOperation reports the progress of the Cloud Map operation the
// RequestID names.
func (n *PrivateDnsNamespace) statusFromOperation(
	ctx context.Context,
	client serviceDiscoveryClientInterface,
	request *resource.StatusRequest,
	state requestState,
) (*resource.StatusResult, error) {
	out, err := client.GetOperation(ctx, &servicediscoverysdk.GetOperationInput{
		OperationId: aws.String(state.OperationID),
	})
	if err != nil {
		var notFound *servicediscoverytypes.OperationNotFound
		if errors.As(err, &notFound) {
			return statusResult(request, resource.OperationStatusFailure,
				fmt.Sprintf("namespace operation %s no longer exists", state.OperationID)), nil
		}
		return nil, fmt.Errorf("servicediscovery: GetOperation %s: %w", state.OperationID, err)
	}
	if out == nil || out.Operation == nil {
		return nil, fmt.Errorf("servicediscovery: GetOperation %s returned no operation", state.OperationID)
	}

	switch out.Operation.Status {
	case servicediscoverytypes.OperationStatusSuccess:
		// A deleted namespace cannot be read back, and there are no properties
		// left to carry anywhere.
		if state.Phase == phaseDelete {
			return statusResult(request, resource.OperationStatusSuccess, ""), nil
		}
		properties, err := readProperties(ctx, client, request.NativeID)
		if err != nil {
			return nil, err
		}
		return successWithProperties(request, properties)
	case servicediscoverytypes.OperationStatusFail:
		if state.Phase == phaseDelete && operationReportsNamespaceInUse(out.Operation) {
			return enterDeleteRetry(ctx, request, request.NativeID, state.Deadline), nil
		}
		return statusResult(request, resource.OperationStatusFailure,
			fmt.Sprintf("namespace operation %s failed: %s", state.OperationID, operationFailureMessage(out.Operation))), nil
	default:
		if n.now().After(state.Deadline) {
			return statusResult(request, resource.OperationStatusFailure,
				fmt.Sprintf("timeout waiting for namespace operation %s to complete (deadline %s)",
					state.OperationID, state.Deadline.Format(time.RFC3339))), nil
		}
		return statusResult(request, resource.OperationStatusInProgress,
			fmt.Sprintf("namespace operation %s is %s", state.OperationID, out.Operation.Status)), nil
	}
}

// statusFromNamespace reports progress for a create that was adopted without an
// operation id of its own.
//
// The operation is recovered from the namespace's own operation history and
// reported on like any other. The namespace record exists from the moment Cloud
// Map accepts a create — including for one that goes on to fail and is rolled
// back — so the namespace being retrievable says nothing about whether the
// create succeeded.
//
// Cloud Map keeps an operation for a day. A namespace whose create is no longer
// listed settled long ago, and then the namespace itself is all there is left to
// confirm.
func (n *PrivateDnsNamespace) statusFromNamespace(
	ctx context.Context,
	client serviceDiscoveryClientInterface,
	request *resource.StatusRequest,
	state requestState,
) (*resource.StatusResult, error) {
	operationID, err := findCreateOperationID(ctx, client, request.NativeID)
	if err != nil {
		return nil, err
	}
	if operationID != "" {
		state.OperationID = operationID
		return n.statusFromOperation(ctx, client, request, state)
	}

	out, err := client.GetNamespace(ctx, &servicediscoverysdk.GetNamespaceInput{
		Id: aws.String(request.NativeID),
	})
	if err != nil {
		var notFound *servicediscoverytypes.NamespaceNotFound
		if !errors.As(err, &notFound) {
			return nil, fmt.Errorf("servicediscovery: GetNamespace %s: %w", request.NativeID, err)
		}
		if n.now().After(state.Deadline) {
			return statusResult(request, resource.OperationStatusFailure,
				fmt.Sprintf("timeout waiting for namespace %s to become available (deadline %s)",
					request.NativeID, state.Deadline.Format(time.RFC3339))), nil
		}
		return statusResult(request, resource.OperationStatusInProgress,
			fmt.Sprintf("namespace %s is not available yet", request.NativeID)), nil
	}
	if out == nil || out.Namespace == nil {
		return nil, fmt.Errorf("servicediscovery: GetNamespace %s returned no namespace", request.NativeID)
	}
	properties, err := namespaceProperties(ctx, client, out.Namespace)
	if err != nil {
		return nil, err
	}
	return successWithProperties(request, properties)
}

// findCreateOperationID recovers the id of the operation that created a
// namespace, and reports an empty id when Cloud Map no longer lists one.
//
// Cloud Map takes a page of operations and only then applies the filters, so a
// page can come back empty while its token still leads to the operation. Paging
// therefore continues on an empty page and stops only once the token runs out.
func findCreateOperationID(ctx context.Context, client serviceDiscoveryClientInterface, namespaceID string) (string, error) {
	input := &servicediscoverysdk.ListOperationsInput{
		Filters: []servicediscoverytypes.OperationFilter{
			{
				Name:      servicediscoverytypes.OperationFilterNameNamespaceId,
				Values:    []string{namespaceID},
				Condition: servicediscoverytypes.FilterConditionEq,
			},
			{
				Name:      servicediscoverytypes.OperationFilterNameType,
				Values:    []string{string(servicediscoverytypes.OperationTypeCreateNamespace)},
				Condition: servicediscoverytypes.FilterConditionEq,
			},
		},
	}
	for {
		out, err := client.ListOperations(ctx, input)
		if err != nil {
			return "", fmt.Errorf("servicediscovery: ListOperations for namespace %s: %w", namespaceID, err)
		}
		if out == nil {
			return "", fmt.Errorf("servicediscovery: ListOperations for namespace %s returned no response", namespaceID)
		}
		for _, operation := range out.Operations {
			if id := aws.ToString(operation.Id); id != "" {
				return id, nil
			}
		}
		if aws.ToString(out.NextToken) == "" {
			return "", nil
		}
		input.NextToken = out.NextToken
	}
}

// successWithProperties reports a settled namespace operation as a success and
// carries the namespace's current properties with it, so its read-only fields —
// Id, Arn and HostedZoneId — reach the stored row that the resolvables of
// dependent resources resolve against.
func successWithProperties(request *resource.StatusRequest, properties map[string]any) (*resource.StatusResult, error) {
	raw, err := json.Marshal(properties)
	if err != nil {
		return nil, fmt.Errorf("servicediscovery: marshal properties: %w", err)
	}
	result := statusResult(request, resource.OperationStatusSuccess, "")
	result.ProgressResult.ResourceProperties = raw
	return result, nil
}

// operationFailureMessage renders why an operation failed, pairing Cloud Map's
// error code with its message when both are present.
func operationFailureMessage(operation *servicediscoverytypes.Operation) string {
	message := aws.ToString(operation.ErrorMessage)
	code := aws.ToString(operation.ErrorCode)
	switch {
	case code != "" && message != "":
		return code + ": " + message
	case code != "":
		return code
	case message != "":
		return message
	default:
		return "no failure reason reported"
	}
}

// ----- Read -----

func (n *PrivateDnsNamespace) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	client, err := n.clientFactory(n.cfg)
	if err != nil {
		return nil, err
	}

	properties, err := readProperties(ctx, client, request.NativeID)
	if err != nil {
		var notFound *servicediscoverytypes.NamespaceNotFound
		if errors.As(err, &notFound) {
			return &resource.ReadResult{
				ResourceType: resourceType,
				ErrorCode:    resource.OperationErrorCodeNotFound,
			}, nil
		}
		return nil, err
	}

	raw, err := json.Marshal(properties)
	if err != nil {
		return nil, fmt.Errorf("servicediscovery: marshal properties: %w", err)
	}
	return &resource.ReadResult{
		ResourceType: resourceType,
		Properties:   string(raw),
	}, nil
}

// readProperties retrieves a namespace and renders it in the shape the schema
// declares.
func readProperties(ctx context.Context, client serviceDiscoveryClientInterface, namespaceID string) (map[string]any, error) {
	out, err := client.GetNamespace(ctx, &servicediscoverysdk.GetNamespaceInput{
		Id: aws.String(namespaceID),
	})
	if err != nil {
		return nil, fmt.Errorf("servicediscovery: GetNamespace %s: %w", namespaceID, err)
	}
	if out == nil || out.Namespace == nil {
		return nil, fmt.Errorf("servicediscovery: GetNamespace %s returned no namespace", namespaceID)
	}
	return namespaceProperties(ctx, client, out.Namespace)
}

// namespaceProperties renders a namespace in the shape the schema declares.
//
// The hosted zone id is emitted twice over: Cloud Map nests it under the
// namespace's DNS properties, and the schema declares it as a top-level
// read-only field. The nested copy is what the declared properties field
// compares against; the top-level one is what the hostedZoneId resolvable
// reads.
//
// Vpc is not emitted. Cloud Map exposes a namespace's VPC through no API at
// all, so any value here would be fabricated — and a fabricated value for a
// create-only field reads as drift and drives a destructive replace.
//
// A property the namespace does not carry is left out rather than emitted
// empty, so it does not compare as drift against a declaration that never set
// it.
func namespaceProperties(
	ctx context.Context,
	client serviceDiscoveryClientInterface,
	namespace *servicediscoverytypes.Namespace,
) (map[string]any, error) {
	properties := map[string]any{}
	putString(properties, "Id", aws.ToString(namespace.Id))
	putString(properties, "Name", aws.ToString(namespace.Name))
	putString(properties, "Description", aws.ToString(namespace.Description))

	arn := aws.ToString(namespace.Arn)
	putString(properties, "Arn", arn)

	if namespace.Properties != nil && namespace.Properties.DnsProperties != nil {
		dnsProperties := namespace.Properties.DnsProperties
		putString(properties, "HostedZoneId", aws.ToString(dnsProperties.HostedZoneId))
		if dnsProperties.SOA != nil && dnsProperties.SOA.TTL != nil {
			properties["Properties"] = map[string]any{
				"DnsProperties": map[string]any{
					"SOA": map[string]any{"TTL": *dnsProperties.SOA.TTL},
				},
			}
		}
	}

	// The tag APIs address a namespace by ARN, so the ARN the namespace itself
	// reports is what the tags are read with.
	if arn == "" {
		plugin.LoggerFromContext(ctx).Warn("servicediscovery: namespace reported no ARN; reading it without its tags",
			"namespaceId", aws.ToString(namespace.Id))
		return properties, nil
	}
	tags, err := namespaceTags(ctx, client, arn)
	if err != nil {
		return nil, err
	}
	if len(tags) > 0 {
		properties["Tags"] = tags
	}
	return properties, nil
}

// namespaceTags reads the tags of the namespace with the given ARN. Cloud Map
// reports them only through this call: they are absent from the namespace
// itself.
func namespaceTags(ctx context.Context, client serviceDiscoveryClientInterface, arn string) ([]map[string]any, error) {
	out, err := client.ListTagsForResource(ctx, &servicediscoverysdk.ListTagsForResourceInput{
		ResourceARN: aws.String(arn),
	})
	if err != nil {
		return nil, fmt.Errorf("servicediscovery: ListTagsForResource %s: %w", arn, err)
	}
	if out == nil {
		return nil, nil
	}
	var tags []map[string]any
	for _, tag := range out.Tags {
		key := aws.ToString(tag.Key)
		if key == "" {
			continue
		}
		tags = append(tags, map[string]any{"Key": key, "Value": aws.ToString(tag.Value)})
	}
	return tags, nil
}

// putString records a property, leaving it out when it has no value.
func putString(properties map[string]any, key, value string) {
	if value != "" {
		properties[key] = value
	}
}

// ----- Update -----

// Update applies the tag changes first and only then dispatches the change to
// the namespace's own properties.
//
// The tag calls are synchronous and idempotent while the property update is an
// operation that settles later, so an update interrupted between the two
// re-applies the tags harmlessly on the next attempt and dispatches the property
// update again. Dispatching first would leave the tags unapplied for good if the
// tag call is what failed.
//
// Name and Vpc are create-only, so a change to either is a replace the engine
// drives rather than an update to apply here.
func (n *PrivateDnsNamespace) Update(ctx context.Context, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	client, err := n.clientFactory(n.cfg)
	if err != nil {
		return nil, err
	}

	var prior, desired map[string]any
	if err := json.Unmarshal(request.PriorProperties, &prior); err != nil {
		return nil, fmt.Errorf("servicediscovery: parse prior properties: %w", err)
	}
	if err := json.Unmarshal(request.DesiredProperties, &desired); err != nil {
		return nil, fmt.Errorf("servicediscovery: parse desired properties: %w", err)
	}
	priorState, err := mutableNamespaceState(prior)
	if err != nil {
		return nil, err
	}
	desiredState, err := mutableNamespaceState(desired)
	if err != nil {
		return nil, err
	}

	if err := applyTagChanges(ctx, client, request.NativeID, prior, desired); err != nil {
		return nil, err
	}

	if desiredState == priorState {
		// Nothing about the namespace itself changed, so there is no operation to
		// wait for and the tags are already applied.
		properties, err := readProperties(ctx, client, request.NativeID)
		if err != nil {
			return nil, err
		}
		raw, err := json.Marshal(properties)
		if err != nil {
			return nil, fmt.Errorf("servicediscovery: marshal properties: %w", err)
		}
		return &resource.UpdateResult{
			ProgressResult: &resource.ProgressResult{
				Operation:          resource.OperationUpdate,
				OperationStatus:    resource.OperationStatusSuccess,
				NativeID:           request.NativeID,
				ResourceProperties: raw,
			},
		}, nil
	}

	resp, err := client.UpdatePrivateDnsNamespace(ctx, &servicediscoverysdk.UpdatePrivateDnsNamespaceInput{
		Id:        aws.String(request.NativeID),
		Namespace: desiredState.change(),
	})
	if err != nil {
		return nil, fmt.Errorf("servicediscovery: UpdatePrivateDnsNamespace %s: %w", request.NativeID, err)
	}
	operationID := ""
	if resp != nil {
		operationID = aws.ToString(resp.OperationId)
	}
	if operationID == "" {
		return nil, errors.New("servicediscovery: UpdatePrivateDnsNamespace returned no operation id")
	}
	plugin.LoggerFromContext(ctx).Info("servicediscovery: updating private DNS namespace",
		"namespaceId", request.NativeID, "operationId", operationID)

	return &resource.UpdateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:       resource.OperationUpdate,
			OperationStatus: resource.OperationStatusInProgress,
			NativeID:        request.NativeID,
			RequestID: encodeRequestID(requestState{
				OperationID: operationID,
				Deadline:    n.now().Add(namespaceOperationTimeout),
			}),
		},
	}, nil
}

// namespaceState is the part of a namespace's declaration Cloud Map lets an
// update change. Everything else the schema declares is either create-only or
// read-only.
type namespaceState struct {
	description string
	ttl         int64
	ttlDeclared bool
}

// mutableNamespaceState reads the updatable part of a declaration. An absent
// description is the empty one, so a description dropped from the declaration
// compares as a change and is cleared rather than left behind.
func mutableNamespaceState(properties map[string]any) (namespaceState, error) {
	var state namespaceState
	if _, declared := properties["Description"]; declared {
		description, err := utils.GetStringProperty(properties, "Description")
		if err != nil {
			return namespaceState{}, fmt.Errorf("servicediscovery: invalid Description: %w", err)
		}
		state.description = description
	}
	ttl, declared, err := soaTTL(properties)
	if err != nil {
		return namespaceState{}, err
	}
	state.ttl, state.ttlDeclared = ttl, declared
	return state, nil
}

// change renders the state as the update Cloud Map takes. The whole declared
// state is sent rather than only the fields that differ, so the namespace ends
// up matching the declaration either way.
//
// An undeclared SOA TTL leaves the SOA record alone: Cloud Map's own default for
// it is not readable from here, so there is nothing to reset it to.
func (s namespaceState) change() *servicediscoverytypes.PrivateDnsNamespaceChange {
	change := &servicediscoverytypes.PrivateDnsNamespaceChange{
		Description: aws.String(s.description),
	}
	if s.ttlDeclared {
		change.Properties = &servicediscoverytypes.PrivateDnsNamespacePropertiesChange{
			DnsProperties: &servicediscoverytypes.PrivateDnsPropertiesMutableChange{
				SOA: &servicediscoverytypes.SOAChange{TTL: aws.Int64(s.ttl)},
			},
		}
	}
	return change
}

// applyTagChanges brings a namespace's tags to what the declaration carries.
// Cloud Map overwrites the value of a key that is tagged again, so only a key
// the declaration has dropped altogether has to be untagged.
func applyTagChanges(
	ctx context.Context,
	client serviceDiscoveryClientInterface,
	namespaceID string,
	prior, desired map[string]any,
) error {
	toSet, toRemove := diffTags(tagSetFromProperties(prior), tagSetFromProperties(desired))
	if len(toSet) == 0 && len(toRemove) == 0 {
		return nil
	}

	// The tag APIs address a namespace by ARN rather than by id.
	arn, err := resolveNamespaceARN(ctx, client, namespaceID, prior)
	if err != nil {
		return err
	}
	if len(toRemove) > 0 {
		_, err := client.UntagResource(ctx, &servicediscoverysdk.UntagResourceInput{
			ResourceARN: aws.String(arn),
			TagKeys:     toRemove,
		})
		if err != nil {
			return fmt.Errorf("servicediscovery: UntagResource %s: %w", arn, err)
		}
	}
	if len(toSet) > 0 {
		_, err := client.TagResource(ctx, &servicediscoverysdk.TagResourceInput{
			ResourceARN: aws.String(arn),
			Tags:        toSet,
		})
		if err != nil {
			return fmt.Errorf("servicediscovery: TagResource %s: %w", arn, err)
		}
	}
	return nil
}

// resolveNamespaceARN resolves the ARN the tag APIs address a namespace by, taking it
// from the state already read where it is recorded and reading the namespace for
// it where it is not.
func resolveNamespaceARN(
	ctx context.Context,
	client serviceDiscoveryClientInterface,
	namespaceID string,
	properties map[string]any,
) (string, error) {
	if arn, ok := properties["Arn"].(string); ok && arn != "" {
		return arn, nil
	}
	out, err := client.GetNamespace(ctx, &servicediscoverysdk.GetNamespaceInput{
		Id: aws.String(namespaceID),
	})
	if err != nil {
		return "", fmt.Errorf("servicediscovery: GetNamespace %s: %w", namespaceID, err)
	}
	if out == nil || out.Namespace == nil {
		return "", fmt.Errorf("servicediscovery: GetNamespace %s returned no namespace", namespaceID)
	}
	arn := aws.ToString(out.Namespace.Arn)
	if arn == "" {
		return "", fmt.Errorf("servicediscovery: namespace %s reports no ARN to address its tags by", namespaceID)
	}
	return arn, nil
}

// ----- Delete -----

func (n *PrivateDnsNamespace) Delete(ctx context.Context, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	client, err := n.clientFactory(n.cfg)
	if err != nil {
		return nil, err
	}

	outcome, err := n.requestDelete(ctx, client, request.NativeID, n.now().Add(namespaceDeleteTimeout))
	if err != nil {
		return nil, err
	}
	return &resource.DeleteResult{
		ProgressResult: &resource.ProgressResult{
			Operation:       resource.OperationDelete,
			OperationStatus: outcome.status,
			NativeID:        request.NativeID,
			RequestID:       outcome.requestID,
			StatusMessage:   outcome.message,
		},
	}, nil
}

// statusFromDeleteRetry re-issues a delete Cloud Map rejected because the
// namespace still held resources. The rejection carries no operation id, so
// there is nothing to poll and the retry is the poll. The deadline the delete
// runs against travels in the RequestID, so it outlives a restart.
func (n *PrivateDnsNamespace) statusFromDeleteRetry(
	ctx context.Context,
	client serviceDiscoveryClientInterface,
	request *resource.StatusRequest,
	state requestState,
) (*resource.StatusResult, error) {
	if n.now().After(state.Deadline) {
		return statusResult(request, resource.OperationStatusFailure,
			fmt.Sprintf("timeout waiting for the resources in namespace %s to be released (deadline %s)",
				request.NativeID, state.Deadline.Format(time.RFC3339))), nil
	}

	// The retry runs against the deadline the delete started with, so a namespace
	// that stays occupied runs the wait out rather than extending it.
	outcome, err := n.requestDelete(ctx, client, request.NativeID, state.Deadline)
	if err != nil {
		return nil, err
	}
	return statusResultInPhase(request, outcome.status, outcome.requestID, outcome.message), nil
}

// deleteOutcome is how far a request to delete a namespace got, in the terms the
// engine's next poll resumes from.
type deleteOutcome struct {
	status    resource.OperationStatus
	requestID string
	message   string
}

// requestDelete asks Cloud Map to delete a namespace. A namespace that is
// already gone is a success; a namespace that still holds resources moves to the
// retry phase, since the resources are released asynchronously by whatever
// registered them; anything else is the caller's error to report.
//
// The deadline bounds the delete as a whole and is carried into whichever phase
// the attempt lands in, rather than renewed per attempt.
func (n *PrivateDnsNamespace) requestDelete(
	ctx context.Context,
	client serviceDiscoveryClientInterface,
	namespaceID string,
	deadline time.Time,
) (deleteOutcome, error) {
	out, err := client.DeleteNamespace(ctx, &servicediscoverysdk.DeleteNamespaceInput{
		Id: aws.String(namespaceID),
	})
	if err != nil {
		var notFound *servicediscoverytypes.NamespaceNotFound
		if errors.As(err, &notFound) {
			return deleteOutcome{status: resource.OperationStatusSuccess}, nil
		}
		var inUse *servicediscoverytypes.ResourceInUse
		if errors.As(err, &inUse) {
			return deleteRetryOutcome(ctx, namespaceID, deadline), nil
		}
		return deleteOutcome{}, fmt.Errorf("servicediscovery: DeleteNamespace %s: %w", namespaceID, err)
	}

	operationID := ""
	if out != nil {
		operationID = aws.ToString(out.OperationId)
	}
	if operationID == "" {
		return deleteOutcome{}, fmt.Errorf("servicediscovery: DeleteNamespace %s returned no operation id", namespaceID)
	}
	plugin.LoggerFromContext(ctx).Info("servicediscovery: deleting private DNS namespace",
		"namespaceId", namespaceID, "operationId", operationID)

	return deleteOutcome{
		status: resource.OperationStatusInProgress,
		requestID: encodeRequestID(requestState{
			Phase:       phaseDelete,
			OperationID: operationID,
			Deadline:    deadline,
		}),
		message: fmt.Sprintf("namespace operation %s is deleting namespace %s", operationID, namespaceID),
	}, nil
}

// enterDeleteRetry reports a delete blocked by the resources still in the
// namespace as progress towards a retry rather than as a failure, under the
// deadline the delete already runs against.
func enterDeleteRetry(
	ctx context.Context,
	request *resource.StatusRequest,
	namespaceID string,
	deadline time.Time,
) *resource.StatusResult {
	outcome := deleteRetryOutcome(ctx, namespaceID, deadline)
	return statusResultInPhase(request, outcome.status, outcome.requestID, outcome.message)
}

func deleteRetryOutcome(ctx context.Context, namespaceID string, deadline time.Time) deleteOutcome {
	plugin.LoggerFromContext(ctx).Info("servicediscovery: namespace still holds resources; retrying its deletion",
		"namespaceId", namespaceID, "deadline", deadline.Format(time.RFC3339))
	return deleteOutcome{
		status:    resource.OperationStatusInProgress,
		requestID: encodeRequestID(requestState{Phase: phaseRetryDelete, Deadline: deadline}),
		message: fmt.Sprintf("namespace %s still holds resources; retrying its deletion until %s",
			namespaceID, deadline.Format(time.RFC3339)),
	}
}

// operationReportsNamespaceInUse reports whether a delete operation failed
// because the namespace still held resources, which Cloud Map reports on the
// operation when it accepted the delete before finding them.
func operationReportsNamespaceInUse(operation *servicediscoverytypes.Operation) bool {
	return strings.EqualFold(aws.ToString(operation.ErrorCode), resourceInUseErrorCode)
}

// ----- List -----

// List reports the private DNS namespaces this account owns. Namespaces other
// accounts have shared with it are excluded: they cannot be tagged or deleted
// from here, and their hosted zone may not be readable either.
//
// Cloud Map takes a page of namespaces and only then applies the filters, so a
// page can come back empty while its token still leads to matching namespaces.
// Paging therefore continues on an empty page and stops only once the token
// runs out.
func (n *PrivateDnsNamespace) List(ctx context.Context, request *resource.ListRequest) (*resource.ListResult, error) {
	client, err := n.clientFactory(n.cfg)
	if err != nil {
		return nil, err
	}

	input := &servicediscoverysdk.ListNamespacesInput{Filters: privateDnsNamespaceFilters()}
	if request.PageSize > 0 {
		input.MaxResults = aws.Int32(request.PageSize)
	}
	if request.PageToken != nil && *request.PageToken != "" {
		input.NextToken = request.PageToken
	}

	var nativeIDs []string
	for {
		out, err := client.ListNamespaces(ctx, input)
		if err != nil {
			return nil, fmt.Errorf("servicediscovery: ListNamespaces: %w", err)
		}
		if out == nil {
			return nil, errors.New("servicediscovery: ListNamespaces returned no response")
		}
		for _, summary := range out.Namespaces {
			if id := aws.ToString(summary.Id); id != "" {
				nativeIDs = append(nativeIDs, id)
			}
		}
		if aws.ToString(out.NextToken) == "" {
			return &resource.ListResult{NativeIDs: nativeIDs}, nil
		}
		if len(nativeIDs) > 0 {
			return &resource.ListResult{NativeIDs: nativeIDs, NextPageToken: out.NextToken}, nil
		}
		input.NextToken = out.NextToken
	}
}

// ----- helpers -----

// creatorRequestID derives the CreatorRequestId Cloud Map deduplicates on from
// the resource's identity, so a create replayed after a lost response is
// recognised as the same request rather than provisioning a second namespace.
func creatorRequestID(typeName, label, name, vpc string) string {
	digest := sha256.Sum256([]byte(strings.Join([]string{typeName, label, name, vpc}, "|")))
	return creatorRequestIDPrefix + hex.EncodeToString(digest[:24])
}

// soaTTL reads Properties.DnsProperties.SOA.TTL, which is the only nested
// property the schema declares. It reports false when any level is absent, so
// the create leaves the SOA record at Cloud Map's own default, and errors when a
// level is present with a type the schema does not allow rather than dropping
// the declared value.
func soaTTL(properties map[string]any) (int64, bool, error) {
	soa, declared, err := nestedObject(properties, "Properties", "DnsProperties", "SOA")
	if err != nil || !declared {
		return 0, false, err
	}
	value, declared := soa["TTL"]
	if !declared {
		return 0, false, nil
	}
	ttl, ok := value.(float64)
	if !ok {
		return 0, false, errors.New("servicediscovery: invalid Properties.DnsProperties.SOA.TTL: not a number")
	}
	return int64(ttl), true, nil
}

// nestedObject walks a path of object-valued keys through a properties map. It
// reports false as soon as a key along the path is absent, and errors when one
// holds anything but an object.
func nestedObject(properties map[string]any, path ...string) (map[string]any, bool, error) {
	current := properties
	for depth, key := range path {
		value, declared := current[key]
		if !declared {
			return nil, false, nil
		}
		nested, ok := value.(map[string]any)
		if !ok {
			return nil, false, fmt.Errorf("servicediscovery: invalid %s: not an object", strings.Join(path[:depth+1], "."))
		}
		current = nested
	}
	return current, true, nil
}

// tagsFromProperties parses a "Tags" property of shape
//
//	[{"Key": "...", "Value": "..."}, ...]
//
// into the Cloud Map SDK's Tag slice.
func tagsFromProperties(properties map[string]any) []servicediscoverytypes.Tag {
	raw, ok := properties["Tags"].([]any)
	if !ok {
		return nil
	}
	var tags []servicediscoverytypes.Tag
	for _, entry := range raw {
		fields, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		key, _ := fields["Key"].(string)
		if key == "" {
			continue
		}
		tag := servicediscoverytypes.Tag{Key: aws.String(key)}
		if value, ok := fields["Value"].(string); ok {
			tag.Value = aws.String(value)
		}
		tags = append(tags, tag)
	}
	return tags
}

// tagSetFromProperties reads a "Tags" property as a key/value map for diffing.
func tagSetFromProperties(properties map[string]any) map[string]string {
	tags := map[string]string{}
	for _, tag := range tagsFromProperties(properties) {
		tags[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
	}
	return tags
}

// diffTags reports the tags to set and the tag keys to remove to bring prior to
// desired. Both are ordered by key, so the same change makes the same calls.
func diffTags(prior, desired map[string]string) (toSet []servicediscoverytypes.Tag, toRemove []string) {
	for _, key := range sortedKeys(desired) {
		value := desired[key]
		if priorValue, tagged := prior[key]; !tagged || priorValue != value {
			toSet = append(toSet, servicediscoverytypes.Tag{Key: aws.String(key), Value: aws.String(value)})
		}
	}
	for _, key := range sortedKeys(prior) {
		if _, declared := desired[key]; !declared {
			toRemove = append(toRemove, key)
		}
	}
	return toSet, toRemove
}

func sortedKeys(tags map[string]string) []string {
	keys := make([]string, 0, len(tags))
	for key := range tags {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
