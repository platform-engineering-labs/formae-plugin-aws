// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package ecs

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	awsecs "github.com/aws/aws-sdk-go-v2/service/ecs"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
)

// parseComposite splits "formae-ecs/<op>/<unixStart>/<ccapiToken>" into its
// components. Returns ok=false for anything that does not start with the
// formae-ecs prefix or that has a malformed op/unix segment — the caller is
// expected to fall through to generic CCAPI Status for ok=false.
func parseComposite(s string) (op resource.Operation, unixStart int64, ccapiToken string, ok bool) {
	if !strings.HasPrefix(s, phaseBPrefix) {
		return "", 0, "", false
	}
	rest := s[len(phaseBPrefix):]
	parts := strings.SplitN(rest, "/", 3)
	if len(parts) != 3 {
		return "", 0, "", false
	}
	switch parts[0] {
	case opSegCreate:
		op = resource.OperationCreate
	case opSegUpdate:
		op = resource.OperationUpdate
	default:
		return "", 0, "", false
	}
	unixStart, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return "", 0, "", false
	}
	ccapiToken = parts[2]
	return op, unixStart, ccapiToken, true
}

// composeRequestID builds the composite RequestID set at Create/Update return time.
func composeRequestID(opSeg string, unixStart int64, ccapiToken string) string {
	return fmt.Sprintf("%s%s/%d/%s", phaseBPrefix, opSeg, unixStart, ccapiToken)
}

// parseClusterAndServiceFromNativeID accepts either:
//   - "pending|<cluster>|<service>"  (synthetic, set by Create when CCAPI gave no Identifier)
//   - "<serviceArn>|<clusterShortName>"  (canonical: Update, or Create with sync Identifier)
//
// Returns ok=false for malformed/empty shapes; caller should treat as a terminal
// programmer error (Failure InvalidRequest).
func parseClusterAndServiceFromNativeID(nativeID string) (cluster, service string, ok bool) {
	if nativeID == "" {
		return "", "", false
	}
	if strings.HasPrefix(nativeID, "pending|") {
		// pending|<cluster>|<service>
		parts := strings.SplitN(nativeID, "|", 3)
		if len(parts) != 3 || parts[1] == "" || parts[2] == "" {
			return "", "", false
		}
		return parts[1], parts[2], true
	}
	// Canonical: <serviceArn>|<clusterShortName>. Last '|' splits arn from cluster.
	idx := strings.LastIndex(nativeID, "|")
	if idx < 0 || idx == len(nativeID)-1 {
		return "", "", false
	}
	serviceArn := nativeID[:idx]
	cluster = nativeID[idx+1:]
	// Service short name is the last "/" segment of the ARN.
	slashIdx := strings.LastIndex(serviceArn, "/")
	if slashIdx < 0 || slashIdx == len(serviceArn)-1 {
		return "", "", false
	}
	service = serviceArn[slashIdx+1:]
	if cluster == "" || service == "" {
		return "", "", false
	}
	return cluster, service, true
}

// buildCanonicalNativeID composes "<serviceArn>|<clusterShortName>" — the canonical
// form the agent persists. Used by finalSuccess once Phase B stability is observed.
func buildCanonicalNativeID(serviceArn, clusterShortName string) string {
	return serviceArn + "|" + clusterShortName
}

// shapeSupportsPhaseB returns true iff the request properties describe the shape
// our Phase B tracking handles: deploymentController.type == "ECS" (or absent),
// AND schedulingStrategy == "REPLICA" (or absent). Anything else (CODE_DEPLOY,
// EXTERNAL, DAEMON) falls through to generic CCAPI Status. Empty/nil props →
// false (conservative — let the generic path handle whatever it is).
func shapeSupportsPhaseB(props json.RawMessage) bool {
	if len(props) == 0 {
		return false
	}
	var shape struct {
		DeploymentController *struct {
			Type string `json:"Type"`
		} `json:"DeploymentController"`
		SchedulingStrategy string `json:"SchedulingStrategy"`
	}
	if err := json.Unmarshal(props, &shape); err != nil {
		return false
	}
	if shape.DeploymentController != nil && shape.DeploymentController.Type != "" && shape.DeploymentController.Type != "ECS" {
		return false
	}
	if shape.SchedulingStrategy != "" && shape.SchedulingStrategy != "REPLICA" {
		return false
	}
	return true
}

