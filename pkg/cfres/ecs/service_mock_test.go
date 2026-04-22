// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package ecs

import (
	"context"

	"github.com/stretchr/testify/mock"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
)

type mockCCXReadClient struct {
	mock.Mock
}

func (m *mockCCXReadClient) ReadResource(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	args := m.Called(ctx, request)
	out, _ := args.Get(0).(*resource.ReadResult)
	return out, args.Error(1)
}
