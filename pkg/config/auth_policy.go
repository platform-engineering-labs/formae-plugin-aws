// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package config

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// AuthPolicy is the plugin-lifetime allowlist of target authentication
// methods. A zero-value policy is unrestricted for backwards compatibility.
type AuthPolicy struct {
	allowed map[string]struct{}
}

// NewAuthPolicy validates and builds an authentication-method allowlist.
func NewAuthPolicy(methods []string) (AuthPolicy, error) {
	policy := AuthPolicy{}
	if len(methods) == 0 {
		return policy, nil
	}

	policy.allowed = make(map[string]struct{}, len(methods))
	for _, method := range methods {
		switch method {
		case AuthTypeDefaultChain, AuthTypeOidc:
			policy.allowed[method] = struct{}{}
		default:
			return AuthPolicy{}, fmt.Errorf("config: unknown auth method %q", method)
		}
	}

	return policy, nil
}

// Validate permits every method when the policy is empty and otherwise
// rejects any method that is not explicitly allowlisted.
func (p AuthPolicy) Validate(method string) error {
	if len(p.allowed) == 0 {
		return nil
	}
	if _, ok := p.allowed[method]; ok {
		return nil
	}

	return fmt.Errorf(
		"config: auth method %q is not allowed; allowed methods: %s",
		method,
		p.allowedMethodsString(),
	)
}

func (p AuthPolicy) allowedMethodsString() string {
	methods := make([]string, 0, len(p.allowed))
	for method := range p.allowed {
		methods = append(methods, method)
	}
	sort.Strings(methods)
	for i := range methods {
		methods[i] = strconv.Quote(methods[i])
	}

	return strings.Join(methods, ", ")
}
