// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build conformance

package ses

import "time"

// Conformance runs use a short timeout so the Failed-on-timeout path is
// observable in well under a minute.
var sesVerificationTimeout = 60 * time.Second
