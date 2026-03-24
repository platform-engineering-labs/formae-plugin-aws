// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build integration

package route53

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/route53"
	r53types "github.com/aws/aws-sdk-go-v2/service/route53/types"
	"github.com/google/uuid"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/ccx"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/config"
	"github.com/stretchr/testify/assert"
)

// uniqueDomain generates a unique domain name for test isolation.
// Uses a short UUID prefix under example.com to avoid conflicts between
// concurrent or repeated test runs.
func uniqueDomain(prefix string) string {
	short := strings.ReplaceAll(uuid.New().String()[:8], "-", "")
	return fmt.Sprintf("formae-test-%s-%s.example.com", prefix, short)
}

// createTestHostedZoneWithCleanup creates a Route53 hosted zone and registers
// a t.Cleanup function to delete it when the test finishes, even on failure.
// Returns the hosted zone ID.
func createTestHostedZoneWithCleanup(t *testing.T, zoneName string) string {
	t.Helper()

	hostedZoneID, err := createTestHostedZone(zoneName)
	if err != nil {
		t.Fatalf("Failed to create test hosted zone %q: %v", zoneName, err)
	}
	t.Logf("Created hosted zone %q with ID: %s", zoneName, hostedZoneID)

	t.Cleanup(func() {
		t.Logf("Cleaning up hosted zone %s (%s)", hostedZoneID, zoneName)
		deleteTestHostedZone(t, hostedZoneID)
	})

	return hostedZoneID
}

func createTestHostedZone(zoneName string) (string, error) {
	cfg, err := awsconfig.LoadDefaultConfig(context.Background())
	if err != nil {
		return "", fmt.Errorf("failed to load AWS config: %w", err)
	}
	client := route53.NewFromConfig(cfg)

	callerRef := uuid.New().String()
	out, err := client.CreateHostedZone(context.Background(), &route53.CreateHostedZoneInput{
		Name:            &zoneName,
		CallerReference: &callerRef,
	})
	if err != nil {
		return "", fmt.Errorf("create failed: %w", err)
	}
	// Route53 returns IDs like "/hostedzone/Z1234", extract just the ID
	hostedZoneID := strings.TrimPrefix(*out.HostedZone.Id, "/hostedzone/")
	return hostedZoneID, nil
}

func deleteTestHostedZone(t *testing.T, hostedZoneID string) {
	t.Helper()

	cfg, err := awsconfig.LoadDefaultConfig(context.Background())
	if err != nil {
		t.Logf("Warning: failed to load AWS config for cleanup: %v", err)
		return
	}
	client := route53.NewFromConfig(cfg)

	// List and delete all non-NS/SOA record sets first
	fullID := "/hostedzone/" + hostedZoneID
	listOut, err := client.ListResourceRecordSets(context.Background(), &route53.ListResourceRecordSetsInput{
		HostedZoneId: &fullID,
	})
	if err != nil {
		t.Logf("Warning: failed to list record sets for cleanup: %v", err)
		return
	}

	var changes []r53types.Change
	for _, rs := range listOut.ResourceRecordSets {
		if rs.Type == r53types.RRTypeNs || rs.Type == r53types.RRTypeSoa {
			continue
		}
		changes = append(changes, r53types.Change{
			Action:            r53types.ChangeActionDelete,
			ResourceRecordSet: &rs,
		})
	}
	if len(changes) > 0 {
		_, err = client.ChangeResourceRecordSets(context.Background(), &route53.ChangeResourceRecordSetsInput{
			HostedZoneId: &fullID,
			ChangeBatch:  &r53types.ChangeBatch{Changes: changes},
		})
		if err != nil {
			t.Logf("Warning: failed to delete record sets: %v", err)
		}
	}

	_, err = client.DeleteHostedZone(context.Background(), &route53.DeleteHostedZoneInput{
		Id: &fullID,
	})
	if err != nil {
		t.Logf("Warning: failed to delete hosted zone: %v", err)
	}
}

