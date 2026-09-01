// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Every hasProviderDefault annotation in the schema must carry a recorded
// disposition in schema/provider-default-dispositions.json. The manifest keeps
// the audit of provider-default semantics self-maintaining: adding a new
// annotation without classifying it fails this test, and removing a field
// without pruning its manifest row fails it too.
//
// Dispositions:
//   - "pending":           annotated before the audit reached it; classify on touch.
//   - "keep":              provider-default-once; requires a "pin", either
//                          "documentation" or "fixture:<path>" to a drift-injection fixture.
//   - "co-owned":          a co-actor legitimately writes the field in some or all
//                          deployments; awaiting the ownership-model design.
//   - "referenced-output": never user-declarable but referenced as an output, so
//                          it cannot be deleted from the schema yet.
//
// Fields classified as deletable or as plain user-controlled do not appear
// here: those dispositions are applied to the schema itself.

type dispositionEntry struct {
	Key         string `json:"key"`
	Disposition string `json:"disposition"`
	Pin         string `json:"pin,omitempty"`
}

type dispositionManifest struct {
	Comment      string             `json:"_comment"`
	Dispositions []dispositionEntry `json:"dispositions"`
}

var (
	classRe = regexp.MustCompile(`^(?:open\s+|abstract\s+)*class\s+(\w+)`)
	fieldRe = regexp.MustCompile("^\\s+`?(\\w+)`?\\s*:")
	// Whitespace-tolerant: an exact-substring match silently misses
	// hasProviderDefault=true and hasProviderDefault  =  true, leaving the
	// annotation unclassified with nothing to signal it.
	providerDefaultRe = regexp.MustCompile(`hasProviderDefault\s*=\s*true`)
)

// collectProviderDefaultAnnotations walks schema/pkl and returns the key
// (relative path # enclosing class . field name) of every field annotated
// hasProviderDefault = true.
func collectProviderDefaultAnnotations(t *testing.T, schemaDir string) []string {
	t.Helper()
	var keys []string
	err := filepath.WalkDir(schemaDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".pkl") {
			return nil
		}
		rel, err := filepath.Rel(schemaDir, path)
		if err != nil {
			return err
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		lines := strings.Split(string(content), "\n")
		currentClass := ""
		for i := 0; i < len(lines); i++ {
			line := lines[i]
			if m := classRe.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
				currentClass = m[1]
				continue
			}
			// A doc comment naming the annotation is prose, not a declaration:
			// treating it as one demands a manifest row for a field that does
			// not exist.
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue
			}
			if !strings.Contains(line, "@") || !strings.Contains(line, "FieldHint") {
				continue
			}
			// Accumulate the annotation block until braces balance.
			block := line
			for strings.Count(block, "{") > strings.Count(block, "}") && i+1 < len(lines) {
				i++
				block += " " + strings.TrimSpace(lines[i])
			}
			if !providerDefaultRe.MatchString(block) {
				continue
			}
			// The next non-blank, non-annotation, non-comment line is the field.
			for j := i + 1; j < len(lines); j++ {
				next := lines[j]
				trimmed := strings.TrimSpace(next)
				if trimmed == "" || strings.HasPrefix(trimmed, "@") || strings.HasPrefix(trimmed, "//") {
					continue
				}
				if m := fieldRe.FindStringSubmatch(next); m != nil {
					keys = append(keys, fmt.Sprintf("%s#%s.%s", rel, currentClass, m[1]))
				} else {
					t.Errorf("%s:%d: hasProviderDefault annotation not followed by a field declaration (got %q)", rel, j+1, trimmed)
				}
				break
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", schemaDir, err)
	}
	sort.Strings(keys)
	return keys
}

func TestProviderDefaultDispositionsManifest(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("schema", "provider-default-dispositions.json"))
	if err != nil {
		t.Fatalf("reading manifest: %v", err)
	}
	var manifest dispositionManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("parsing manifest: %v", err)
	}

	rows := make(map[string]dispositionEntry, len(manifest.Dispositions))
	for _, e := range manifest.Dispositions {
		if _, dup := rows[e.Key]; dup {
			t.Errorf("duplicate manifest row: %s", e.Key)
		}
		rows[e.Key] = e

		switch e.Disposition {
		case "pending", "co-owned", "referenced-output":
			if e.Pin != "" {
				t.Errorf("%s: pin is only valid on disposition \"keep\"", e.Key)
			}
		case "keep":
			if e.Pin != "documentation" && !strings.HasPrefix(e.Pin, "fixture:") {
				t.Errorf("%s: disposition \"keep\" requires pin \"documentation\" or \"fixture:<path>\", got %q", e.Key, e.Pin)
			}
			if p, ok := strings.CutPrefix(e.Pin, "fixture:"); ok {
				if _, err := os.Stat(p); err != nil {
					t.Errorf("%s: pin fixture %q does not exist", e.Key, p)
				}
			}
		default:
			t.Errorf("%s: unknown disposition %q", e.Key, e.Disposition)
		}
	}

	annotations := collectProviderDefaultAnnotations(t, filepath.Join("schema", "pkl"))
	seen := make(map[string]bool, len(annotations))
	for _, key := range annotations {
		seen[key] = true
		if _, ok := rows[key]; !ok {
			t.Errorf("hasProviderDefault field without a disposition; add to schema/provider-default-dispositions.json:\n  {\"key\": %q, \"disposition\": \"pending\"}", key)
		}
	}
	for key := range rows {
		if !seen[key] {
			t.Errorf("stale manifest row (no matching hasProviderDefault annotation): %s", key)
		}
	}
}
