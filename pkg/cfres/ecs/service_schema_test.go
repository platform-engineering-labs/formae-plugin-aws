// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package ecs

// TestService_Schema_AttachesToAnnotations is a textual smoke test that verifies
// the two targetGroupArn fields on AWS::ECS::Service carry the attachesTo edge
// annotation (@aws.FieldHint { edgeKind = "attachesTo" }) in the PKL
// schema source. This guards against a future refactor accidentally removing
// the annotation and silently breaking the AttachesTo-based destroy ordering
// that ECS load-balancer and VPC Lattice wiring depends on.
//
// This is intentionally a textual test rather than a full schema-emission test:
// running ExtractSchema in a unit test requires a live pkl CLI subprocess and
// network access for package resolution, making it unsuitable for make test-unit.
// A proper emission test should live in a dedicated schema integration test suite.

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func serviceSchemaPath() string {
	_, filename, _, _ := runtime.Caller(0)
	// pkg/cfres/ecs/ -> (repo root)/schema/pkl/ecs/service.pkl
	root := filepath.Join(filepath.Dir(filename), "..", "..", "..", "schema", "pkl", "ecs")
	return filepath.Join(root, "service.pkl")
}

// extractClassBody returns the text of the class body from the first occurrence
// of "open class <className>" up to the closing "}" that ends the class block
// (we use a simple heuristic: collect lines until we see a line that is just "}").
func extractClassBody(src, className string) string {
	marker := "open class " + className + " extends"
	start := strings.Index(src, marker)
	if start == -1 {
		return ""
	}
	rest := src[start:]
	// Collect until we find the closing brace that ends the class (a line
	// consisting of only "}" with possible surrounding whitespace).
	lines := strings.Split(rest, "\n")
	var body strings.Builder
	for _, line := range lines {
		body.WriteString(line)
		body.WriteByte('\n')
		if strings.TrimSpace(line) == "}" {
			break
		}
	}
	return body.String()
}

func TestService_Schema_LoadBalancer_TargetGroupArn_AttachesTo(t *testing.T) {
	content, err := os.ReadFile(serviceSchemaPath())
	if err != nil {
		t.Fatalf("could not read service.pkl: %v", err)
	}

	body := extractClassBody(string(content), "LoadBalancer")
	if body == "" {
		t.Fatal("LoadBalancer class not found in service.pkl")
	}

	lines := strings.Split(body, "\n")
	for i, line := range lines {
		if strings.Contains(line, "targetGroupArn") {
			// The annotation must appear on the line immediately before.
			if i == 0 {
				t.Fatal("targetGroupArn is the first line of LoadBalancer body — no preceding annotation line")
			}
			prev := strings.TrimSpace(lines[i-1])
			if !strings.Contains(prev, "edgeKind") || !strings.Contains(prev, "attachesTo") {
				t.Errorf("LoadBalancer.targetGroupArn: expected @aws.FieldHint { edgeKind = \"attachesTo\" } on preceding line, got %q", prev)
			}
			return
		}
	}
	t.Fatal("targetGroupArn field not found inside LoadBalancer class body in service.pkl")
}

func TestService_Schema_VpcLatticeConfiguration_TargetGroupArn_AttachesTo(t *testing.T) {
	content, err := os.ReadFile(serviceSchemaPath())
	if err != nil {
		t.Fatalf("could not read service.pkl: %v", err)
	}

	body := extractClassBody(string(content), "VpcLatticeConfiguration")
	if body == "" {
		t.Fatal("VpcLatticeConfiguration class not found in service.pkl")
	}

	lines := strings.Split(body, "\n")
	for i, line := range lines {
		if strings.Contains(line, "targetGroupArn") {
			if i == 0 {
				t.Fatal("targetGroupArn is the first line of VpcLatticeConfiguration body — no preceding annotation line")
			}
			prev := strings.TrimSpace(lines[i-1])
			if !strings.Contains(prev, "edgeKind") || !strings.Contains(prev, "attachesTo") {
				t.Errorf("VpcLatticeConfiguration.targetGroupArn: expected @aws.FieldHint { edgeKind = \"attachesTo\" } on preceding line, got %q", prev)
			}
			return
		}
	}
	t.Fatal("targetGroupArn field not found inside VpcLatticeConfiguration class body in service.pkl")
}
