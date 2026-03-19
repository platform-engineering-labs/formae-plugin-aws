// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package elasticbeanstalk

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	eb "github.com/aws/aws-sdk-go-v2/service/elasticbeanstalk"
	ebtypes "github.com/aws/aws-sdk-go-v2/service/elasticbeanstalk/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/config"
)

func TestConfigurationTemplate_Update_Success(t *testing.T) {
	ctx := context.Background()
	client := &mockEBClient{}

	client.On("UpdateConfigurationTemplate", ctx, matchAppAndTemplate("my-app", "my-template")).Return(
		&eb.UpdateConfigurationTemplateOutput{
			ApplicationName: strPtr("my-app"),
			TemplateName:    strPtr("my-template"),
			Description:     strPtr("updated description"),
		}, nil,
	)

	ct := &ConfigurationTemplate{cfg: &config.Config{}}
	desired := map[string]any{
		"ApplicationName": "my-app",
		"Description":     "updated description",
	}
	desiredJSON, _ := json.Marshal(desired)

	result, err := ct.updateWithClient(ctx, client, &resource.UpdateRequest{
		NativeID:          "my-app|my-template",
		ResourceType:      "AWS::ElasticBeanstalk::ConfigurationTemplate",
		DesiredProperties: desiredJSON,
	})

	assert.NoError(t, err)
	assert.Equal(t, resource.OperationStatusSuccess, result.ProgressResult.OperationStatus)
	assert.Equal(t, "my-app|my-template", result.ProgressResult.NativeID)
	client.AssertExpectations(t)
}

func TestConfigurationTemplate_Update_InvalidNativeID(t *testing.T) {
	ctx := context.Background()
	client := &mockEBClient{}

	ct := &ConfigurationTemplate{cfg: &config.Config{}}
	result, err := ct.updateWithClient(ctx, client, &resource.UpdateRequest{
		NativeID:          "no-pipe-separator",
		ResourceType:      "AWS::ElasticBeanstalk::ConfigurationTemplate",
		DesiredProperties: json.RawMessage(`{"Description":"test"}`),
	})

	assert.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "invalid NativeID")
}

func TestConfigurationTemplate_Update_APIError(t *testing.T) {
	ctx := context.Background()
	client := &mockEBClient{}

	client.On("UpdateConfigurationTemplate", ctx, matchAppAndTemplate("my-app", "my-template")).Return(
		(*eb.UpdateConfigurationTemplateOutput)(nil), fmt.Errorf("throttling exception"),
	)

	ct := &ConfigurationTemplate{cfg: &config.Config{}}
	desired := map[string]any{
		"Description": "new desc",
	}
	desiredJSON, _ := json.Marshal(desired)

	result, err := ct.updateWithClient(ctx, client, &resource.UpdateRequest{
		NativeID:          "my-app|my-template",
		ResourceType:      "AWS::ElasticBeanstalk::ConfigurationTemplate",
		DesiredProperties: desiredJSON,
	})

	assert.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "throttling exception")
	client.AssertExpectations(t)
}

func TestConfigurationTemplate_Update_WithOptionSettings(t *testing.T) {
	ctx := context.Background()
	client := &mockEBClient{}

	client.On("UpdateConfigurationTemplate", ctx, mock.MatchedBy(func(input *eb.UpdateConfigurationTemplateInput) bool {
		return input.ApplicationName != nil && *input.ApplicationName == "my-app" &&
			input.TemplateName != nil && *input.TemplateName == "my-template" &&
			len(input.OptionSettings) == 1 &&
			*input.OptionSettings[0].Namespace == "aws:autoscaling:asg" &&
			*input.OptionSettings[0].OptionName == "MinSize" &&
			*input.OptionSettings[0].Value == "2"
	})).Return(
		&eb.UpdateConfigurationTemplateOutput{
			ApplicationName: strPtr("my-app"),
			TemplateName:    strPtr("my-template"),
		}, nil,
	)

	ct := &ConfigurationTemplate{cfg: &config.Config{}}
	desired := map[string]any{
		"OptionSettings": []any{
			map[string]any{
				"Namespace":  "aws:autoscaling:asg",
				"OptionName": "MinSize",
				"Value":      "2",
			},
		},
	}
	desiredJSON, _ := json.Marshal(desired)

	result, err := ct.updateWithClient(ctx, client, &resource.UpdateRequest{
		NativeID:          "my-app|my-template",
		ResourceType:      "AWS::ElasticBeanstalk::ConfigurationTemplate",
		DesiredProperties: desiredJSON,
	})

	assert.NoError(t, err)
	assert.Equal(t, resource.OperationStatusSuccess, result.ProgressResult.OperationStatus)
	client.AssertExpectations(t)
}

