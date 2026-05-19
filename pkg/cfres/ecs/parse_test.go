// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package ecs

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
)

func TestParseComposite_ValidCreate(t *testing.T) {
	op, unixStart, ccapiToken, ok := parseComposite("formae-ecs/create/1747526400/f470d40b-d23c-4d3a")
	assert.True(t, ok)
	assert.Equal(t, resource.OperationCreate, op)
	assert.Equal(t, int64(1747526400), unixStart)
	assert.Equal(t, "f470d40b-d23c-4d3a", ccapiToken)
}

func TestParseComposite_ValidUpdate(t *testing.T) {
	op, unixStart, ccapiToken, ok := parseComposite("formae-ecs/update/1747526400/abc-123")
	assert.True(t, ok)
	assert.Equal(t, resource.OperationUpdate, op)
	assert.Equal(t, int64(1747526400), unixStart)
	assert.Equal(t, "abc-123", ccapiToken)
}

func TestParseComposite_BareCCAPIToken(t *testing.T) {
	// CCAPI-shaped UUID without our prefix — this is the normal path for
	// CODE_DEPLOY/EXTERNAL/DAEMON shapes that bypass our wrap.
	_, _, _, ok := parseComposite("f470d40b-d23c-4d3a-9c11-some-uuid")
	assert.False(t, ok)
}

func TestParseComposite_EmptyString(t *testing.T) {
	_, _, _, ok := parseComposite("")
	assert.False(t, ok)
}

func TestParseComposite_MalformedUnix(t *testing.T) {
	_, _, _, ok := parseComposite("formae-ecs/create/not-a-number/abc")
	assert.False(t, ok)
}

func TestParseComposite_UnknownOp(t *testing.T) {
	_, _, _, ok := parseComposite("formae-ecs/delete/1747526400/abc")
	assert.False(t, ok)
}

func TestComposeRequestID_Create(t *testing.T) {
	s := composeRequestID(opSegCreate, 1747526400, "ccapi-token")
	assert.Equal(t, "formae-ecs/create/1747526400/ccapi-token", s)
}

func TestComposeRequestID_Update(t *testing.T) {
	s := composeRequestID(opSegUpdate, 1747526400, "ccapi-token")
	assert.Equal(t, "formae-ecs/update/1747526400/ccapi-token", s)
}

func TestComposite_RoundTrip(t *testing.T) {
	encoded := composeRequestID(opSegCreate, 1747526400, "tA")
	op, unixStart, token, ok := parseComposite(encoded)
	assert.True(t, ok)
	assert.Equal(t, resource.OperationCreate, op)
	assert.Equal(t, int64(1747526400), unixStart)
	assert.Equal(t, "tA", token)
}
