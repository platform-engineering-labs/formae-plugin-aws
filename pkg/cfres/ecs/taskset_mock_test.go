// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package ecs

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/stretchr/testify/mock"
)

type mockECSTaskSetClient struct {
	mock.Mock
}

func (m *mockECSTaskSetClient) DescribeServices(ctx context.Context, input *ecs.DescribeServicesInput, optFns ...func(*ecs.Options)) (*ecs.DescribeServicesOutput, error) {
	args := m.Called(ctx, input)
	out, _ := args.Get(0).(*ecs.DescribeServicesOutput)
	return out, args.Error(1)
}