func create_record_set(rs RecordSet, propsBytes json.RawMessage, t *testing.T) resource.CreateResult {
	createRes, err := rs.Create(context.Background(), &resource.CreateRequest{
		ResourceType: "AWS::Route53::RecordSet",
		Label:        "pel-record-resolvable",
		Properties:   propsBytes,
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	if createRes == nil || createRes.ProgressResult == nil || createRes.ProgressResult.RequestID == "" {
		t.Fatalf("Create did not return a valid RequestID")
	}
	return *createRes
}

func wait_for_status(rs RecordSet, requestID string, nativeID string, t *testing.T) *resource.StatusResult {
	t.Helper()
	deadline := time.After(2 * time.Minute)
	for {
		select {
		case <-deadline:
			t.Fatalf("Timed out waiting for status on request %s", requestID)
		default:
		}
		statusRes, err := rs.Status(context.Background(), &resource.StatusRequest{
			RequestID:    requestID,
			NativeID:     nativeID,
			ResourceType: "AWS::Route53::RecordSet",
		})
		if err != nil {
			t.Fatalf("Status failed: %v", err)
		}
		if statusRes.ProgressResult.OperationStatus == resource.OperationStatusSuccess {
			return statusRes
		}
		time.Sleep(2 * time.Second)
	}
}

func delete_record_set(rs RecordSet, nativeID string, t *testing.T) *resource.DeleteResult {
	deleteReq := &resource.DeleteRequest{
		NativeID:     nativeID,
		ResourceType: "AWS::Route53::RecordSet",
	}
	deleteRes, err := rs.Delete(context.Background(), deleteReq)
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
	if deleteRes == nil || deleteRes.ProgressResult == nil || deleteRes.ProgressResult.RequestID == "" {
		t.Fatalf("Delete did not return a valid RequestID")
	}
	return deleteRes
}

func TestRecordSet_Lifecycle(t *testing.T) {
	domain := uniqueDomain("lifecycle")
	hostedZoneID := createTestHostedZoneWithCleanup(t, domain)

	cfg := &config.Config{}
	rs := RecordSet{cfg: cfg}

	// --- CREATE ---
	createProps := map[string]any{
		"HostedZoneId":    hostedZoneID,
		"Name":            "eng." + domain,
		"ResourceRecords": []string{"192.168.55.2"},
		"TTL":             300,
		"Type":            "A",
	}
	createPropsBytes, _ := json.Marshal(createProps)

	createRes, err := rs.Create(context.Background(), &resource.CreateRequest{
		ResourceType: "AWS::Route53::RecordSet",
		Label:        "pel-record-snarf",
		Properties:   createPropsBytes,
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	if createRes == nil || createRes.ProgressResult == nil || createRes.ProgressResult.RequestID == "" {
		t.Fatalf("Create did not return a valid RequestID")
	}

	nativeID := createRes.ProgressResult.NativeID

	// Wait for create to be INSYNC
	statusRes := wait_for_status(rs, createRes.ProgressResult.RequestID, nativeID, t)
	_ = statusRes

	// --- READ after create ---
	readRes, err := rs.Read(context.Background(), &resource.ReadRequest{
		NativeID: nativeID,
	})
	if err != nil {
		t.Fatalf("Read after create failed: %v", err)
	}
	t.Logf("Created record: %s", readRes.Properties)

	// --- UPDATE ---
	updateProps := map[string]any{
		"HostedZoneId":    hostedZoneID,
		"Name":            "eng." + domain,
		"ResourceRecords": []string{"192.168.55.3"}, // change IP
		"TTL":             600,                      // change TTL
		"Type":            "A",
	}
	updatePropsBytes, _ := json.Marshal(updateProps)

	updateRes, err := rs.Update(context.Background(), &resource.UpdateRequest{
		PriorProperties:   createPropsBytes,
		DesiredProperties: updatePropsBytes,
	})
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}
	if updateRes == nil || updateRes.ProgressResult == nil || updateRes.ProgressResult.RequestID == "" {
		t.Fatalf("Update did not return a valid RequestID")
	}

	// Wait for update to be INSYNC
	wait_for_status(rs, updateRes.ProgressResult.RequestID, nativeID, t)

	// --- READ after update ---
	readRes, err = rs.Read(context.Background(), &resource.ReadRequest{
		NativeID: nativeID,
	})
	if err != nil {
		t.Fatalf("Read after update failed: %v", err)
	}
	t.Logf("Updated record: %s", readRes.Properties)

	// --- DELETE record set ---
	deleteReq := &resource.DeleteRequest{
		NativeID: nativeID,
	}
	deleteRes, err := rs.Delete(context.Background(), deleteReq)
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
	if deleteRes == nil || deleteRes.ProgressResult == nil || deleteRes.ProgressResult.RequestID == "" {
		t.Fatalf("Delete did not return a valid RequestID")
	}

	// Wait for delete to be INSYNC
	wait_for_status(rs, deleteRes.ProgressResult.RequestID, nativeID, t)

	// Hosted zone cleanup is handled by t.Cleanup via createTestHostedZoneWithCleanup
}

func TestCreate_A_Record(t *testing.T) {
	domain := uniqueDomain("a-record")
	hostedZoneID := createTestHostedZoneWithCleanup(t, domain)

	cfg := &config.Config{}
	rs := RecordSet{cfg: cfg}

	props := map[string]any{
		"HostedZoneId":    hostedZoneID,
		"Name":            "a." + domain,
		"TTL":             "300",
		"Type":            "A",
		"ResourceRecords": []string{"192.168.1.1"},
	}

	propsBytes, _ := json.Marshal(props)

	t.Log("Creating A record set with properties:", string(propsBytes))
	createRes := create_record_set(rs, propsBytes, t)
	nativeID := createRes.ProgressResult.NativeID
	t.Log("Created A record set with RequestID:", createRes.ProgressResult.RequestID)

	t.Log("Waiting for create to finish")
	wait_for_status(rs, createRes.ProgressResult.RequestID, nativeID, t)
	t.Log("Create completed for A record set")

	t.Log("Deleting A record set")
	deleteRes := delete_record_set(rs, nativeID, t)
	t.Log("Delete RequestID:", deleteRes.ProgressResult.RequestID)

	t.Log("Waiting for delete to finish")
	wait_for_status(rs, deleteRes.ProgressResult.RequestID, nativeID, t)
	t.Log("Deleted A record set")
}

// TestCreate_Records tests creation and deletion for all supported Route53 record types.
func TestCreate_Records(t *testing.T) {
	domain := uniqueDomain("records")
	hostedZoneID := createTestHostedZoneWithCleanup(t, domain)

	cfg := &config.Config{}
	rs := RecordSet{cfg: cfg}

	testCases := []struct {
		name  string
		props map[string]any
	}{
		{
			name: "A record",
			props: map[string]any{
				"HostedZoneId":    hostedZoneID,
				"Name":            "a." + domain,
				"TTL":             "300",
				"Type":            "A",
				"ResourceRecords": []string{"192.168.1.1"},
			},
		},
		{
			name: "AAAA record",
			props: map[string]any{
				"HostedZoneId":    hostedZoneID,
				"Name":            "aaaa." + domain,
				"TTL":             "300",
				"Type":            "AAAA",
				"ResourceRecords": []string{"2001:db8::1"},
			},
		},
		{
			name: "CNAME record",
			props: map[string]any{
				"HostedZoneId":    hostedZoneID,
				"Name":            "cname." + domain,
				"TTL":             "300",
				"Type":            "CNAME",
				"ResourceRecords": []string{"target.example.com."},
			},
		},
		{
			name: "MX record",
			props: map[string]any{
				"HostedZoneId":    hostedZoneID,
				"Name":            "mx." + domain,
				"TTL":             "300",
				"Type":            "MX",
				"ResourceRecords": []string{"10 mail1.example.com.", "20 mail2.example.com."},
			},
		},
		{
			name: "TXT record",
			props: map[string]any{
				"HostedZoneId":    hostedZoneID,
				"Name":            "txt." + domain,
				"TTL":             "300",
				"Type":            "TXT",
				"ResourceRecords": []string{`"v=spf1 include:example.com ~all"`},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			propsBytes, _ := json.Marshal(tc.props)

			t.Log("Creating", tc.name, "with properties:", string(propsBytes))
			createRes := create_record_set(rs, propsBytes, t)
			nativeID := createRes.ProgressResult.NativeID
			t.Log("Created", tc.name, "with RequestID:", createRes.ProgressResult.RequestID)

			t.Log("Waiting for create to finish")
			wait_for_status(rs, createRes.ProgressResult.RequestID, nativeID, t)
			t.Log("Create completed for", tc.name)

			t.Log("Deleting", tc.name)
			deleteRes := delete_record_set(rs, nativeID, t)
			t.Log("Delete RequestID:", deleteRes.ProgressResult.RequestID)

			t.Log("Waiting for delete to finish")
			wait_for_status(rs, deleteRes.ProgressResult.RequestID, nativeID, t)
			t.Log("Deleted", tc.name)
		})
	}
}

func TestRecordSet_ListRecordSets(t *testing.T) {
	domain := uniqueDomain("list")
	hostedZoneID := createTestHostedZoneWithCleanup(t, domain)

	cfg := &config.Config{}
	rs := RecordSet{cfg: cfg}

	// create two recordsets in AWS (a hosted zone comes with two default recordsets)
	nativeID1 := createRecordSet(t, rs, hostedZoneID, "eng1."+domain, []string{"192.168.55.2"}, "300", "A")
	nativeID2 := createRecordSet(t, rs, hostedZoneID, "eng2."+domain, []string{"192.168.55.3"}, "300", "A")

	t.Cleanup(func() {
		// delete the record sets before the hosted zone cleanup runs
		deleteRecordSet(t, rs, hostedZoneID, nativeID1, "eng1."+domain, []string{"192.168.55.2"}, "300", "A")
		deleteRecordSet(t, rs, hostedZoneID, nativeID2, "eng2."+domain, []string{"192.168.55.3"}, "300", "A")
	})

	var pageSize int32 = 2

	// expect 2 pages of 2 recordsets each
	firstPage, err := rs.List(context.Background(), &resource.ListRequest{
		ResourceType: "AWS::Route53::RecordSet",
		PageSize:     pageSize,
		AdditionalProperties: map[string]string{
			"HostedZoneId": hostedZoneID,
		},
	})

	assert.NoError(t, err)
	assert.Len(t, firstPage.NativeIDs, 2)
	assert.NotNil(t, firstPage.NextPageToken)

	secondPage, err := rs.List(context.Background(), &resource.ListRequest{
		ResourceType: "AWS::Route53::RecordSet",
		PageSize:     pageSize,
		PageToken:    firstPage.NextPageToken,
		AdditionalProperties: map[string]string{
			"HostedZoneId": hostedZoneID,
		},
	})

	assert.NoError(t, err)
	assert.Len(t, secondPage.NativeIDs, 2)
	assert.Nil(t, secondPage.NextPageToken)
}

func createRecordSet(t *testing.T, rs RecordSet, hostedZoneID string, name string, records []string, ttl string, recordType string) string {
	props := map[string]any{
		"HostedZoneId":    hostedZoneID,
		"Name":            name,
		"ResourceRecords": records,
		"TTL":             ttl,
		"Type":            recordType,
	}
	propsBytes, _ := json.Marshal(props)

	createRes, err := rs.Create(context.Background(), &resource.CreateRequest{
		ResourceType: "AWS::Route53::RecordSet",
		Label:        fmt.Sprintf("pel-record-snarf-%s", uuid.New().String()),
		Properties:   propsBytes,
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	if createRes == nil || createRes.ProgressResult == nil || createRes.ProgressResult.RequestID == "" {
		t.Fatalf("Create did not return a valid RequestID")
	}

	nativeID := createRes.ProgressResult.NativeID
	var statusRes *resource.StatusResult
	assert.Eventually(t, func() bool {
		statusRes, err = rs.Status(context.Background(), &resource.StatusRequest{
			RequestID:    createRes.ProgressResult.RequestID,
			NativeID:     nativeID,
			ResourceType: "AWS::Route53::RecordSet",
		})
		if err != nil {
			t.Fatalf("Status (create) failed: %v", err)
		}
		return statusRes.ProgressResult.OperationStatus == resource.OperationStatusSuccess
	}, 2*time.Minute, 500*time.Millisecond)

	return nativeID
}

func deleteRecordSet(t *testing.T, rs RecordSet, hostedZoneID string, nativeID, name string, records []string, ttl string, recordType string) {
	req := &resource.DeleteRequest{
		NativeID:     nativeID,
		ResourceType: "AWS::Route53::RecordSet",
	}
	deleteRes, err := rs.Delete(context.Background(), req)
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
	if deleteRes == nil || deleteRes.ProgressResult == nil || deleteRes.ProgressResult.RequestID == "" {
		t.Fatalf("Delete did not return a valid RequestID")
	}

	var statusRes *resource.StatusResult
	assert.Eventually(t, func() bool {
		statusRes, err = rs.Status(context.Background(), &resource.StatusRequest{
			RequestID:    deleteRes.ProgressResult.RequestID,
			NativeID:     nativeID,
			ResourceType: "AWS::Route53::RecordSet",
		})
		if err != nil {
			t.Fatalf("Status (delete) failed: %v", err)
		}

		return statusRes.ProgressResult.OperationStatus == resource.OperationStatusSuccess
	}, 2*time.Minute, 500*time.Millisecond)

	t.Logf("Delete completed: %+v", statusRes.ProgressResult)
}