// parseCreateClusterAndService extracts Cluster (normalized to short name) and
// ServiceName from a Create request's Properties. Returns a wrapped error
// pointing at the schema field if ServiceName is missing.
//
// Cluster is optional in the AWS::ECS::Service schema: when absent, ECS places
// the service in a cluster literally named "default" (auto-created on first
// use). We mirror that semantic here so Phase B polls the right cluster.
func parseCreateClusterAndService(props json.RawMessage) (cluster, service string, err error) {
	var p struct {
		Cluster     string `json:"Cluster"`
		ServiceName string `json:"ServiceName"`
	}
	if err := json.Unmarshal(props, &p); err != nil {
		return "", "", fmt.Errorf("parse Create properties: %w", err)
	}
	if p.ServiceName == "" {
		return "", "", fmt.Errorf("AWS::ECS::Service.ServiceName is required for Phase B tracking (REPLICA + ECS controller); auto-generated names are not supported in v1")
	}
	cluster = p.Cluster
	if strings.HasPrefix(cluster, "arn:") {
		// arn:aws:ecs:region:account:cluster/<name> → last "/" segment
		if idx := strings.LastIndex(cluster, "/"); idx >= 0 {
			cluster = cluster[idx+1:]
		}
	}
	if cluster == "" {
		cluster = "default"
	}
	return cluster, p.ServiceName, nil
}

// parseUpdateClusterAndService extracts cluster + service from the canonical
// NativeID an Update request carries ("<serviceArn>|<clusterShortName>").
func parseUpdateClusterAndService(nativeID string) (cluster, service string, err error) {
	c, s, ok := parseClusterAndServiceFromNativeID(nativeID)
	if !ok {
		return "", "", fmt.Errorf("malformed Update NativeID: %q", nativeID)
	}
	return c, s, nil
}

// wrapForCreate encodes the composite RequestID and (when CCAPI didn't give us a
// canonical identifier) a synthetic NativeID, so the operator-preserved fields
// carry enough state for Phase B dispatch without depending on CCAPI returning
// an Identifier first. If CCAPI returned sync Success, also rewrite to InProgress
// so the operator transitions into status-polling. No-op for missing RequestID
// (CCAPI returned an error-shaped result with no token).
func (s *Service) wrapForCreate(pr *resource.ProgressResult, cluster, service string) {
	if pr == nil || pr.RequestID == "" {
		return
	}
	if pr.OperationStatus == resource.OperationStatusSuccess {
		pr.OperationStatus = resource.OperationStatusInProgress
		pr.ResourceProperties = nil
		pr.StatusMessage = "ECS registration complete; waiting for service to appear"
	}
	pr.RequestID = composeRequestID(opSegCreate, s.now().Unix(), pr.RequestID)
	if pr.NativeID == "" {
		pr.NativeID = "pending|" + cluster + "|" + service
	}
	pr.Operation = resource.OperationCreate
}

// wrapForUpdate is the Update analogue. NativeID is always canonical for Update
// (req.NativeID is populated by definition), but we set it defensively in case
// ccx returned an empty one.
func (s *Service) wrapForUpdate(pr *resource.ProgressResult, canonicalNativeID, cluster, service string) {
	if pr == nil || pr.RequestID == "" {
		return
	}
	if pr.OperationStatus == resource.OperationStatusSuccess {
		pr.OperationStatus = resource.OperationStatusInProgress
		pr.ResourceProperties = nil
		pr.StatusMessage = "ECS update accepted; waiting for deployment to stabilize"
	}
	pr.RequestID = composeRequestID(opSegUpdate, s.now().Unix(), pr.RequestID)
	if pr.NativeID == "" {
		pr.NativeID = canonicalNativeID
	}
	pr.Operation = resource.OperationUpdate
}

// hasInactiveFailure scans DescribeServices.Failures for any entry whose Reason
// is "INACTIVE" — the fast path for OOB-delete detection. AWS keeps INACTIVE
// tombstones for ~1 hour post-delete.
func hasInactiveFailure(out *awsecs.DescribeServicesOutput) bool {
	if out == nil {
		return false
	}
	for _, f := range out.Failures {
		if f.Reason != nil && *f.Reason == "INACTIVE" {
			return true
		}
	}
	return false
}
