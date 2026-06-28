// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package route53

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/route53"
	"github.com/aws/aws-sdk-go-v2/service/route53/types"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres/prov"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres/registry"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/config"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres/utils"
)

const recordSetGroupType = "AWS::Route53::RecordSetGroup"

// maxChangesPerBatch is Route53's documented cap on the number of changes in a
// single ChangeResourceRecordSets batch. The provisioner applies the whole
// group atomically in one batch, so a group larger than this cannot be applied.
const maxChangesPerBatch = 1000

// unsupportedRecordFields are the advanced routing-policy fields the provisioner
// does not support. They are rejected (not silently dropped) because without
// SetIdentifier, (Name, Type) is a complete Route53 record identity — the
// foundation of the NativeID key model. Supporting weighted/latency/geo routing
// is deferred to a follow-up.
var unsupportedRecordFields = []string{
	"SetIdentifier",
	"Weight",
	"GeoLocation",
	"GeoProximityLocation",
	"Failover",
	"MultiValueAnswer",
	"Region",
	"CidrRoutingConfig",
	"HealthCheckId",
}

// recordKey is the complete identity of a supported record within a group:
// its canonical (trailing-dot) name and record type.
type recordKey struct {
	Name string `json:"Name"`
	Type string `json:"Type"`
}

type recordSetGroupClientInterface interface {
	ChangeResourceRecordSets(ctx context.Context, params *route53.ChangeResourceRecordSetsInput, optFns ...func(*route53.Options)) (*route53.ChangeResourceRecordSetsOutput, error)
	ListResourceRecordSets(ctx context.Context, params *route53.ListResourceRecordSetsInput, optFns ...func(*route53.Options)) (*route53.ListResourceRecordSetsOutput, error)
	GetChange(ctx context.Context, params *route53.GetChangeInput, optFns ...func(*route53.Options)) (*route53.GetChangeOutput, error)
}

type RecordSetGroup struct {
	cfg *config.Config
}

var _ prov.Provisioner = &RecordSetGroup{}

func init() {
	registry.Register(recordSetGroupType,
		[]resource.Operation{
			resource.OperationCreate,
			resource.OperationRead,
			resource.OperationUpdate,
			resource.OperationCheckStatus,
			resource.OperationDelete,
		},
		func(cfg *config.Config) prov.Provisioner {
			return &RecordSetGroup{cfg: cfg}
		})
}

// canonicalName returns the Route53 canonical form of a DNS name (trailing dot).
func canonicalName(name string) string {
	if !strings.HasSuffix(name, ".") {
		return name + "."
	}
	return name
}

// encodeRecordSetGroupNativeID builds the composite identity for a group:
// "<hostedZoneId>|<base64url(JSON of sorted [{Name,Type}] key list)>". The key
// list is variable-length and record names can contain delimiter characters, so
// base64(JSON) — not a plain delimiter-join — keeps it collision-free. Names are
// canonicalized before encoding so encode/decode/match are dot-consistent.
func encodeRecordSetGroupNativeID(hostedZoneID string, keys []recordKey) (string, error) {
	canon := make([]recordKey, len(keys))
	for i, k := range keys {
		canon[i] = recordKey{Name: canonicalName(k.Name), Type: k.Type}
	}
	sort.Slice(canon, func(i, j int) bool {
		if canon[i].Name != canon[j].Name {
			return canon[i].Name < canon[j].Name
		}
		return canon[i].Type < canon[j].Type
	})
	data, err := json.Marshal(canon)
	if err != nil {
		return "", fmt.Errorf("failed to encode record keys: %w", err)
	}
	return hostedZoneID + "|" + base64.RawURLEncoding.EncodeToString(data), nil
}

func decodeRecordSetGroupNativeID(nativeID string) (string, []recordKey, error) {
	parts := strings.SplitN(nativeID, "|", 2)
	if len(parts) != 2 || parts[0] == "" {
		return "", nil, fmt.Errorf("invalid NativeID format: expected 'hostedZoneId|base64keys', got: %s", nativeID)
	}
	data, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", nil, fmt.Errorf("invalid NativeID key segment: %w", err)
	}
	var keys []recordKey
	if err := json.Unmarshal(data, &keys); err != nil {
		return "", nil, fmt.Errorf("invalid NativeID key list: %w", err)
	}
	return parts[0], keys, nil
}