func TestConfigurationTemplate_Read_Success(t *testing.T) {
	ctx := context.Background()
	client := &mockEBClient{}

	client.On("DescribeConfigurationSettings", ctx, mock.Anything).Return(
		&eb.DescribeConfigurationSettingsOutput{
			ConfigurationSettings: []ebtypes.ConfigurationSettingsDescription{
				{
					ApplicationName:  strPtr("my-app"),
					TemplateName:     strPtr("my-template"),
					SolutionStackName: strPtr("64bit Amazon Linux 2023 v4.11.0 running Docker"),
				},
			},
		}, nil,
	)

	ct := &ConfigurationTemplate{cfg: &config.Config{}}
	result, err := ct.readWithClient(ctx, client, &resource.ReadRequest{
		NativeID:     "my-app|my-template",
		ResourceType: "AWS::ElasticBeanstalk::ConfigurationTemplate",
	})

	assert.NoError(t, err)
	assert.Empty(t, result.ErrorCode)
	assert.Contains(t, result.Properties, "my-app")
	client.AssertExpectations(t)
}

func TestConfigurationTemplate_Read_NotFound(t *testing.T) {
	ctx := context.Background()
	client := &mockEBClient{}

	client.On("DescribeConfigurationSettings", ctx, mock.Anything).Return(
		(*eb.DescribeConfigurationSettingsOutput)(nil),
		&ebtypes.ResourceNotFoundException{Message: strPtr("template not found")},
	)

	ct := &ConfigurationTemplate{cfg: &config.Config{}}
	result, err := ct.readWithClient(ctx, client, &resource.ReadRequest{
		NativeID:     "my-app|my-template",
		ResourceType: "AWS::ElasticBeanstalk::ConfigurationTemplate",
	})

	assert.NoError(t, err)
	assert.Equal(t, resource.OperationErrorCodeNotFound, result.ErrorCode)
	client.AssertExpectations(t)
}

func TestConfigurationTemplate_Read_EmptySettings(t *testing.T) {
	ctx := context.Background()
	client := &mockEBClient{}

	client.On("DescribeConfigurationSettings", ctx, mock.Anything).Return(
		&eb.DescribeConfigurationSettingsOutput{
			ConfigurationSettings: []ebtypes.ConfigurationSettingsDescription{},
		}, nil,
	)

	ct := &ConfigurationTemplate{cfg: &config.Config{}}
	result, err := ct.readWithClient(ctx, client, &resource.ReadRequest{
		NativeID:     "my-app|my-template",
		ResourceType: "AWS::ElasticBeanstalk::ConfigurationTemplate",
	})

	assert.NoError(t, err)
	assert.Equal(t, resource.OperationErrorCodeNotFound, result.ErrorCode)
	client.AssertExpectations(t)
}

func TestConfigurationTemplate_Read_UnexpectedError(t *testing.T) {
	ctx := context.Background()
	client := &mockEBClient{}

	client.On("DescribeConfigurationSettings", ctx, mock.Anything).Return(
		(*eb.DescribeConfigurationSettingsOutput)(nil),
		fmt.Errorf("connection timeout"),
	)

	ct := &ConfigurationTemplate{cfg: &config.Config{}}
	result, err := ct.readWithClient(ctx, client, &resource.ReadRequest{
		NativeID:     "my-app|my-template",
		ResourceType: "AWS::ElasticBeanstalk::ConfigurationTemplate",
	})

	assert.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "connection timeout")
	client.AssertExpectations(t)
}

func strPtr(s string) *string {
	return &s
}
