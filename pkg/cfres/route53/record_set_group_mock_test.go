// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package route53

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/service/route53"
	"github.com/stretchr/testify/mock"
)

type mockRoute53Client struct {
	mock.Mock
}

func (m *mockRoute53Client) ChangeResourceRecordSets(ctx context.Context, input *route53.ChangeResourceRecordSetsInput, optFns ...func(*route53.Options)) (*route53.ChangeResourceRecordSetsOutput, error) {
	args := m.Called(ctx, input)
	out, _ := args.Get(0).(*route53.ChangeResourceRecordSetsOutput)
	return out, args.Error(1)
}

func (m *mockRoute53Client) ListResourceRecordSets(ctx context.Context, input *route53.ListResourceRecordSetsInput, optFns ...func(*route53.Options)) (*route53.ListResourceRecordSetsOutput, error) {
	args := m.Called(ctx, input)
	out, _ := args.Get(0).(*route53.ListResourceRecordSetsOutput)
	return out, args.Error(1)
}

func (m *mockRoute53Client) GetChange(ctx context.Context, input *route53.GetChangeInput, optFns ...func(*route53.Options)) (*route53.GetChangeOutput, error) {
	args := m.Called(ctx, input)
	out, _ := args.Get(0).(*route53.GetChangeOutput)
	return out, args.Error(1)
}
