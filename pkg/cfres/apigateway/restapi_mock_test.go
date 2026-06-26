// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package apigateway

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/stretchr/testify/mock"
)

// resetIdentityCache clears the package-level caller-identity memo so each test
// starts from a cold cache.
func resetIdentityCache() {
	identityCacheMu.Lock()
	defer identityCacheMu.Unlock()
	identityCache = map[identityCacheKey]callerIdentity{}
}

type mockStsClient struct {
	mock.Mock
}

func (m *mockStsClient) GetCallerIdentity(ctx context.Context, input *sts.GetCallerIdentityInput, optFns ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error) {
	args := m.Called(ctx, input)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*sts.GetCallerIdentityOutput), args.Error(1)
}
