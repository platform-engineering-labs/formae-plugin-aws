// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package ecs

import (
	"testing"
	"time"

	awsecs "github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/stretchr/testify/assert"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
)

func TestParseComposite_ValidCreate(t *testing.T) {
	op, unixStart, ccapiToken, ok := parseComposite("formae-ecs/create/1747526400/f470d40b-d23c-4d3a")
	assert.True(t, ok)
	assert.Equal(t, resource.OperationCreate, op)
	assert.Equal(t, int64(1747526400), unixStart)
	assert.Equal(t, "f470d40b-d23c-4d3a", ccapiToken)
}

func TestParseComposite_ValidUpdate(t *testing.T) {
	op, unixStart, ccapiToken, ok := parseComposite("formae-ecs/update/1747526400/abc-123")
	assert.True(t, ok)
	assert.Equal(t, resource.OperationUpdate, op)
	assert.Equal(t, int64(1747526400), unixStart)
	assert.Equal(t, "abc-123", ccapiToken)
}

func TestParseComposite_BareCCAPIToken(t *testing.T) {
	// CCAPI-shaped UUID without our prefix — this is the normal path for
	// CODE_DEPLOY/EXTERNAL/DAEMON shapes that bypass our wrap.
	_, _, _, ok := parseComposite("f470d40b-d23c-4d3a-9c11-some-uuid")
	assert.False(t, ok)
}

func TestParseComposite_EmptyString(t *testing.T) {
	_, _, _, ok := parseComposite("")
	assert.False(t, ok)
}

func TestParseComposite_MalformedUnix(t *testing.T) {
	_, _, _, ok := parseComposite("formae-ecs/create/not-a-number/abc")
	assert.False(t, ok)
}

func TestParseComposite_UnknownOp(t *testing.T) {
	_, _, _, ok := parseComposite("formae-ecs/delete/1747526400/abc")
	assert.False(t, ok)
}

func TestComposeRequestID_Create(t *testing.T) {
	s := composeRequestID(opSegCreate, 1747526400, "ccapi-token")
	assert.Equal(t, "formae-ecs/create/1747526400/ccapi-token", s)
}

func TestComposeRequestID_Update(t *testing.T) {
	s := composeRequestID(opSegUpdate, 1747526400, "ccapi-token")
	assert.Equal(t, "formae-ecs/update/1747526400/ccapi-token", s)
}

func TestComposite_RoundTrip(t *testing.T) {
	encoded := composeRequestID(opSegCreate, 1747526400, "tA")
	op, unixStart, token, ok := parseComposite(encoded)
	assert.True(t, ok)
	assert.Equal(t, resource.OperationCreate, op)
	assert.Equal(t, int64(1747526400), unixStart)
	assert.Equal(t, "tA", token)
}

func TestParseClusterAndServiceFromNativeID_Synthetic(t *testing.T) {
	cluster, service, ok := parseClusterAndServiceFromNativeID("pending|my-cluster|my-svc")
	assert.True(t, ok)
	assert.Equal(t, "my-cluster", cluster)
	assert.Equal(t, "my-svc", service)
}

func TestParseClusterAndServiceFromNativeID_Canonical(t *testing.T) {
	cluster, service, ok := parseClusterAndServiceFromNativeID(
		"arn:aws:ecs:us-east-1:123456789012:service/my-cluster/my-svc|my-cluster")
	assert.True(t, ok)
	assert.Equal(t, "my-cluster", cluster)
	assert.Equal(t, "my-svc", service)
}

func TestParseClusterAndServiceFromNativeID_Empty(t *testing.T) {
	_, _, ok := parseClusterAndServiceFromNativeID("")
	assert.False(t, ok)
}

func TestParseClusterAndServiceFromNativeID_Malformed(t *testing.T) {
	_, _, ok := parseClusterAndServiceFromNativeID("not-a-real-shape")
	assert.False(t, ok)
}

func TestParseClusterAndServiceFromNativeID_SyntheticEmptyCluster(t *testing.T) {
	_, _, ok := parseClusterAndServiceFromNativeID("pending||my-svc")
	assert.False(t, ok)
}