// parseRecordSets extracts and validates the RecordSets list from a property
// map. A group with no records cannot be applied as an atomic change batch.
func parseRecordSets(properties map[string]any) ([]any, error) {
	raw, ok := properties["RecordSets"].([]any)
	if !ok || len(raw) == 0 {
		return nil, fmt.Errorf("at least one record set is required")
	}
	return raw, nil
}

// buildResourceRecordSet converts one declared record map into an AWS
// ResourceRecordSet, validating it is a supported simple record, and returns its
// canonical key.
func buildResourceRecordSet(rec map[string]any) (*types.ResourceRecordSet, recordKey, error) {
	for _, field := range unsupportedRecordFields {
		if _, present := rec[field]; present {
			return nil, recordKey{}, fmt.Errorf("unsupported routing field %q: the RecordSetGroup provisioner supports simple records only (name, type, ttl, resourceRecords, aliasTarget)", field)
		}
	}

	name, err := utils.GetStringProperty(rec, "Name")
	if err != nil {
		return nil, recordKey{}, fmt.Errorf("invalid record Name: %w", err)
	}
	recordType, err := utils.GetStringProperty(rec, "Type")
	if err != nil {
		return nil, recordKey{}, fmt.Errorf("invalid record Type: %w", err)
	}
	cName := canonicalName(name)

	rrs := &types.ResourceRecordSet{
		Name: aws.String(cName),
		Type: types.RRType(recordType),
	}

	var aliasTarget *types.AliasTarget
	if aliasRaw, hasAlias := rec["AliasTarget"].(map[string]any); hasAlias {
		aliasTarget, err = buildAliasTarget(aliasRaw)
		if err != nil {
			return nil, recordKey{}, fmt.Errorf("record %q: %w", name, err)
		}
	}

	records := extractResourceRecords(rec)
	if aliasTarget != nil {
		if len(records) > 0 {
			return nil, recordKey{}, fmt.Errorf("record %q: aliasTarget and resourceRecords are mutually exclusive", name)
		}
		rrs.AliasTarget = aliasTarget
	} else {
		if len(records) == 0 {
			return nil, recordKey{}, fmt.Errorf("record %q: at least one resourceRecord is required when aliasTarget is not set", name)
		}
		rrs.ResourceRecords = records
		rrs.TTL = aws.Int64(utils.GetInt64Property(rec, "TTL", 300))
	}

	return rrs, recordKey{Name: cName, Type: recordType}, nil
}

func extractResourceRecords(rec map[string]any) []types.ResourceRecord {
	raw, ok := rec["ResourceRecords"].([]any)
	if !ok {
		return nil
	}
	records := make([]types.ResourceRecord, 0, len(raw))
	for _, r := range raw {
		if value, ok := r.(string); ok && value != "" {
			records = append(records, types.ResourceRecord{Value: aws.String(value)})
		}
	}
	return records
}

// listAllRecordSets paginates the full set of record sets in a hosted zone.
func listAllRecordSets(ctx context.Context, client recordSetGroupClientInterface, hostedZoneID string) ([]types.ResourceRecordSet, error) {
	var all []types.ResourceRecordSet
	input := &route53.ListResourceRecordSetsInput{HostedZoneId: aws.String(hostedZoneID)}
	for {
		resp, err := client.ListResourceRecordSets(ctx, input)
		if err != nil {
			return nil, err
		}
		all = append(all, resp.ResourceRecordSets...)
		if !resp.IsTruncated {
			break
		}
		input.StartRecordName = resp.NextRecordName
		input.StartRecordType = resp.NextRecordType
		input.StartRecordIdentifier = resp.NextRecordIdentifier
	}
	return all, nil
}

// indexRecordSetsByKey maps live record sets by their canonical key.
func indexRecordSetsByKey(recordSets []types.ResourceRecordSet) map[recordKey]*types.ResourceRecordSet {
	m := make(map[recordKey]*types.ResourceRecordSet, len(recordSets))
	for i := range recordSets {
		rrs := &recordSets[i]
		key := recordKey{Name: canonicalName(aws.ToString(rrs.Name)), Type: string(rrs.Type)}
		m[key] = rrs
	}
	return m
}

func (r *RecordSetGroup) Create(ctx context.Context, request *resource.CreateRequest) (*resource.CreateResult, error) {
	awsCfg, err := r.cfg.ToAwsConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to load AWS config: %w", err)
	}
	return r.createWithClient(ctx, route53.NewFromConfig(awsCfg), request)
}

