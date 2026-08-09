// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package kms

import (
	"testing"
)

func TestPreserveTargetKeyIDForm(t *testing.T) {
	const keyID = "7fe219a1-df69-445e-a3a8-ffa6a17574d9"
	const keyARN = "arn:aws:kms:us-east-1:111122223333:key/" + keyID

	tests := []struct {
		name  string
		prior string
		read  string
		want  string
	}{
		{
			name:  "prior ARN naming the read key keeps the ARN form",
			prior: keyARN,
			read:  keyID,
			want:  keyARN,
		},
		{
			name:  "prior ARN naming a different key reports the read value",
			prior: "arn:aws:kms:us-east-1:111122223333:key/00000000-0000-0000-0000-000000000000",
			read:  keyID,
			want:  keyID,
		},
		{
			name:  "prior bare key id passes the read value through",
			prior: keyID,
			read:  keyID,
			want:  keyID,
		},
		{
			name:  "no prior value passes the read value through",
			prior: "",
			read:  keyID,
			want:  keyID,
		},
		{
			name:  "alias ARN as prior is not a key ARN and passes the read value through",
			prior: "arn:aws:kms:us-east-1:111122223333:alias/" + keyID,
			read:  keyID,
			want:  keyID,
		},
		{
			name:  "empty read value stays empty",
			prior: keyARN,
			read:  "",
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := preserveTargetKeyIDForm(tt.prior, tt.read); got != tt.want {
				t.Errorf("preserveTargetKeyIDForm(%q, %q) = %q, want %q", tt.prior, tt.read, got, tt.want)
			}
		})
	}
}
