// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package rds

import (
	"encoding/json"
	"strings"
)

// patchAffects reports whether the planned change described by an RFC 6902 patch
// document touches the property at path.
//
// It is the only signal available for a write-only property: its prior value is
// never read back, so a declared value cannot be compared against anything. The
// patch is not a complete oracle either — it reports only what was planned — so
// the answer is deliberately biased towards yes. An absent, empty or unparseable
// document counts as affecting the property, as does an operation anywhere above
// or below it. Re-applying a value that did not change converges; missing a
// change does not.
func patchAffects(patchDocument *string, path string) bool {
	if patchDocument == nil || strings.TrimSpace(*patchDocument) == "" {
		return true
	}

	var operations []map[string]any
	if err := json.Unmarshal([]byte(*patchDocument), &operations); err != nil {
		return true
	}
	if len(operations) == 0 {
		return true
	}

	for _, operation := range operations {
		// "from" is the source of a move or a copy, which carries the property
		// away from where it was as surely as "path" carries one to it.
		for _, key := range []string{"path", "from"} {
			candidate, ok := operation[key].(string)
			if !ok {
				continue
			}
			if pathsOverlap(candidate, path) {
				return true
			}
		}
	}
	return false
}

// pathsOverlap reports whether two JSON Pointers name the same property, or one
// contains the other.
//
// Comparison is case-insensitive so that an operation named for the schema's own
// spelling of a property matches its serialized spelling; the two differ only in
// case, and answering yes to both is the safe direction.
func pathsOverlap(a, b string) bool {
	a, b = strings.ToLower(a), strings.ToLower(b)
	return a == b || contains(a, b) || contains(b, a)
}

// contains reports whether one pointer encloses another. The empty pointer is the
// document root, which encloses everything; otherwise a pointer encloses only
// whole segments beneath it, so /PasswordPolicy does not enclose /Password.
func contains(outer, inner string) bool {
	if outer == "" {
		return true
	}
	return strings.HasPrefix(inner, outer+"/")
}
