// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package elasticloadbalancingv2

import (
	"context"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	"github.com/stretchr/testify/mock"
)

type mockResourceReader struct {
	mock.Mock
}

func (m *mockResourceReader) ReadResource(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	args := m.Called(ctx, request)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*resource.ReadResult), args.Error(1)
}
