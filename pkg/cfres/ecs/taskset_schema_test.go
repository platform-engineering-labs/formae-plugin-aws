// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package ecs

// TestTaskSet_Schema_AttachesToAnnotations is a textual smoke test that verifies
// the targetGroupArn field on AWS::ECS::TaskSet's LoadBalancer carries the
// attachesTo edge annotation (@aws.FieldHint { edgeKind = "attachesTo" })
// in the PKL schema source. TaskSet uses the same load balancer wiring as
// AWS::ECS::Service, so it must mirror the same reachability semantics.
// Without this annotation the AttachesTo-based destroy ordering for TaskSet
// load-balancer wiring would silently break.
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

func tasksetSchemaPath() string {
	_, filename, _, _ := runtime.Caller(0)
	// pkg/cfres/ecs/ -> (repo root)/schema/pkl/ecs/taskset.pkl
	root := filepath.Join(filepath.Dir(filename), "..", "..", "..", "schema", "pkl", "ecs")
	return filepath.Join(root, "taskset.pkl")
}

func TestTaskSet_Schema_LoadBalancer_TargetGroupArn_AttachesTo(t *testing.T) {
	content, err := os.ReadFile(tasksetSchemaPath())
	if err != nil {
		t.Fatalf("could not read taskset.pkl: %v", err)
	}

	body := extractClassBody(string(content), "LoadBalancer")
	if body == "" {
		t.Fatal("LoadBalancer class not found in taskset.pkl")
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
	t.Fatal("targetGroupArn field not found inside LoadBalancer class body in taskset.pkl")
}
