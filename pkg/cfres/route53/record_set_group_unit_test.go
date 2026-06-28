// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package route53

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/route53"
	"github.com/aws/aws-sdk-go-v2/service/route53/types"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func recordSetGroupProps(zoneID string, records []map[string]any) json.RawMessage {
	b, _ := json.Marshal(map[string]any{
		"HostedZoneId": zoneID,
		"RecordSets":   records,
	})
	return b
}

func changeOutput(id string) *route53.ChangeResourceRecordSetsOutput {
	return &route53.ChangeResourceRecordSetsOutput{
		ChangeInfo: &types.ChangeInfo{Id: aws.String(id)},
	}
}

func captureChange(target **route53.ChangeResourceRecordSetsInput) func(mock.Arguments) {
	return func(args mock.Arguments) {
		*target = args.Get(1).(*route53.ChangeResourceRecordSetsInput)
	}
}

func changesByAction(changes []types.Change) (upserts, deletes, creates []types.Change) {
	for _, c := range changes {
		switch c.Action {
		case types.ChangeActionUpsert:
			upserts = append(upserts, c)
		case types.ChangeActionDelete:
			deletes = append(deletes, c)
		case types.ChangeActionCreate:
			creates = append(creates, c)
		}
	}
	return
}

// Create batches one CREATE change per declared record and returns the
// ChangeInfo.Id plus a NativeID that round-trips to the managed keys.
func TestRecordSetGroup_Create_BatchesCreateActions(t *testing.T) {
	m := &mockRoute53Client{}
	var captured *route53.ChangeResourceRecordSetsInput
	m.On("ChangeResourceRecordSets", mock.Anything, mock.Anything).
		Run(captureChange(&captured)).
		Return(changeOutput("/change/C123"), nil)

	rsg := &RecordSetGroup{}
	props := recordSetGroupProps("Z123", []map[string]any{
		{"Name": "a.example.com", "Type": "A", "TTL": 300, "ResourceRecords": []string{"192.0.2.1"}},
		{"Name": "txt.example.com", "Type": "TXT", "TTL": 300, "ResourceRecords": []string{`"hello"`}},
	})

	res, err := rsg.createWithClient(context.Background(), m, &resource.CreateRequest{
		ResourceType: recordSetGroupType,
		Properties:   props,
	})
	require.NoError(t, err)
	require.NotNil(t, res.ProgressResult)
	assert.Equal(t, resource.OperationStatusInProgress, res.ProgressResult.OperationStatus)
	assert.Equal(t, "/change/C123", res.ProgressResult.RequestID)

	require.Len(t, captured.ChangeBatch.Changes, 2)
	_, _, creates := changesByAction(captured.ChangeBatch.Changes)
	assert.Len(t, creates, 2, "every change on create should be a CREATE action")

	zone, keys, err := decodeRecordSetGroupNativeID(res.ProgressResult.NativeID)
	require.NoError(t, err)
	assert.Equal(t, "Z123", zone)
	assert.Len(t, keys, 2)
}

// Create supports alias records: an AliasTarget record carries the canonical
// (dotted) DNSName and no ResourceRecords/TTL.
func TestRecordSetGroup_Create_AliasRecord(t *testing.T) {
	m := &mockRoute53Client{}
	var captured *route53.ChangeResourceRecordSetsInput
	m.On("ChangeResourceRecordSets", mock.Anything, mock.Anything).
		Run(captureChange(&captured)).
		Return(changeOutput("/change/C1"), nil)

	rsg := &RecordSetGroup{}
	props := recordSetGroupProps("Z123", []map[string]any{
		{
			"Name": "alias.example.com",
			"Type": "A",
			"AliasTarget": map[string]any{
				"DNSName":              "lb-123.us-east-1.elb.amazonaws.com",
				"HostedZoneId":         "Z35SXDOTRQ7X7K",
				"EvaluateTargetHealth": true,
			},
		},
	})

	_, err := rsg.createWithClient(context.Background(), m, &resource.CreateRequest{Properties: props})
	require.NoError(t, err)

	require.Len(t, captured.ChangeBatch.Changes, 1)
	rrs := captured.ChangeBatch.Changes[0].ResourceRecordSet
	require.NotNil(t, rrs.AliasTarget)
	assert.Equal(t, "lb-123.us-east-1.elb.amazonaws.com.", aws.ToString(rrs.AliasTarget.DNSName),
		"alias DNSName should carry the canonical trailing dot")
	assert.Empty(t, rrs.ResourceRecords)
}