func (r *RecordSetGroup) createWithClient(ctx context.Context, client recordSetGroupClientInterface, request *resource.CreateRequest) (*resource.CreateResult, error) {
	var properties map[string]any
	if err := json.Unmarshal(request.Properties, &properties); err != nil {
		return nil, fmt.Errorf("failed to parse properties: %w", err)
	}

	hostedZoneID, err := utils.GetStringProperty(properties, "HostedZoneId")
	if err != nil {
		return nil, fmt.Errorf("invalid HostedZoneId: %w", err)
	}

	rawRecords, err := parseRecordSets(properties)
	if err != nil {
		return nil, err
	}
	if len(rawRecords) > maxChangesPerBatch {
		return nil, fmt.Errorf("record set group has %d records, which exceeds the Route53 atomic change-batch limit of %d", len(rawRecords), maxChangesPerBatch)
	}

	changes := make([]types.Change, 0, len(rawRecords))
	keys := make([]recordKey, 0, len(rawRecords))
	seen := make(map[recordKey]bool, len(rawRecords))
	for _, raw := range rawRecords {
		rec, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("invalid record set entry: expected an object, got %T", raw)
		}
		rrs, key, err := buildResourceRecordSet(rec)
		if err != nil {
			return nil, err
		}
		if seen[key] {
			return nil, fmt.Errorf("duplicate record (Name=%s, Type=%s): a record set group cannot declare the same name+type twice without a routing policy", key.Name, key.Type)
		}
		seen[key] = true
		keys = append(keys, key)
		changes = append(changes, types.Change{Action: types.ChangeActionCreate, ResourceRecordSet: rrs})
	}

	result, err := client.ChangeResourceRecordSets(ctx, &route53.ChangeResourceRecordSetsInput{
		HostedZoneId: aws.String(hostedZoneID),
		ChangeBatch:  &types.ChangeBatch{Changes: changes},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create record set group: %w", err)
	}

	nativeID, err := encodeRecordSetGroupNativeID(hostedZoneID, keys)
	if err != nil {
		return nil, err
	}

	return &resource.CreateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:       resource.OperationCreate,
			OperationStatus: resource.OperationStatusInProgress,
			RequestID:       aws.ToString(result.ChangeInfo.Id),
			NativeID:        nativeID,
		},
	}, nil
}

func (r *RecordSetGroup) Update(ctx context.Context, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	awsCfg, err := r.cfg.ToAwsConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to load AWS config: %w", err)
	}
	return r.updateWithClient(ctx, route53.NewFromConfig(awsCfg), request)
}

