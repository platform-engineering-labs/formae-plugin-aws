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