// Update upserts every desired record and deletes the records dropped from the
// set — sourcing the DELETE batch from LIVE Route53 state, not stale prior props.
func TestRecordSetGroup_Update_DiffsAgainstLiveState(t *testing.T) {
	m := &mockRoute53Client{}
	live := &route53.ListResourceRecordSetsOutput{
		ResourceRecordSets: []types.ResourceRecordSet{
			{Name: aws.String("a.example.com."), Type: types.RRTypeA, TTL: aws.Int64(300), ResourceRecords: []types.ResourceRecord{{Value: aws.String("192.0.2.1")}}},
			{Name: aws.String("b.example.com."), Type: types.RRTypeA, TTL: aws.Int64(300), ResourceRecords: []types.ResourceRecord{{Value: aws.String("192.0.2.9")}}},
		},
	}
	m.On("ListResourceRecordSets", mock.Anything, mock.Anything).Return(live, nil)
	var captured *route53.ChangeResourceRecordSetsInput
	m.On("ChangeResourceRecordSets", mock.Anything, mock.Anything).
		Run(captureChange(&captured)).
		Return(changeOutput("/change/C1"), nil)

	rsg := &RecordSetGroup{}
	// prior b carries a DIFFERENT value than live b, proving DELETE sources live state.
	prior := recordSetGroupProps("Z1", []map[string]any{
		{"Name": "a.example.com", "Type": "A", "TTL": 300, "ResourceRecords": []string{"192.0.2.1"}},
		{"Name": "b.example.com", "Type": "A", "TTL": 300, "ResourceRecords": []string{"192.0.2.5"}},
	})
	desired := recordSetGroupProps("Z1", []map[string]any{
		{"Name": "a.example.com", "Type": "A", "TTL": 600, "ResourceRecords": []string{"192.0.2.1"}},
		{"Name": "c.example.com", "Type": "A", "TTL": 300, "ResourceRecords": []string{"192.0.2.3"}},
	})

	res, err := rsg.updateWithClient(context.Background(), m, &resource.UpdateRequest{
		PriorProperties:   prior,
		DesiredProperties: desired,
	})
	require.NoError(t, err)
	assert.Equal(t, resource.OperationStatusInProgress, res.ProgressResult.OperationStatus)

	upserts, deletes, _ := changesByAction(captured.ChangeBatch.Changes)
	var upsertNames, deleteNames []string
	for _, c := range upserts {
		upsertNames = append(upsertNames, aws.ToString(c.ResourceRecordSet.Name))
	}
	for _, c := range deletes {
		deleteNames = append(deleteNames, aws.ToString(c.ResourceRecordSet.Name))
	}
	assert.ElementsMatch(t, []string{"a.example.com.", "c.example.com."}, upsertNames)
	require.Equal(t, []string{"b.example.com."}, deleteNames)
	require.Len(t, deletes[0].ResourceRecordSet.ResourceRecords, 1)
	assert.Equal(t, "192.0.2.9", aws.ToString(deletes[0].ResourceRecordSet.ResourceRecords[0].Value),
		"DELETE should use the live value (192.0.2.9), not the stale prior value (192.0.2.5)")

	// NativeID recomputed from the desired key set.
	_, keys, err := decodeRecordSetGroupNativeID(res.ProgressResult.NativeID)
	require.NoError(t, err)
	assert.Len(t, keys, 2)
}

