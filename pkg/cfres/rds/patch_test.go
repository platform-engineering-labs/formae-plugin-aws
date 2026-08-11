// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package rds

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPatchAffects(t *testing.T) {
	document := func(body string) *string { return &body }

	tests := []struct {
		name  string
		patch *string
		want  bool
	}{
		{"exact path", document(`[{"op":"replace","path":"/Password","value":"x"}]`), true},
		{"document root", document(`[{"op":"replace","path":"","value":{}}]`), true},
		{"parent object", document(`[{"op":"add","path":"/Password/nested","value":"x"}]`), true},
		{"schema-cased path", document(`[{"op":"replace","path":"/password","value":"x"}]`), true},
		{"move destination", document(`[{"op":"move","from":"/Other","path":"/Password"}]`), true},
		{"move source", document(`[{"op":"move","from":"/Password","path":"/Other"}]`), true},
		{"another property", document(`[{"op":"replace","path":"/CanLogin","value":false}]`), false},
		{"a property whose name starts the same", document(`[{"op":"replace","path":"/PasswordPolicy","value":"x"}]`), false},
		// An unusable signal must not be read as "nothing changed": re-applying a
		// value converges, missing a rotation does not.
		{"absent", nil, true},
		{"empty", document(""), true},
		{"blank", document("   "), true},
		{"no operations", document("[]"), true},
		{"json null", document("null"), true},
		{"unparseable", document("{not a patch"), true},
		{"not an array of operations", document(`{"op":"replace","path":"/CanLogin"}`), true},
		{"operation with no path", document(`[{"op":"replace","value":"x"}]`), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, patchAffects(tt.patch, "/Password"))
		})
	}
}
