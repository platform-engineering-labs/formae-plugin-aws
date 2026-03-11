// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package elasticbeanstalk

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/service/elasticbeanstalk"
	"github.com/stretchr/testify/mock"
)

type mockEBClient struct {
	mock.Mock
}

func (m *mockEBClient) UpdateConfigurationTemplate(ctx context.Context, input *elasticbeanstalk.UpdateConfigurationTemplateInput, optFns ...func(*elasticbeanstalk.Options)) (*elasticbeanstalk.UpdateConfigurationTemplateOutput, error) {
	args := m.Called(ctx, input)
	return args.Get(0).(*elasticbeanstalk.UpdateConfigurationTemplateOutput), args.Error(1)
}

func (m *mockEBClient) DescribeConfigurationSettings(ctx context.Context, input *elasticbeanstalk.DescribeConfigurationSettingsInput, optFns ...func(*elasticbeanstalk.Options)) (*elasticbeanstalk.DescribeConfigurationSettingsOutput, error) {
	args := m.Called(ctx, input)
	return args.Get(0).(*elasticbeanstalk.DescribeConfigurationSettingsOutput), args.Error(1)
}

func matchAppAndTemplate(appName, templateName string) any {
	return mock.MatchedBy(func(input *elasticbeanstalk.UpdateConfigurationTemplateInput) bool {
		return input.ApplicationName != nil && *input.ApplicationName == appName &&
			input.TemplateName != nil && *input.TemplateName == templateName
	})
}