func TestRecordSetGroup_Update_RejectsHostedZoneChange(t *testing.T) {
	rsg := &RecordSetGroup{}
	prior := recordSetGroupProps("Z1", []map[string]any{{"Name": "a.example.com", "Type": "A", "TTL": 300, "ResourceRecords": []string{"192.0.2.1"}}})
	desired := recordSetGroupProps("Z2", []map[string]any{{"Name": "a.example.com", "Type": "A", "TTL": 300, "ResourceRecords": []string{"192.0.2.1"}}})

	_, err := rsg.updateWithClient(context.Background(), &mockRoute53Client{}, &resource.UpdateRequest{
		PriorProperties:   prior,
		DesiredProperties: desired,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "hosted zone")
}

// Delete reads live state for the managed keys and deletes by live value.
func TestRecordSetGroup_Delete_DeletesFromLiveValues(t *testing.T) {
	m := &mockRoute53Client{}
	live := &route53.ListResourceRecordSetsOutput{
		ResourceRecordSets: []types.ResourceRecordSet{
			{Name: aws.String("a.example.com."), Type: types.RRTypeA, TTL: aws.Int64(300), ResourceRecords: []types.ResourceRecord{{Value: aws.String("192.0.2.1")}}},
			{Name: aws.String("b.example.com."), Type: types.RRTypeA, TTL: aws.Int64(300), ResourceRecords: []types.ResourceRecord{{Value: aws.String("192.0.2.2")}}},
		},
	}
	m.On("ListResourceRecordSets", mock.Anything, mock.Anything).Return(live, nil)
	var captured *route53.ChangeResourceRecordSetsInput
	m.On("ChangeResourceRecordSets", mock.Anything, mock.Anything).
		Run(captureChange(&captured)).
		Return(changeOutput("/change/C1"), nil)

	nativeID, err := encodeRecordSetGroupNativeID("Z1", []recordKey{
		{Name: "a.example.com.", Type: "A"},
		{Name: "b.example.com.", Type: "A"},
	})
	require.NoError(t, err)

	rsg := &RecordSetGroup{}
	res, err := rsg.deleteWithClient(context.Background(), m, &resource.DeleteRequest{NativeID: nativeID})
	require.NoError(t, err)
	assert.Equal(t, resource.OperationStatusInProgress, res.ProgressResult.OperationStatus)

	_, deletes, _ := changesByAction(captured.ChangeBatch.Changes)
	assert.Len(t, deletes, 2, "both managed records should be deleted")
}

// Delete is idempotent: when no managed records remain live, it succeeds
// without issuing a change batch.
func TestRecordSetGroup_Delete_NoopWhenAbsent(t *testing.T) {
	m := &mockRoute53Client{}
	m.On("ListResourceRecordSets", mock.Anything, mock.Anything).
		Return(&route53.ListResourceRecordSetsOutput{}, nil)

	nativeID, err := encodeRecordSetGroupNativeID("Z1", []recordKey{{Name: "a.example.com.", Type: "A"}})
	require.NoError(t, err)

	rsg := &RecordSetGroup{}
	res, err := rsg.deleteWithClient(context.Background(), m, &resource.DeleteRequest{NativeID: nativeID})
	require.NoError(t, err)
	assert.Equal(t, resource.OperationStatusSuccess, res.ProgressResult.OperationStatus)
	m.AssertNotCalled(t, "ChangeResourceRecordSets", mock.Anything, mock.Anything)
}

// Read assembles the managed records into a sorted RecordSets list regardless
// of the order Route53 returns them.
func TestRecordSetGroup_Read_AssemblesAndSorts(t *testing.T) {
	m := &mockRoute53Client{}
	live := &route53.ListResourceRecordSetsOutput{
		ResourceRecordSets: []types.ResourceRecordSet{
			{Name: aws.String("b.example.com."), Type: types.RRTypeA, TTL: aws.Int64(300), ResourceRecords: []types.ResourceRecord{{Value: aws.String("192.0.2.2")}}},
			{Name: aws.String("a.example.com."), Type: types.RRTypeA, TTL: aws.Int64(300), ResourceRecords: []types.ResourceRecord{{Value: aws.String("192.0.2.1")}}},
		},
	}
	m.On("ListResourceRecordSets", mock.Anything, mock.Anything).Return(live, nil)

	nativeID, err := encodeRecordSetGroupNativeID("Z1", []recordKey{
		{Name: "a.example.com.", Type: "A"},
		{Name: "b.example.com.", Type: "A"},
	})
	require.NoError(t, err)

	rsg := &RecordSetGroup{}
	res, err := rsg.readWithClient(context.Background(), m, &resource.ReadRequest{NativeID: nativeID})
	require.NoError(t, err)
	require.Empty(t, res.ErrorCode)

	var props struct {
		HostedZoneId string           `json:"HostedZoneId"`
		RecordSets   []map[string]any `json:"RecordSets"`
	}
	require.NoError(t, json.Unmarshal([]byte(res.Properties), &props))
	assert.Equal(t, "Z1", props.HostedZoneId)
	require.Len(t, props.RecordSets, 2)
	assert.Equal(t, "a.example.com", props.RecordSets[0]["Name"], "records should be sorted by name")
	assert.Equal(t, "b.example.com", props.RecordSets[1]["Name"])
}

func TestRecordSetGroup_Read_NotFoundWhenNonePresent(t *testing.T) {
	m := &mockRoute53Client{}
	m.On("ListResourceRecordSets", mock.Anything, mock.Anything).
		Return(&route53.ListResourceRecordSetsOutput{}, nil)

	nativeID, err := encodeRecordSetGroupNativeID("Z1", []recordKey{{Name: "a.example.com.", Type: "A"}})
	require.NoError(t, err)

	rsg := &RecordSetGroup{}
	res, err := rsg.readWithClient(context.Background(), m, &resource.ReadRequest{NativeID: nativeID})
	require.NoError(t, err)
	assert.Equal(t, resource.OperationErrorCodeNotFound, res.ErrorCode)
}

// A partially-present group returns the records that exist so reconcile can
// re-create the missing ones as drift.
func TestRecordSetGroup_Read_PartialSubset(t *testing.T) {
	m := &mockRoute53Client{}
	live := &route53.ListResourceRecordSetsOutput{
		ResourceRecordSets: []types.ResourceRecordSet{
			{Name: aws.String("a.example.com."), Type: types.RRTypeA, TTL: aws.Int64(300), ResourceRecords: []types.ResourceRecord{{Value: aws.String("192.0.2.1")}}},
		},
	}
	m.On("ListResourceRecordSets", mock.Anything, mock.Anything).Return(live, nil)

	nativeID, err := encodeRecordSetGroupNativeID("Z1", []recordKey{
		{Name: "a.example.com.", Type: "A"},
		{Name: "b.example.com.", Type: "A"},
	})
	require.NoError(t, err)

	rsg := &RecordSetGroup{}
	res, err := rsg.readWithClient(context.Background(), m, &resource.ReadRequest{NativeID: nativeID})
	require.NoError(t, err)
	require.Empty(t, res.ErrorCode)

	var props struct {
		RecordSets []map[string]any `json:"RecordSets"`
	}
	require.NoError(t, json.Unmarshal([]byte(res.Properties), &props))
	assert.Len(t, props.RecordSets, 1)
}

func TestRecordSetGroup_Read_MalformedNativeID(t *testing.T) {
	rsg := &RecordSetGroup{}
	for _, id := range []string{"", "Z1", "Z1|not-base64!!", "|abc"} {
		_, err := rsg.readWithClient(context.Background(), &mockRoute53Client{}, &resource.ReadRequest{NativeID: id})
		assert.Error(t, err, "malformed NativeID %q should error", id)
	}
}

func TestRecordSetGroup_Status_InsyncSuccess(t *testing.T) {
	m := &mockRoute53Client{}
	m.On("GetChange", mock.Anything, mock.Anything).
		Return(&route53.GetChangeOutput{ChangeInfo: &types.ChangeInfo{Id: aws.String("/change/C1"), Status: types.ChangeStatusInsync}}, nil)
	m.On("ListResourceRecordSets", mock.Anything, mock.Anything).
		Return(&route53.ListResourceRecordSetsOutput{
			ResourceRecordSets: []types.ResourceRecordSet{
				{Name: aws.String("a.example.com."), Type: types.RRTypeA, TTL: aws.Int64(300), ResourceRecords: []types.ResourceRecord{{Value: aws.String("192.0.2.1")}}},
			},
		}, nil)

	nativeID, err := encodeRecordSetGroupNativeID("Z1", []recordKey{{Name: "a.example.com.", Type: "A"}})
	require.NoError(t, err)

	rsg := &RecordSetGroup{}
	res, err := rsg.statusWithClient(context.Background(), m, &resource.StatusRequest{RequestID: "/change/C1", NativeID: nativeID})
	require.NoError(t, err)
	assert.Equal(t, resource.OperationStatusSuccess, res.ProgressResult.OperationStatus)
	assert.NotEmpty(t, res.ProgressResult.ResourceProperties)
}

func TestRecordSetGroup_Status_PendingInProgress(t *testing.T) {
	m := &mockRoute53Client{}
	m.On("GetChange", mock.Anything, mock.Anything).
		Return(&route53.GetChangeOutput{ChangeInfo: &types.ChangeInfo{Id: aws.String("/change/C1"), Status: types.ChangeStatusPending}}, nil)

	rsg := &RecordSetGroup{}
	res, err := rsg.statusWithClient(context.Background(), m, &resource.StatusRequest{RequestID: "/change/C1"})
	require.NoError(t, err)
	assert.Equal(t, resource.OperationStatusInProgress, res.ProgressResult.OperationStatus)
}

func TestRecordSetGroup_Create_RejectsUnsupportedRoutingField(t *testing.T) {
	rsg := &RecordSetGroup{}
	props := recordSetGroupProps("Z1", []map[string]any{
		{"Name": "a.example.com", "Type": "A", "TTL": 300, "ResourceRecords": []string{"192.0.2.1"}, "SetIdentifier": "primary", "Weight": 10},
	})
	_, err := rsg.createWithClient(context.Background(), &mockRoute53Client{}, &resource.CreateRequest{Properties: props})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "SetIdentifier")
}

