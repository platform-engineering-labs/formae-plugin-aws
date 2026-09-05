// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package ccx

import (
	"encoding/json"
	"regexp"
	"strings"
)

// writeOnlyRejectionPattern matches the rejection CloudControl returns when a
// patch uses an operation other than 'add' on a writeOnly property:
//
//	ValidationException: Invalid patch update: writeOnlyProperties
//	[/Code/ZipFile] can only be updated using 'add' operation
//
// The bracketed list is the whole point: CloudControl names the exact paths it
// will not accept a 'replace' for, which is why the plugin needs no schema of
// its own to know which properties are writeOnly.
// The match is deliberately narrow on structure and loose on prose: it keys on
// the property list CloudControl brackets, not on the English sentence around
// it. Requiring the full sentence would make a rewording break every writeOnly
// update, including the SecretsManager case that worked before this existed.
// A message that brackets writeOnly properties for some other reason costs one
// rejected resend and then stops, which is a cheaper failure than that.
var writeOnlyRejectionPattern = regexp.MustCompile(`writeOnlyProperties \[([^\]]*)\]`)

// maxWriteOnlyResends returns how many times a patch can usefully be rewritten
// and resent: every resend converts at least one 'replace' into an 'add' and
// never the reverse, so the document is exhausted once every replace operation
// has been converted.
//
// The bound is derived from the patch rather than fixed, because CloudControl
// may name one offending property per rejection. A constant smaller than the
// number of replace operations would abandon an update that was still
// converging, and resource types carrying ten or sixteen writeOnly properties
// make that reachable. An unparseable patch yields zero resends.
func maxWriteOnlyResends(patchDoc string) int {
	var ops []map[string]any
	if err := json.Unmarshal([]byte(patchDoc), &ops); err != nil {
		return 0
	}

	replaces := 0
	for _, op := range ops {
		if op["op"] == "replace" {
			replaces++
		}
	}
	return replaces
}

// writeOnlyPathsFromError returns the patch paths CloudControl named in a
// writeOnly rejection, or nil when the error is not one. A message whose shape
// changes therefore yields no paths and the update fails exactly as it did
// before this existed, rather than misfiring.
func writeOnlyPathsFromError(err error) []string {
	if err == nil {
		return nil
	}
	match := writeOnlyRejectionPattern.FindStringSubmatch(err.Error())
	if match == nil {
		return nil
	}

	var paths []string
	for _, path := range strings.Split(match[1], ",") {
		if path = strings.TrimSpace(path); path != "" {
			paths = append(paths, path)
		}
	}
	return paths
}

// transformWriteOnlyPatch rewrites 'replace' to 'add' for every operation whose
// path CloudControl named, which is the only operation it accepts on a
// writeOnly property. formae plans 'replace' because the property carries a
// prior value in its model; CloudControl, which never returns the property on a
// read, sees nothing to replace.
//
// Only an exact path match is rewritten. CloudControl enumerates nested
// writeOnly properties in their own right — Lambda names both /SnapStart and
// /SnapStart/ApplyOn — so it always names the path it is actually rejecting,
// and rewriting a descendant it did not name would be guesswork: 'add' below a
// parent that is absent from live state does not create that parent.
//
// A malformed document is returned unchanged alongside the error, so a caller
// that ignores the error still transmits what it was given.
func transformWriteOnlyPatch(patchDoc string, writeOnly []string) (string, error) {
	if patchDoc == "" || len(writeOnly) == 0 {
		return patchDoc, nil
	}

	var ops []map[string]any
	if err := json.Unmarshal([]byte(patchDoc), &ops); err != nil {
		return patchDoc, err
	}

	named := make(map[string]struct{}, len(writeOnly))
	for _, path := range writeOnly {
		named[path] = struct{}{}
	}

	modified := false
	for i, op := range ops {
		if op["op"] != "replace" {
			continue
		}
		path, ok := op["path"].(string)
		if !ok {
			continue
		}
		if _, isWriteOnly := named[path]; !isWriteOnly {
			continue
		}
		ops[i]["op"] = "add"
		modified = true
	}

	if !modified {
		return patchDoc, nil
	}

	transformed, err := json.Marshal(ops)
	if err != nil {
		return patchDoc, err
	}
	return string(transformed), nil
}