func TestBuildCanonicalNativeID(t *testing.T) {
	canonical := buildCanonicalNativeID(
		"arn:aws:ecs:us-east-1:123456789012:service/my-cluster/my-svc",
		"my-cluster")
	assert.Equal(t, "arn:aws:ecs:us-east-1:123456789012:service/my-cluster/my-svc|my-cluster", canonical)
}

func TestShapeSupportsPhaseB_DefaultShape(t *testing.T) {
	// Empty JSON object — all fields absent = all defaults (ECS controller, REPLICA strategy)
	assert.True(t, shapeSupportsPhaseB([]byte(`{}`)))
}

func TestShapeSupportsPhaseB_ExplicitREPLICA_ECS(t *testing.T) {
	props := []byte(`{
        "Cluster": "c", "ServiceName": "s",
        "DeploymentController": {"Type": "ECS"},
        "SchedulingStrategy": "REPLICA"
    }`)
	assert.True(t, shapeSupportsPhaseB(props))
}

func TestShapeSupportsPhaseB_CODE_DEPLOY(t *testing.T) {
	props := []byte(`{"DeploymentController": {"Type": "CODE_DEPLOY"}}`)
	assert.False(t, shapeSupportsPhaseB(props))
}

func TestShapeSupportsPhaseB_EXTERNAL(t *testing.T) {
	props := []byte(`{"DeploymentController": {"Type": "EXTERNAL"}}`)
	assert.False(t, shapeSupportsPhaseB(props))
}

func TestShapeSupportsPhaseB_DAEMON(t *testing.T) {
	props := []byte(`{"SchedulingStrategy": "DAEMON"}`)
	assert.False(t, shapeSupportsPhaseB(props))
}

func TestShapeSupportsPhaseB_EmptyProps(t *testing.T) {
	// No properties at all → conservatively false; let the generic path handle it.
	assert.False(t, shapeSupportsPhaseB(nil))
	assert.False(t, shapeSupportsPhaseB([]byte{}))
}

func TestParseCreateClusterAndService_FullArn(t *testing.T) {
	props := []byte(`{
        "Cluster": "arn:aws:ecs:us-east-1:123456789012:cluster/my-cluster",
        "ServiceName": "my-svc"
    }`)
	cluster, service, err := parseCreateClusterAndService(props)
	assert.NoError(t, err)
	assert.Equal(t, "my-cluster", cluster)
	assert.Equal(t, "my-svc", service)
}

func TestParseCreateClusterAndService_ShortName(t *testing.T) {
	props := []byte(`{"Cluster": "my-cluster", "ServiceName": "my-svc"}`)
	cluster, service, err := parseCreateClusterAndService(props)
	assert.NoError(t, err)
	assert.Equal(t, "my-cluster", cluster)
	assert.Equal(t, "my-svc", service)
}

func TestParseCreateClusterAndService_MissingServiceName(t *testing.T) {
	props := []byte(`{"Cluster": "my-cluster"}`)
	_, _, err := parseCreateClusterAndService(props)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "ServiceName")
}

func TestParseCreateClusterAndService_MissingCluster_DefaultsToDefaultCluster(t *testing.T) {
	// Schema marks Cluster as optional. When absent, ECS places the service in
	// a cluster literally named "default". Regression coverage for the gap the
	// adversarial review caught: pre-this-branch the generic CCAPI path handled
	// this transparently, so Phase B must mirror that semantic.
	props := []byte(`{"ServiceName": "my-svc"}`)
	cluster, service, err := parseCreateClusterAndService(props)
	assert.NoError(t, err)
	assert.Equal(t, "default", cluster)
	assert.Equal(t, "my-svc", service)
}

func TestParseUpdateClusterAndService_Canonical(t *testing.T) {
	cluster, service, err := parseUpdateClusterAndService(
		"arn:aws:ecs:us-east-1:123456789012:service/my-cluster/my-svc|my-cluster")
	assert.NoError(t, err)
	assert.Equal(t, "my-cluster", cluster)
	assert.Equal(t, "my-svc", service)
}