func TestRecordSetGroup_Create_RejectsDuplicateKeys(t *testing.T) {
	rsg := &RecordSetGroup{}
	props := recordSetGroupProps("Z1", []map[string]any{
		{"Name": "a.example.com", "Type": "A", "TTL": 300, "ResourceRecords": []string{"192.0.2.1"}},
		{"Name": "a.example.com.", "Type": "A", "TTL": 300, "ResourceRecords": []string{"192.0.2.2"}},
	})
	_, err := rsg.createWithClient(context.Background(), &mockRoute53Client{}, &resource.CreateRequest{Properties: props})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate")
}

func TestRecordSetGroup_Create_RejectsAliasAndResourceRecords(t *testing.T) {
	rsg := &RecordSetGroup{}
	props := recordSetGroupProps("Z1", []map[string]any{
		{
			"Name":            "a.example.com",
			"Type":            "A",
			"ResourceRecords": []string{"192.0.2.1"},
			"AliasTarget": map[string]any{
				"DNSName":      "lb.example.com",
				"HostedZoneId": "Z2",
			},
		},
	})
	_, err := rsg.createWithClient(context.Background(), &mockRoute53Client{}, &resource.CreateRequest{Properties: props})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mutually exclusive")
}

func TestRecordSetGroup_Create_RejectsRecordWithoutValues(t *testing.T) {
	rsg := &RecordSetGroup{}
	props := recordSetGroupProps("Z1", []map[string]any{
		{"Name": "a.example.com", "Type": "A", "TTL": 300},
	})
	_, err := rsg.createWithClient(context.Background(), &mockRoute53Client{}, &resource.CreateRequest{Properties: props})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resourceRecord")
}

