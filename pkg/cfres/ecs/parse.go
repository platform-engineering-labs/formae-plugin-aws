// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package ecs

import (
	"fmt"
	"strconv"
	"strings"

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
