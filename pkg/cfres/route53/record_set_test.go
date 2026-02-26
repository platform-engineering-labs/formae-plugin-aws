// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build integration

package route53

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/google/uuid"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/ccx"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/config"
	"github.com/stretchr/testify/assert"
)

func createTestHostedZone(zoneName string) (string, error) {
	cfg := &config.Config{}

	client, err := ccx.NewClient(cfg)
	if err != nil {
		return "", fmt.Errorf("failed to create client: %w", err)
	}

	props := map[string]any{
		"Name": zoneName,
	}
	propsBytes, _ := json.Marshal(props)

	createReq := &resource.CreateRequest{
		ResourceType: "AWS::Route53::HostedZone",
		Properties:   propsBytes,
	}

	createRes, err := client.CreateResource(context.Background(), createReq)
	if err != nil {
		return "", fmt.Errorf("create failed: %w", err)
	}
	if createRes == nil || createRes.ProgressResult == nil || createRes.ProgressResult.RequestID == "" {
		return "", fmt.Errorf("create did not return a valid request ID")
	}

	for i := 0; i < 20; i++ {
		statusRes, err := client.StatusResource(context.Background(), &resource.StatusRequest{
			RequestID: createRes.ProgressResult.RequestID,
		}, func(ctx context.Context, req *resource.ReadRequest) (*resource.ReadResult, error) {
			return client.ReadResource(ctx, req)
		})
		if err != nil {
			return "", fmt.Errorf("status failed: %w", err)
		}
		if statusRes.ProgressResult.OperationStatus == resource.OperationStatusSuccess {
			return statusRes.ProgressResult.NativeID, nil
		}
		time.Sleep(3 * time.Second)
	}
	return "", fmt.Errorf("timeout waiting for hosted zone creation")
}

func deleteTestHostedZone(t *testing.T, hostedZoneID string) {
	cfg := &config.Config{}

	client, err := ccx.NewClient(cfg)
	assert.NoError(t, err)

	deleteReq := &resource.DeleteRequest{
		NativeID:     hostedZoneID,
		ResourceType: "AWS::Route53::HostedZone",
	}

	deleteRes, err := client.DeleteResource(context.Background(), deleteReq)
	assert.NoError(t, err)

	if deleteRes == nil || deleteRes.ProgressResult == nil || deleteRes.ProgressResult.RequestID == "" {
		t.Fatalf("Delete did not return a valid RequestID")
	}

	// Wait for deletion to complete
	assert.Eventually(t, func() bool {
		statusRes, err := client.StatusResource(context.Background(), &resource.StatusRequest{
			RequestID: deleteRes.ProgressResult.RequestID,
		}, func(ctx context.Context, req *resource.ReadRequest) (*resource.ReadResult, error) {
			return client.ReadResource(ctx, req)
		})
		if err != nil {
			t.Fatalf("Status failed: %v", err)
		}
		return statusRes.ProgressResult.OperationStatus == resource.OperationStatusSuccess
	}, 2*time.Minute, 5*time.Second, "Timed out waiting for hosted zone deletion")
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

func TestCreate_Route53(t *testing.T) {
	t.Skip("Skipping Route53 create test - WIP")
	cfg := &config.Config{}
	rs := RecordSet{cfg: cfg}

	res, err := rs.Create(context.Background(), &resource.CreateRequest{
		ResourceType: "AWS::Route53::RecordSet",
		Label:        "pel-record-snarf",
		Properties:   json.RawMessage(`{"HostedZoneId":"Z03405173PGMODHWMP57N","Name":"eng.snarf.test.pel","ResourceRecords":["192.168.55.2"],"TTL":"300","Type":"A"}`),
	})

	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// Wait for status to be success
	if res == nil || res.ProgressResult == nil || res.ProgressResult.RequestID == "" {
		t.Fatalf("Create did not return a valid RequestID")
	}
	nativeID := res.ProgressResult.NativeID
	wait_for_status(rs, res.ProgressResult.RequestID, nativeID, t)

	_, err = rs.Read(context.Background(), &resource.ReadRequest{
		NativeID: nativeID,
	})
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}

	deleteReq := &resource.DeleteRequest{
		NativeID:     nativeID,
		ResourceType: "AWS::Route53::RecordSet",
	}
	_, err = rs.Delete(context.Background(), deleteReq)
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
}

func TestDelete_Route53(t *testing.T) {
	t.Skip("Skipping Route53 delete test - WIP")
	cfg := &config.Config{}
	rs := RecordSet{cfg: cfg}

	nativeID := "Z034"
	deleteReq := &resource.DeleteRequest{
		NativeID: nativeID,
	}
	_, err := rs.Delete(context.Background(), deleteReq)
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
}