func TestRecordSetGroup_Create_RejectsEmptyGroup(t *testing.T) {
	rsg := &RecordSetGroup{}
	props := recordSetGroupProps("Z1", []map[string]any{})
	_, err := rsg.createWithClient(context.Background(), &mockRoute53Client{}, &resource.CreateRequest{Properties: props})
	require.Error(t, err)
}

func TestRecordSetGroup_Create_RejectsOversizedBatch(t *testing.T) {
	records := make([]map[string]any, maxChangesPerBatch+1)
	for i := range records {
		records[i] = map[string]any{
			"Name":            fmt.Sprintf("r%d.example.com", i),
			"Type":            "A",
			"TTL":             300,
			"ResourceRecords": []string{"192.0.2.1"},
		}
	}
	rsg := &RecordSetGroup{}
	_, err := rsg.createWithClient(context.Background(), &mockRoute53Client{}, &resource.CreateRequest{Properties: recordSetGroupProps("Z1", records)})
	require.Error(t, err)
}

// NativeID encoding canonicalizes names (trailing dot) so encode/decode/match
// are dot-consistent regardless of how the name was declared.
func TestRecordSetGroup_NativeID_RoundTripCanonicalizesNames(t *testing.T) {
	id1, err := encodeRecordSetGroupNativeID("Z1", []recordKey{{Name: "a.example.com", Type: "A"}})
	require.NoError(t, err)
	id2, err := encodeRecordSetGroupNativeID("Z1", []recordKey{{Name: "a.example.com.", Type: "A"}})
	require.NoError(t, err)
	assert.Equal(t, id1, id2, "dotted and dot-less names should encode to the same NativeID")

	zone, keys, err := decodeRecordSetGroupNativeID(id1)
	require.NoError(t, err)
	assert.Equal(t, "Z1", zone)
	require.Len(t, keys, 1)
	assert.Equal(t, "a.example.com.", keys[0].Name)
}

// Encoding is order-insensitive: the same key set in any order yields the same
// NativeID (the list is sorted before encoding).
func TestRecordSetGroup_NativeID_OrderIndependent(t *testing.T) {
	id1, err := encodeRecordSetGroupNativeID("Z1", []recordKey{{Name: "a.example.com.", Type: "A"}, {Name: "b.example.com.", Type: "A"}})
	require.NoError(t, err)
	id2, err := encodeRecordSetGroupNativeID("Z1", []recordKey{{Name: "b.example.com.", Type: "A"}, {Name: "a.example.com.", Type: "A"}})
	require.NoError(t, err)
	assert.Equal(t, id1, id2)
}

func TestRecordSetGroup_List_NotSupported(t *testing.T) {
	rsg := &RecordSetGroup{}
	_, err := rsg.List(context.Background(), &resource.ListRequest{})
	require.Error(t, err, "List must return an explicit error, never an empty (false-empty) list")
}