func (r *RecordSetGroup) updateWithClient(ctx context.Context, client recordSetGroupClientInterface, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	var priorProperties, desiredProperties map[string]any
	if err := json.Unmarshal(request.PriorProperties, &priorProperties); err != nil {
		return nil, fmt.Errorf("failed to parse prior properties: %w", err)
	}
	if err := json.Unmarshal(request.DesiredProperties, &desiredProperties); err != nil {
		return nil, fmt.Errorf("failed to parse desired properties: %w", err)
	}

	priorZoneID, err := utils.GetStringProperty(priorProperties, "HostedZoneId")
	if err != nil {
		return nil, fmt.Errorf("invalid prior HostedZoneId: %w", err)
	}
	desiredZoneID, err := utils.GetStringProperty(desiredProperties, "HostedZoneId")
	if err != nil {
		return nil, fmt.Errorf("invalid desired HostedZoneId: %w", err)
	}
	// hostedZoneId is createOnly; a zone change is a framework-driven replace,
	// never an in-place update.
	if priorZoneID != desiredZoneID {
		return nil, fmt.Errorf("cannot move a record set group between hosted zones (%s -> %s)", priorZoneID, desiredZoneID)
	}

	desiredRecords, err := parseRecordSets(desiredProperties)
	if err != nil {
		return nil, err
	}
	if len(desiredRecords) > maxChangesPerBatch {
		return nil, fmt.Errorf("record set group has %d records, which exceeds the Route53 atomic change-batch limit of %d", len(desiredRecords), maxChangesPerBatch)
	}

	changes := make([]types.Change, 0, len(desiredRecords))
	desiredKeys := make([]recordKey, 0, len(desiredRecords))
	desiredSet := make(map[recordKey]bool, len(desiredRecords))
	for _, raw := range desiredRecords {
		rec, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("invalid record set entry: expected an object, got %T", raw)
		}
		rrs, key, err := buildResourceRecordSet(rec)
		if err != nil {
			return nil, err
		}
		if desiredSet[key] {
			return nil, fmt.Errorf("duplicate record (Name=%s, Type=%s): a record set group cannot declare the same name+type twice without a routing policy", key.Name, key.Type)
		}
		desiredSet[key] = true
		desiredKeys = append(desiredKeys, key)
		changes = append(changes, types.Change{Action: types.ChangeActionUpsert, ResourceRecordSet: rrs})
	}

	// Delete records dropped from the set, sourcing their values from LIVE
	// Route53 state — a delete-by-value is rejected unless it matches the exact
	// canonical record, so prior properties are too brittle (drift / canon).
	live, err := listAllRecordSets(ctx, client, desiredZoneID)
	if err != nil {
		return nil, fmt.Errorf("failed to read live record sets: %w", err)
	}
	liveByKey := indexRecordSetsByKey(live)
	for _, raw := range parsePriorKeys(priorProperties) {
		if desiredSet[raw] {
			continue
		}
		if liveRRS, present := liveByKey[raw]; present {
			changes = append(changes, types.Change{Action: types.ChangeActionDelete, ResourceRecordSet: liveRRS})
		}
	}

	nativeID, err := encodeRecordSetGroupNativeID(desiredZoneID, desiredKeys)
	if err != nil {
		return nil, err
	}

	result, err := client.ChangeResourceRecordSets(ctx, &route53.ChangeResourceRecordSetsInput{
		HostedZoneId: aws.String(desiredZoneID),
		ChangeBatch:  &types.ChangeBatch{Changes: changes},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to update record set group: %w", err)
	}

	return &resource.UpdateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:       resource.OperationUpdate,
			OperationStatus: resource.OperationStatusInProgress,
			RequestID:       aws.ToString(result.ChangeInfo.Id),
			NativeID:        nativeID,
		},
	}, nil
}

// parsePriorKeys extracts the canonical keys of the previously-applied records,
// skipping any malformed entries (prior state is trusted but parsed leniently).
func parsePriorKeys(priorProperties map[string]any) []recordKey {
	raw, ok := priorProperties["RecordSets"].([]any)
	if !ok {
		return nil
	}
	keys := make([]recordKey, 0, len(raw))
	for _, entry := range raw {
		rec, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		name, err := utils.GetStringProperty(rec, "Name")
		if err != nil {
			continue
		}
		recordType, err := utils.GetStringProperty(rec, "Type")
		if err != nil {
			continue
		}
		keys = append(keys, recordKey{Name: canonicalName(name), Type: recordType})
	}
	return keys
}

func (r *RecordSetGroup) Delete(ctx context.Context, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	awsCfg, err := r.cfg.ToAwsConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to load AWS config: %w", err)
	}
	return r.deleteWithClient(ctx, route53.NewFromConfig(awsCfg), request)
}

func (r *RecordSetGroup) deleteWithClient(ctx context.Context, client recordSetGroupClientInterface, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	hostedZoneID, keys, err := decodeRecordSetGroupNativeID(request.NativeID)
	if err != nil {
		return nil, err
	}

	success := &resource.DeleteResult{
		ProgressResult: &resource.ProgressResult{
			Operation:       resource.OperationDelete,
			OperationStatus: resource.OperationStatusSuccess,
			NativeID:        request.NativeID,
		},
	}

	live, err := listAllRecordSets(ctx, client, hostedZoneID)
	if err != nil {
		// The hosted zone is gone (or otherwise unreadable); there is nothing
		// left to delete. Mirrors the singular provisioner treating an absent
		// record as an already-satisfied delete.
		return success, nil
	}
	liveByKey := indexRecordSetsByKey(live)

	changes := make([]types.Change, 0, len(keys))
	for _, key := range keys {
		if liveRRS, present := liveByKey[key]; present {
			changes = append(changes, types.Change{Action: types.ChangeActionDelete, ResourceRecordSet: liveRRS})
		}
	}
	if len(changes) == 0 {
		return success, nil
	}

	result, err := client.ChangeResourceRecordSets(ctx, &route53.ChangeResourceRecordSetsInput{
		HostedZoneId: aws.String(hostedZoneID),
		ChangeBatch:  &types.ChangeBatch{Changes: changes},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to delete record set group: %w", err)
	}

	return &resource.DeleteResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationDelete,
			OperationStatus:    resource.OperationStatusInProgress,
			RequestID:          aws.ToString(result.ChangeInfo.Id),
			NativeID:           request.NativeID,
			ResourceProperties: json.RawMessage{},
		},
	}, nil
}

