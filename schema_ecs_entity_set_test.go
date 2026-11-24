// © 2026 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The identity hint is consumed against serialized provider properties. A
// logical Pkl field name here makes distinct containers share a missing key.
func TestSchema_ECSContainerEntitySetIdentity(t *testing.T) {
	if _, err := exec.LookPath("pkl"); err != nil {
		t.Skip("pkl not on PATH")
	}
	dir := schemaPklDir()
	probe, err := os.CreateTemp(dir, "_ecs_entity_probe_*.pkl")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.Remove(probe.Name()) })
	_, err = probe.WriteString(`
import "ecs/taskdefinition.pkl"
import "@formae/forma.pkl"

output { renderer = forma.output.renderer }

local app = new taskdefinition.ContainerDefinition {
  name = "app"
  image = "example/app:1"
  essential = true
  environment { new { name = "MODE"; value = "serve" } }
}
local sidecar = new taskdefinition.ContainerDefinition {
  name = "sidecar"
  image = "example/sidecar:1"
  essential = false
  command { "proxy" }
}
local before = new taskdefinition.TaskDefinition {
  label = "entity-test"
  containerDefinitions { app; sidecar }
}
local after = (before) {
  containerDefinitions = new Listing<taskdefinition.ContainerDefinition> {
    (app) { image = "example/app:2" }
    sidecar
  }
}
local reordered = (after) {
  containerDefinitions = new Listing<taskdefinition.ContainerDefinition> {
    sidecar
    (app) { image = "example/app:2" }
  }
}
hint = before.hints()["ContainerDefinitions"]
oldContainers = before.props().ContainerDefinitions
newContainers = after.props().ContainerDefinitions
reorderedContainers = reordered.props().ContainerDefinitions
`)
	require.NoError(t, err)
	require.NoError(t, probe.Close())
	resolve := exec.Command("pkl", "project", "resolve")
	resolve.Dir = dir
	out, err := resolve.CombinedOutput()
	require.NoError(t, err, "pkl project resolve: %s", out)
	cmd := exec.Command("pkl", "eval", "-f", "json", filepath.Base(probe.Name()))
	cmd.Dir = dir
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "pkl eval: %s", out)
	var rendered struct {
		Hint struct {
			IndexField   string
			UpdateMethod string
		}
		OldContainers, NewContainers, ReorderedContainers []map[string]any
	}
	require.NoError(t, json.Unmarshal(out, &rendered))
	require.Equal(t, "EntitySet", rendered.Hint.UpdateMethod)
	// Assert both the emitted contract and its use on actual rendered members.
	assert.Equal(t, "Name", rendered.Hint.IndexField)
	index := func(containers []map[string]any) map[string]map[string]any {
		t.Helper()
		require.Len(t, containers, 2)
		result := make(map[string]map[string]any)
		for _, container := range containers {
			identity, ok := container[rendered.Hint.IndexField].(string)
			require.True(t, ok, "identity %q missing from serialized container", rendered.Hint.IndexField)
			require.NotEmpty(t, identity)
			require.NotContains(t, result, identity, "containers must have distinct identities")
			result[identity] = container
		}
		require.Contains(t, result, "app")
		require.Contains(t, result, "sidecar")
		return result
	}
	old := index(rendered.OldContainers)
	updated := index(rendered.NewContainers)
	reorderedIndex := index(rendered.ReorderedContainers)
	assert.Equal(t, updated, reorderedIndex, "reordering must preserve entity identity")
	assert.Equal(t, old["sidecar"], updated["sidecar"])
	assert.Equal(t, "example/app:1", old["app"]["Image"])
	assert.Equal(t, "example/app:2", updated["app"]["Image"])
	// Everything other than the intentionally changed image survives unchanged.
	delete(old["app"], "Image")
	delete(updated["app"], "Image")
	assert.Equal(t, old["app"], updated["app"])
}
