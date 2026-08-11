// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package rds

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The RFC 7677 SCRAM-SHA-256 test vector: password "pencil", 4096 iterations,
// with the StoredKey and ServerKey the RFC publishes for that salt. PostgreSQL
// stores exactly these two keys in its verifier, so matching the vector proves
// the composition the engine will accept.
func TestScramVerifierMatchesRFC7677Vector(t *testing.T) {
	salt, err := base64.StdEncoding.DecodeString("W22ZaJ0SNY7soEsUEjb6gQ==")
	require.NoError(t, err)

	verifier := scramVerifier("pencil", salt, 4096)

	assert.Equal(t,
		"SCRAM-SHA-256$4096:W22ZaJ0SNY7soEsUEjb6gQ==$WG5d8oPm3OtcPnkdi4Uo7BkeZkBFzpcXkuLmtbsT4qY=:wfPLwcE6nTWhTAmQ7tl2KeoiWGPlZqQxSrmfPwDl2dU=",
		verifier)
}

func TestValidatePasswordAcceptsPrintableASCII(t *testing.T) {
	valid := []struct {
		name     string
		password string
	}{
		{"generated style", "aB3$xY7!kL9#mN2%"},
		{"lower boundary space", "abc def"},
		{"upper boundary tilde", "abc~def"},
		{"only boundaries", " ~"},
	}
	for _, tt := range valid {
		t.Run(tt.name, func(t *testing.T) {
			assert.NoError(t, validatePassword(tt.password))
		})
	}
}

func TestValidatePasswordRejectsOutsidePrintableASCII(t *testing.T) {
	invalid := []struct {
		name     string
		password string
	}{
		{"empty", ""},
		{"unit separator below range", "abcdef"},
		{"delete above range", "abcdef"},
		{"tab", "abc\tdef"},
		{"newline", "abc\ndef"},
		{"non ascii letter", "café"},
		{"emoji", "pass🔐word"},
	}
	for _, tt := range invalid {
		t.Run(tt.name, func(t *testing.T) {
			err := validatePassword(tt.password)
			require.Error(t, err)
			// The constraint has to be named, or the operator cannot tell why a
			// perfectly good-looking password was refused.
			assert.Contains(t, err.Error(), "U+0020")
			assert.Contains(t, err.Error(), "U+007E")
			// Never echo the password itself.
			if tt.password != "" {
				assert.NotContains(t, err.Error(), tt.password)
			}
		})
	}
}

func TestNewScramVerifierProducesAFreshSaltPerCall(t *testing.T) {
	first, err := newScramVerifier("aB3$xY7!kL9#mN2%")
	require.NoError(t, err)
	second, err := newScramVerifier("aB3$xY7!kL9#mN2%")
	require.NoError(t, err)

	assert.NotEqual(t, first, second, "each call must draw a new salt")
}

func TestNewScramVerifierProducesAParseableEnvelope(t *testing.T) {
	verifier, err := newScramVerifier("aB3$xY7!kL9#mN2%")
	require.NoError(t, err)

	require.True(t, strings.HasPrefix(verifier, "SCRAM-SHA-256$"))

	body := strings.TrimPrefix(verifier, "SCRAM-SHA-256$")
	saltPart, keysPart, ok := strings.Cut(body, "$")
	require.True(t, ok, "verifier must separate the salt section from the key section")

	iterations, encodedSalt, ok := strings.Cut(saltPart, ":")
	require.True(t, ok)
	assert.Equal(t, "4096", iterations)
	salt, err := base64.StdEncoding.DecodeString(encodedSalt)
	require.NoError(t, err)
	assert.Len(t, salt, 16)

	storedKey, serverKey, ok := strings.Cut(keysPart, ":")
	require.True(t, ok)
	decodedStored, err := base64.StdEncoding.DecodeString(storedKey)
	require.NoError(t, err)
	assert.Len(t, decodedStored, 32)
	decodedServer, err := base64.StdEncoding.DecodeString(serverKey)
	require.NoError(t, err)
	assert.Len(t, decodedServer, 32)
}

func TestNewScramVerifierRejectsAnInvalidPassword(t *testing.T) {
	_, err := newScramVerifier("café")
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "café")
}

// A verifier that composed to the empty string would clear the role's password
// while the plugin reported success, so it must be refused outright.
func TestNewScramVerifierNeverReturnsAnEmptyVerifier(t *testing.T) {
	verifier, err := newScramVerifier("aB3$xY7!kL9#mN2%")
	require.NoError(t, err)
	assert.NotEmpty(t, verifier)
}
