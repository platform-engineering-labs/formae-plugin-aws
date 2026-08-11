// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package rds

import (
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
)

const (
	// PostgreSQL's own defaults for a SCRAM-SHA-256 verifier.
	scramSaltLength = 16
	scramIterations = 4096
	scramKeyLength  = 32
)

// validatePassword requires every code point to be printable ASCII.
//
// PostgreSQL SASLprep-normalizes a password before hashing it. Composing the
// verifier here means normalizing it here too, and an approximate SASLprep would
// produce a verifier the engine accepts but no client can authenticate against —
// a silent login failure. U+0020 to U+007E is the range SASLprep passes through
// unchanged, so restricting to it makes normalization a no-op. Secrets Manager's
// generated passwords are printable ASCII, so the ordinary path is unaffected.
func validatePassword(password string) error {
	if password == "" {
		return fmt.Errorf("password must not be empty: every code point must be printable ASCII (U+0020 to U+007E)")
	}
	for position, codePoint := range password {
		if codePoint < 0x20 || codePoint > 0x7E {
			return fmt.Errorf("password contains a code point outside U+0020 to U+007E at position %d", position)
		}
	}
	return nil
}

// scramVerifier composes the PostgreSQL SCRAM-SHA-256 verifier for a password:
//
//	SCRAM-SHA-256$<iterations>:<salt>$<StoredKey>:<ServerKey>
//
// PostgreSQL stores this string verbatim, so sending it in place of the
// plaintext keeps the reusable credential out of the SQL text — and therefore
// out of CloudTrail's rds-data events and any statement logging.
//
// Returns the empty string if the key derivation fails; callers must treat that
// as an error rather than sending it, since an empty PASSWORD clause would clear
// the role's password.
func scramVerifier(password string, salt []byte, iterations int) string {
	saltedPassword, err := pbkdf2.Key(sha256.New, password, salt, iterations, scramKeyLength)
	if err != nil {
		return ""
	}

	clientKey := hmacSHA256(saltedPassword, "Client Key")
	storedKey := sha256.Sum256(clientKey)
	serverKey := hmacSHA256(saltedPassword, "Server Key")

	return fmt.Sprintf("SCRAM-SHA-256$%d:%s$%s:%s",
		iterations,
		base64.StdEncoding.EncodeToString(salt),
		base64.StdEncoding.EncodeToString(storedKey[:]),
		base64.StdEncoding.EncodeToString(serverKey))
}

func hmacSHA256(key []byte, message string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(message))
	return mac.Sum(nil)
}

// newScramVerifier validates a password and composes a verifier for it under a
// freshly drawn salt, so rotating to the same password still produces a
// different verifier.
func newScramVerifier(password string) (string, error) {
	if err := validatePassword(password); err != nil {
		return "", err
	}

	salt := make([]byte, scramSaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("failed to generate a password salt: %w", err)
	}

	verifier := scramVerifier(password, salt, scramIterations)
	if verifier == "" {
		// Sending an empty verifier would clear the role's password while the
		// operation reported success.
		return "", fmt.Errorf("failed to compose the password verifier")
	}

	return verifier, nil
}
