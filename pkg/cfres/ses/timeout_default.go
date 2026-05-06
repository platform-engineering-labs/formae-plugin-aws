// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build !conformance

package ses

import "time"

// sesVerificationTimeout caps how long EmailIdentityVerification will poll
// before declaring failure. Production default; build with -tags=conformance
// to use a short value suitable for CI conformance runs.
var sesVerificationTimeout = 30 * time.Minute
