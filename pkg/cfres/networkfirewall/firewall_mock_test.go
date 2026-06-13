// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package networkfirewall

import (
	"context"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	"github.com/stretchr/testify/mock"
)

type mockCCXClient struct {
	mock.Mock
}

func (m *mockCCXClient) ReadResource(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	args := m.Called(ctx, request)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*resource.ReadResult), args.Error(1)
}

func (m *mockCCXClient) StatusResource(ctx context.Context, request *resource.StatusRequest, readFunc func(context.Context, *resource.ReadRequest) (*resource.ReadResult, error)) (*resource.StatusResult, error) {
	args := m.Called(ctx, request, readFunc)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*resource.StatusResult), args.Error(1)
}