func TestStatus_Route53(t *testing.T) {
	t.Skip("Skipping Route53 status test - WIP")
	_, err := awsconfig.LoadDefaultConfig(context.Background())
	assert.NoError(t, err)

	cfg := &config.Config{}
	rs := RecordSet{cfg: cfg}

	_, err = rs.Status(context.Background(), &resource.StatusRequest{
		RequestID:    "/change/C0143912BN0L1VGPGYWI",
		ResourceType: "AWS::Route53::RecordSet",
	})
	if err != nil {
		t.Fatalf("Status failed: %v", err)
	}
}

func TestRecordSet_Lifecycle(t *testing.T) {
	//t.Skip("Skipping Route53 record set lifecycle test - WIP")
	hostedZoneID, err := createTestHostedZone("snarf.test.pel")
	if err != nil {
		t.Fatalf("Failed to create test hosted zone: %v", err)
		return
	}
	t.Logf("Created hosted zone with ID: %s", hostedZoneID)
	cfg := &config.Config{}
	rs := RecordSet{cfg: cfg}

	// --- CREATE ---
	createProps := map[string]any{
		"HostedZoneId":    hostedZoneID,
		"Name":            "eng.snarf.test.pel",
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
		"Name":            "eng.snarf.test.pel",
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

	// --- DELETE ---
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

	deleteTestHostedZone(t, hostedZoneID)
}

func TestCreate_A_Record(t *testing.T) {
	hostedZoneID, err := createTestHostedZone("a-record.test.pel")
	if err != nil {
		t.Fatalf("Failed to create test hosted zone: %v", err)
	}
	defer deleteTestHostedZone(t, hostedZoneID)

	cfg := &config.Config{}
	rs := RecordSet{cfg: cfg}

	props := map[string]any{
		"HostedZoneId":    hostedZoneID,
		"Name":            "a.a-record.test.pel",
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
	domain := "records.test.pel"
	hostedZoneID, err := createTestHostedZone(domain)
	if err != nil {
		t.Fatalf("Failed to create test hosted zone: %v", err)
	}
	defer deleteTestHostedZone(t, hostedZoneID)

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

func TestCreate_RecordSet_2(t *testing.T) {
	t.Skip("Skipping Route53 record set test - WIP")
	cfg := &config.Config{}
	rs := RecordSet{cfg: cfg}

	// This simulates a record set with a $ref and $value for HostedZoneId
	props := map[string]any{
		"AliasTarget": map[string]any{
			"DNSName":              "d123456abcdef8.cloudfront.net",
			"EvaluateTargetHealth": false,
			"HostedZoneId":         "Z2FDTNDATAQYW2",
		},
		"HostedZoneId": "Z07395323V6QPG5XX24K3",
		"Name":         "test.platform.engineering",
		"Type":         "A",
	}
	propsBytes, _ := json.Marshal(props)

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

	nativeID := createRes.ProgressResult.NativeID

	// Wait for create to be INSYNC
	wait_for_status(rs, createRes.ProgressResult.RequestID, nativeID, t)

	// --- READ after create ---
	_, err = rs.Read(context.Background(), &resource.ReadRequest{
		NativeID: nativeID,
	})
	if err != nil {
		t.Fatalf("Read after create failed: %v", err)
	}

	// --- DELETE ---
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

	// Wait for delete to be INSYNC
	wait_for_status(rs, deleteRes.ProgressResult.RequestID, nativeID, t)
}

func TestRecordSet_ListRecordSets(t *testing.T) {
	hostedZoneID, err := createTestHostedZone("snarf.test.pel")
	assert.NoError(t, err)
	cfg := &config.Config{}
	rs := RecordSet{cfg: cfg}

	// create two recordsets in AWS (a hosted zone comes with two default recordsets)
	nativeID1 := createRecordSet(t, rs, hostedZoneID, "eng1.snarf.test.pel", []string{"192.168.55.2"}, "300", "A")
	nativeID2 := createRecordSet(t, rs, hostedZoneID, "eng2.snarf.test.pel", []string{"192.168.55.3"}, "300", "A")

	defer func() {
		// delete the record sets
		deleteRecordSet(t, rs, hostedZoneID, nativeID1, "eng1.snarf.test.pel", []string{"192.168.55.2"}, "300", "A")
		deleteRecordSet(t, rs, hostedZoneID, nativeID2, "eng2.snarf.test.pel", []string{"192.168.55.3"}, "300", "A")

		// delete hosted zone
		deleteTestHostedZone(t, hostedZoneID)
	}()

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