func (r *RecordSetGroup) Status(ctx context.Context, request *resource.StatusRequest) (*resource.StatusResult, error) {
	awsCfg, err := r.cfg.ToAwsConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to load AWS config: %w", err)
	}
	return r.statusWithClient(ctx, route53.NewFromConfig(awsCfg), request)
}

func (r *RecordSetGroup) statusWithClient(ctx context.Context, client recordSetGroupClientInterface, request *resource.StatusRequest) (*resource.StatusResult, error) {
	result, err := client.GetChange(ctx, &route53.GetChangeInput{Id: aws.String(request.RequestID)})
	if err != nil {
		return nil, fmt.Errorf("failed to get change status: %w", err)
	}

	status := resource.OperationStatusInProgress
	var resourceProperties json.RawMessage
	if result.ChangeInfo.Status == types.ChangeStatusInsync {
		status = resource.OperationStatusSuccess
		if request.NativeID != "" {
			readRes, readErr := r.readWithClient(ctx, client, &resource.ReadRequest{
				NativeID:     request.NativeID,
				ResourceType: request.ResourceType,
				TargetConfig: request.TargetConfig,
			})
			if readErr == nil && readRes != nil && readRes.ErrorCode == "" {
				resourceProperties = json.RawMessage(readRes.Properties)
			}
		}
	}

	return &resource.StatusResult{
		ProgressResult: &resource.ProgressResult{
			OperationStatus:    status,
			RequestID:          aws.ToString(result.ChangeInfo.Id),
			NativeID:           request.NativeID,
			ResourceProperties: resourceProperties,
		},
	}, nil
}

func (r *RecordSetGroup) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	awsCfg, err := r.cfg.ToAwsConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to load AWS config: %w", err)
	}
	return r.readWithClient(ctx, route53.NewFromConfig(awsCfg), request)
}

func (r *RecordSetGroup) readWithClient(ctx context.Context, client recordSetGroupClientInterface, request *resource.ReadRequest) (*resource.ReadResult, error) {
	hostedZoneID, keys, err := decodeRecordSetGroupNativeID(request.NativeID)
	if err != nil {
		return nil, err
	}

	live, err := listAllRecordSets(ctx, client, hostedZoneID)
	if err != nil {
		return &resource.ReadResult{ResourceType: recordSetGroupType, ErrorCode: resource.OperationErrorCodeNotFound}, nil
	}
	liveByKey := indexRecordSetsByKey(live)

	recordSets := make([]map[string]any, 0, len(keys))
	for _, key := range keys {
		if rrs, present := liveByKey[key]; present {
			recordSets = append(recordSets, buildReadProperties(rrs, hostedZoneID, key.Name, key.Type))
		}
	}
	if len(recordSets) == 0 {
		return &resource.ReadResult{ResourceType: recordSetGroupType, ErrorCode: resource.OperationErrorCodeNotFound}, nil
	}

	// The record list is order-insensitive; sort it deterministically so an
	// unchanged group never reads back as drift.
	sort.Slice(recordSets, func(i, j int) bool {
		ni, _ := recordSets[i]["Name"].(string)
		nj, _ := recordSets[j]["Name"].(string)
		if ni != nj {
			return ni < nj
		}
		ti, _ := recordSets[i]["Type"].(string)
		tj, _ := recordSets[j]["Type"].(string)
		return ti < tj
	})

	propBytes, err := json.Marshal(map[string]any{
		"HostedZoneId": hostedZoneID,
		"RecordSets":   recordSets,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to marshal properties: %w", err)
	}

	return &resource.ReadResult{
		ResourceType: recordSetGroupType,
		Properties:   string(propBytes),
	}, nil
}

// List is not supported: RecordSetGroup is a non-discoverable CloudFormation
// grouping abstraction with no AWS-native identity. Returning an explicit error
// (rather than an empty list, which would falsely read as "no resources")
// surfaces the unsupported operation honestly.
func (r *RecordSetGroup) List(_ context.Context, _ *resource.ListRequest) (*resource.ListResult, error) {
	return nil, fmt.Errorf("listing is not supported for %s (discoverable = false)", recordSetGroupType)
}