func TestParseUpdateClusterAndService_Malformed(t *testing.T) {
	_, _, err := parseUpdateClusterAndService("garbage")
	assert.Error(t, err)
}

func TestWrapForCreate_AsyncInProgress(t *testing.T) {
	s := &Service{now: func() time.Time { return time.Unix(1747526400, 0) }}
	pr := &resource.ProgressResult{
		Operation:       resource.OperationCreate,
		OperationStatus: resource.OperationStatusInProgress,
		RequestID:       "ccapi-tA",
		NativeID:        "",
	}
	s.wrapForCreate(pr, "my-cluster", "my-svc")
	assert.Equal(t, resource.OperationCreate, pr.Operation)
	assert.Equal(t, resource.OperationStatusInProgress, pr.OperationStatus)
	assert.Equal(t, "formae-ecs/create/1747526400/ccapi-tA", pr.RequestID)
	assert.Equal(t, "pending|my-cluster|my-svc", pr.NativeID)
}

func TestWrapForCreate_SyncSuccess_RewritesToInProgress(t *testing.T) {
	s := &Service{now: func() time.Time { return time.Unix(1747526400, 0) }}
	pr := &resource.ProgressResult{
		Operation:          resource.OperationCreate,
		OperationStatus:    resource.OperationStatusSuccess,
		RequestID:          "ccapi-tA",
		NativeID:           "arn:aws:ecs:us-east-1:123:service/my-cluster/my-svc|my-cluster",
		ResourceProperties: []byte(`{"some":"props"}`),
	}
	s.wrapForCreate(pr, "my-cluster", "my-svc")
	assert.Equal(t, resource.OperationStatusInProgress, pr.OperationStatus)
	assert.Nil(t, pr.ResourceProperties)
	assert.Contains(t, pr.StatusMessage, "waiting")
	assert.Equal(t, "formae-ecs/create/1747526400/ccapi-tA", pr.RequestID)
	// NativeID stays canonical when CCAPI gave us one.
	assert.Equal(t, "arn:aws:ecs:us-east-1:123:service/my-cluster/my-svc|my-cluster", pr.NativeID)
}

func TestWrapForCreate_EmptyRequestID_NoOp(t *testing.T) {
	s := &Service{now: func() time.Time { return time.Unix(1747526400, 0) }}
	pr := &resource.ProgressResult{
		OperationStatus: resource.OperationStatusFailure,
		RequestID:       "",
	}
	s.wrapForCreate(pr, "c", "s")
	assert.Equal(t, "", pr.RequestID)
	assert.Equal(t, "", pr.NativeID)
}

func TestWrapForUpdate_NativeIDStaysCanonical(t *testing.T) {
	s := &Service{now: func() time.Time { return time.Unix(1747526400, 0) }}
	pr := &resource.ProgressResult{
		Operation:       resource.OperationUpdate,
		OperationStatus: resource.OperationStatusInProgress,
		RequestID:       "ccapi-tU",
		NativeID:        "",
	}
	canonical := "arn:aws:ecs:us-east-1:123:service/my-cluster/my-svc|my-cluster"
	s.wrapForUpdate(pr, canonical, "my-cluster", "my-svc")
	assert.Equal(t, "formae-ecs/update/1747526400/ccapi-tU", pr.RequestID)
	assert.Equal(t, canonical, pr.NativeID)
	assert.Equal(t, resource.OperationUpdate, pr.Operation)
}

func TestHasInactiveFailure(t *testing.T) {
	out := &awsecs.DescribeServicesOutput{
		Failures: []ecstypes.Failure{
			{Arn: ptrString("arn"), Reason: ptrString("INACTIVE")},
		},
	}
	assert.True(t, hasInactiveFailure(out))

	out2 := &awsecs.DescribeServicesOutput{
		Failures: []ecstypes.Failure{
			{Arn: ptrString("arn"), Reason: ptrString("MISSING")},
		},
	}
	assert.False(t, hasInactiveFailure(out2))

	out3 := &awsecs.DescribeServicesOutput{}
	assert.False(t, hasInactiveFailure(out3))
}

func ptrString(s string) *string { return &s }
