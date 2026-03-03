// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package elasticbeanstalk

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/elasticbeanstalk"
	ebtypes "github.com/aws/aws-sdk-go-v2/service/elasticbeanstalk/types"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres/prov"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres/registry"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/config"
)

type ebClientInterface interface {
	UpdateConfigurationTemplate(ctx context.Context, params *elasticbeanstalk.UpdateConfigurationTemplateInput, optFns ...func(*elasticbeanstalk.Options)) (*elasticbeanstalk.UpdateConfigurationTemplateOutput, error)
}

type ConfigurationTemplate struct {
	cfg *config.Config
}

var _ prov.Provisioner = &ConfigurationTemplate{}

func init() {
	registry.Register("AWS::ElasticBeanstalk::ConfigurationTemplate",
		[]resource.Operation{resource.OperationUpdate},
		func(cfg *config.Config) prov.Provisioner {
			return &ConfigurationTemplate{cfg: cfg}
		})
}

func (ct *ConfigurationTemplate) Update(ctx context.Context, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	awsCfg, err := ct.cfg.ToAwsConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading AWS config: %w", err)
	}
	client := elasticbeanstalk.NewFromConfig(awsCfg)
	return ct.updateWithClient(ctx, client, request)
}

func (ct *ConfigurationTemplate) updateWithClient(ctx context.Context, client ebClientInterface, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	appName, templateName, err := parseNativeID(request.NativeID)
	if err != nil {
		return nil, err
	}

	var desired map[string]any
	if err := json.Unmarshal(request.DesiredProperties, &desired); err != nil {
		return nil, fmt.Errorf("parsing desired properties: %w", err)
	}

	input := &elasticbeanstalk.UpdateConfigurationTemplateInput{
		ApplicationName: &appName,
		TemplateName:    &templateName,
	}

	if desc, ok := desired["Description"]; ok {
		if s, ok := desc.(string); ok {
			input.Description = &s
		}
	}

	if settings, ok := desired["OptionSettings"]; ok {
		if arr, ok := settings.([]any); ok {
			for _, item := range arr {
				if m, ok := item.(map[string]any); ok {
					setting := ebtypes.ConfigurationOptionSetting{}
					if v, ok := m["Namespace"].(string); ok {
						setting.Namespace = &v
					}
					if v, ok := m["OptionName"].(string); ok {
						setting.OptionName = &v
					}
					if v, ok := m["Value"].(string); ok {
						setting.Value = &v
					}
					if v, ok := m["ResourceName"].(string); ok {
						setting.ResourceName = &v
					}
					input.OptionSettings = append(input.OptionSettings, setting)
				}
			}
		}
	}

	if _, err := client.UpdateConfigurationTemplate(ctx, input); err != nil {
		return nil, err
	}

	return &resource.UpdateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:       resource.OperationUpdate,
			OperationStatus: resource.OperationStatusSuccess,
			NativeID:        request.NativeID,
		},
	}, nil
}

func parseNativeID(nativeID string) (string, string, error) {
	parts := strings.SplitN(nativeID, "|", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("invalid NativeID %q: expected ApplicationName|TemplateName", nativeID)
	}
	return parts[0], parts[1], nil
}

func (ct *ConfigurationTemplate) Create(_ context.Context, _ *resource.CreateRequest) (*resource.CreateResult, error) {
	return nil, fmt.Errorf("operation not implemented - cloudcontrol handles this")
}

func (ct *ConfigurationTemplate) Read(_ context.Context, _ *resource.ReadRequest) (*resource.ReadResult, error) {
	return nil, fmt.Errorf("operation not implemented - cloudcontrol handles this")
}

func (ct *ConfigurationTemplate) Delete(_ context.Context, _ *resource.DeleteRequest) (*resource.DeleteResult, error) {
	return nil, fmt.Errorf("operation not implemented - cloudcontrol handles this")
}

func (ct *ConfigurationTemplate) Status(_ context.Context, _ *resource.StatusRequest) (*resource.StatusResult, error) {
	return nil, fmt.Errorf("operation not implemented - cloudcontrol handles this")
}

func (ct *ConfigurationTemplate) List(_ context.Context, _ *resource.ListRequest) (*resource.ListResult, error) {
	return nil, fmt.Errorf("operation not implemented - cloudcontrol handles this")
}
