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

// ----- RequestID codec -----

// requestState is what the RequestID carries from an asynchronous operation to
// the Status polls that follow it. OperationID is empty when a create was
// adopted from an existing namespace whose operation could not be identified;
// Status then confirms the namespace itself instead of an operation.
//
// The encoding is a `key=value;…` list, and decoding ignores keys it does not
// know, so keys can be added without invalidating a RequestID already in flight.
type requestState struct {
	OperationID string
	Deadline    time.Time
}

func encodeRequestID(state requestState) string {
	return fmt.Sprintf("op=%s;deadline=%s", state.OperationID, state.Deadline.UTC().Format(time.RFC3339))
}

func decodeRequestID(requestID string) (requestState, error) {
	var state requestState
	var haveDeadline bool
	for _, field := range strings.Split(requestID, ";") {
		if field == "" {
			continue
		}
		key, value, ok := strings.Cut(field, "=")
		if !ok {
			return requestState{}, fmt.Errorf("servicediscovery: invalid RequestID %q", requestID)
		}
		switch key {
		case "op":
			state.OperationID = value
		case "deadline":
			deadline, err := time.Parse(time.RFC3339, value)
			if err != nil {
				return requestState{}, fmt.Errorf("servicediscovery: invalid deadline in RequestID %q: %w", requestID, err)
			}
			state.Deadline = deadline
			haveDeadline = true
		}
	}
	if !haveDeadline {
		return requestState{}, fmt.Errorf("servicediscovery: invalid RequestID %q: no deadline", requestID)
	}
	return state, nil
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
	if description, ok := properties["Description"].(string); ok && description != "" {
		input.Description = aws.String(description)
	}
	if ttl, ok := soaTTL(properties); ok {
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
		if adopted := n.adoptExistingNamespace(ctx, err, aws.ToString(input.CreatorRequestId), name); adopted != nil {
			return adopted, nil
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

	namespaceID, err := n.awaitNamespaceID(ctx, client, operationID)
	if err != nil {
		return nil, err
	}
	plugin.LoggerFromContext(ctx).Info("servicediscovery: creating private DNS namespace",
		"name", name, "namespaceId", namespaceID, "operationId", operationID)

	return n.createInProgress(namespaceID, operationID), nil
}

// adoptDuplicatedCreate recovers the namespace a deduplicated create refers to.
//
// A create replayed with the same CreatorRequestId is rejected with
// DuplicateRequest, and the response that would have carried the operation id is
// exactly what the replay no longer gets — so failing here would strand a
// namespace that exists but was never adopted. The id is taken from the
// operation the error names and, failing that, by resolving the namespace by
// name.
func (n *PrivateDnsNamespace) adoptDuplicatedCreate(
	ctx context.Context,
	client serviceDiscoveryClientInterface,
	duplicate *servicediscoverytypes.DuplicateRequest,
	name string,
) (*resource.CreateResult, error) {
	log := plugin.LoggerFromContext(ctx)

	if operationID := aws.ToString(duplicate.DuplicateOperationId); operationID != "" {
		namespaceID, err := n.awaitNamespaceID(ctx, client, operationID)
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

// adoptExistingNamespace recovers the namespace behind a NamespaceAlreadyExists
// rejection, which is how a replayed create is answered once the original create
// operation has completed. Cloud Map reports the creator request id the existing
// namespace was created with, so the namespace is adopted only when it is the
// one this resource created — a namespace of the same name created by anything
// else is a genuine collision and is left to fail. It returns nil when the
// rejection is not an adoptable one.
func (n *PrivateDnsNamespace) adoptExistingNamespace(ctx context.Context, err error, creatorRequestID, name string) *resource.CreateResult {
	var exists *servicediscoverytypes.NamespaceAlreadyExists
	if !errors.As(err, &exists) {
		return nil
	}
	namespaceID := aws.ToString(exists.NamespaceId)
	if namespaceID == "" || aws.ToString(exists.CreatorRequestId) != creatorRequestID {
		return nil
	}
	plugin.LoggerFromContext(ctx).Info("servicediscovery: adopted the existing namespace of a replayed create request",
		"name", name, "namespaceId", namespaceID)

	// The operation that created this namespace is unknown, so the RequestID
	// carries no operation id and Status confirms the namespace itself.
	return n.createInProgress(namespaceID, "")
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
func (n *PrivateDnsNamespace) awaitNamespaceID(
	ctx context.Context,
	client serviceDiscoveryClientInterface,
	operationID string,
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
	return "", fmt.Errorf("servicediscovery: namespace operation %s reported no %s target after %d attempts",
		operationID, namespaceTargetKey, attempts)
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
	if state.OperationID == "" {
		return n.statusFromNamespace(ctx, client, request, state)
	}
	return n.statusFromOperation(ctx, client, request, state)
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
		return statusResult(request, resource.OperationStatusSuccess, ""), nil
	case servicediscoverytypes.OperationStatusFail:
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
// operation to poll: the namespace exists as far as the listing was concerned,
// so success is it being retrievable in its own right.
func (n *PrivateDnsNamespace) statusFromNamespace(
	ctx context.Context,
	client serviceDiscoveryClientInterface,
	request *resource.StatusRequest,
	state requestState,
) (*resource.StatusResult, error) {
	_, err := client.GetNamespace(ctx, &servicediscoverysdk.GetNamespaceInput{
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
	return statusResult(request, resource.OperationStatusSuccess, ""), nil
}

func statusResult(request *resource.StatusRequest, status resource.OperationStatus, message string) *resource.StatusResult {
	return &resource.StatusResult{
		ProgressResult: &resource.ProgressResult{
			OperationStatus: status,
			NativeID:        request.NativeID,
			RequestID:       request.RequestID,
			StatusMessage:   message,
		},
	}
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

// ----- Read / Update / Delete / List -----

func (n *PrivateDnsNamespace) Read(_ context.Context, _ *resource.ReadRequest) (*resource.ReadResult, error) {
	return nil, errors.New("servicediscovery: reading a private DNS namespace is not implemented")
}

func (n *PrivateDnsNamespace) Update(_ context.Context, _ *resource.UpdateRequest) (*resource.UpdateResult, error) {
	return nil, errors.New("servicediscovery: updating a private DNS namespace is not implemented")
}

func (n *PrivateDnsNamespace) Delete(_ context.Context, _ *resource.DeleteRequest) (*resource.DeleteResult, error) {
	return nil, errors.New("servicediscovery: deleting a private DNS namespace is not implemented")
}

func (n *PrivateDnsNamespace) List(_ context.Context, _ *resource.ListRequest) (*resource.ListResult, error) {
	return nil, errors.New("servicediscovery: listing private DNS namespaces is not implemented")
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
// the create leaves the SOA record at Cloud Map's own default.
func soaTTL(properties map[string]any) (int64, bool) {
	nested, ok := properties["Properties"].(map[string]any)
	if !ok {
		return 0, false
	}
	dnsProperties, ok := nested["DnsProperties"].(map[string]any)
	if !ok {
		return 0, false
	}
	soa, ok := dnsProperties["SOA"].(map[string]any)
	if !ok {
		return 0, false
	}
	ttl, ok := soa["TTL"].(float64)
	if !ok {
		return 0, false
	}
	return int64(ttl), true
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
