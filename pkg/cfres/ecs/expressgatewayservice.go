// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package ecs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres/prov"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres/registry"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/config"
)

type ecsClientInterface interface {
	CreateExpressGatewayService(ctx context.Context, params *ecs.CreateExpressGatewayServiceInput, optFns ...func(*ecs.Options)) (*ecs.CreateExpressGatewayServiceOutput, error)
	DescribeExpressGatewayService(ctx context.Context, params *ecs.DescribeExpressGatewayServiceInput, optFns ...func(*ecs.Options)) (*ecs.DescribeExpressGatewayServiceOutput, error)
	UpdateExpressGatewayService(ctx context.Context, params *ecs.UpdateExpressGatewayServiceInput, optFns ...func(*ecs.Options)) (*ecs.UpdateExpressGatewayServiceOutput, error)
	DeleteExpressGatewayService(ctx context.Context, params *ecs.DeleteExpressGatewayServiceInput, optFns ...func(*ecs.Options)) (*ecs.DeleteExpressGatewayServiceOutput, error)
}

type ExpressGatewayService struct {
	cfg *config.Config
}

var _ prov.Provisioner = &ExpressGatewayService{}

func init() {
	registry.Register("AWS::ECS::ExpressGatewayService",
		[]resource.Operation{
			resource.OperationCreate,
			resource.OperationRead,
			resource.OperationUpdate,
			resource.OperationDelete,
			resource.OperationCheckStatus,
		},
		func(cfg *config.Config) prov.Provisioner {
			return &ExpressGatewayService{cfg: cfg}
		})
}

func (e *ExpressGatewayService) Create(ctx context.Context, request *resource.CreateRequest) (*resource.CreateResult, error) {
	awsCfg, err := e.cfg.ToAwsConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading AWS config: %w", err)
	}
	return e.createWithClient(ctx, ecs.NewFromConfig(awsCfg), request)
}

func (e *ExpressGatewayService) createWithClient(ctx context.Context, client ecsClientInterface, request *resource.CreateRequest) (*resource.CreateResult, error) {
	props, err := parseProperties(request.Properties)
	if err != nil {
		return nil, err
	}

	input := &ecs.CreateExpressGatewayServiceInput{
		ExecutionRoleArn:      aws.String(props.ExecutionRoleArn),
		InfrastructureRoleArn: aws.String(props.InfrastructureRoleArn),
		PrimaryContainer:      buildContainer(props.PrimaryContainer),
	}

	if props.Cluster != "" {
		input.Cluster = aws.String(props.Cluster)
	}
	if props.Cpu != "" {
		input.Cpu = aws.String(props.Cpu)
	}
	if props.Memory != "" {
		input.Memory = aws.String(props.Memory)
	}
	if props.HealthCheckPath != "" {
		input.HealthCheckPath = aws.String(props.HealthCheckPath)
	}
	if props.ServiceName != "" {
		input.ServiceName = aws.String(props.ServiceName)
	}
	if props.TaskRoleArn != "" {
		input.TaskRoleArn = aws.String(props.TaskRoleArn)
	}
	if props.ScalingTarget != nil {
		input.ScalingTarget = buildScalingTarget(props.ScalingTarget)
	}
	if props.NetworkConfiguration != nil {
		input.NetworkConfiguration = buildNetworkConfiguration(props.NetworkConfiguration)
	}
	if len(props.Tags) > 0 {
		input.Tags = buildTags(props.Tags)
	}

	output, err := client.CreateExpressGatewayService(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("creating express gateway service: %w", err)
	}

	svc := output.Service
	resultProps := serializeService(svc)
	resultJSON, _ := json.Marshal(resultProps)

	return &resource.CreateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationCreate,
			OperationStatus:    resource.OperationStatusSuccess,
			NativeID:           aws.ToString(svc.ServiceArn),
			ResourceProperties: resultJSON,
		},
	}, nil
}

func (e *ExpressGatewayService) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	awsCfg, err := e.cfg.ToAwsConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading AWS config: %w", err)
	}
	return e.readWithClient(ctx, ecs.NewFromConfig(awsCfg), request)
}

func (e *ExpressGatewayService) readWithClient(ctx context.Context, client ecsClientInterface, request *resource.ReadRequest) (*resource.ReadResult, error) {
	output, err := client.DescribeExpressGatewayService(ctx, &ecs.DescribeExpressGatewayServiceInput{
		ServiceArn: aws.String(request.NativeID),
	})
	if err != nil {
		var notFound *ecstypes.ServiceNotFoundException
		if errors.As(err, &notFound) {
			return &resource.ReadResult{
				ResourceType: request.ResourceType,
				ErrorCode:    resource.OperationErrorCodeNotFound,
			}, nil
		}
		return nil, fmt.Errorf("describing express gateway service: %w", err)
	}

	svc := output.Service

	// Treat DRAINING and INACTIVE as not found — the service is being or has been deleted
	if svc.Status != nil && (svc.Status.StatusCode == ecstypes.ExpressGatewayServiceStatusCodeDraining ||
		svc.Status.StatusCode == ecstypes.ExpressGatewayServiceStatusCodeInactive) {
		return &resource.ReadResult{
			ResourceType: request.ResourceType,
			ErrorCode:    resource.OperationErrorCodeNotFound,
		}, nil
	}

	resultProps := serializeService(svc)
	resultJSON, _ := json.Marshal(resultProps)

	return &resource.ReadResult{
		ResourceType: request.ResourceType,
		Properties:   string(resultJSON),
	}, nil
}

func (e *ExpressGatewayService) Update(ctx context.Context, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	awsCfg, err := e.cfg.ToAwsConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading AWS config: %w", err)
	}
	return e.updateWithClient(ctx, ecs.NewFromConfig(awsCfg), request)
}

func (e *ExpressGatewayService) updateWithClient(ctx context.Context, client ecsClientInterface, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	props, err := parseProperties(request.DesiredProperties)
	if err != nil {
		return nil, err
	}

	input := &ecs.UpdateExpressGatewayServiceInput{
		ServiceArn:       aws.String(request.NativeID),
		PrimaryContainer: buildContainer(props.PrimaryContainer),
	}

	if props.Cpu != "" {
		input.Cpu = aws.String(props.Cpu)
	}
	if props.Memory != "" {
		input.Memory = aws.String(props.Memory)
	}
	if props.HealthCheckPath != "" {
		input.HealthCheckPath = aws.String(props.HealthCheckPath)
	}
	if props.ExecutionRoleArn != "" {
		input.ExecutionRoleArn = aws.String(props.ExecutionRoleArn)
	}
	if props.TaskRoleArn != "" {
		input.TaskRoleArn = aws.String(props.TaskRoleArn)
	}
	if props.ScalingTarget != nil {
		input.ScalingTarget = buildScalingTarget(props.ScalingTarget)
	}
	if props.NetworkConfiguration != nil {
		input.NetworkConfiguration = buildNetworkConfiguration(props.NetworkConfiguration)
	}

	_, err = client.UpdateExpressGatewayService(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("updating express gateway service: %w", err)
	}

	// Read back the service to get the canonical state
	readResult, err := e.readWithClient(ctx, client, &resource.ReadRequest{
		NativeID:     request.NativeID,
		ResourceType: request.ResourceType,
	})
	if err != nil {
		return nil, fmt.Errorf("reading service after update: %w", err)
	}

	return &resource.UpdateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationUpdate,
			OperationStatus:    resource.OperationStatusSuccess,
			NativeID:           request.NativeID,
			ResourceProperties: json.RawMessage(readResult.Properties),
		},
	}, nil
}

func (e *ExpressGatewayService) Delete(ctx context.Context, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	awsCfg, err := e.cfg.ToAwsConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading AWS config: %w", err)
	}
	return e.deleteWithClient(ctx, ecs.NewFromConfig(awsCfg), request)
}

func (e *ExpressGatewayService) deleteWithClient(ctx context.Context, client ecsClientInterface, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	_, err := client.DeleteExpressGatewayService(ctx, &ecs.DeleteExpressGatewayServiceInput{
		ServiceArn: aws.String(request.NativeID),
	})
	if err != nil {
		var notFound *ecstypes.ServiceNotFoundException
		if errors.As(err, &notFound) {
			// Already deleted
			return &resource.DeleteResult{
				ProgressResult: &resource.ProgressResult{
					Operation:       resource.OperationDelete,
					OperationStatus: resource.OperationStatusSuccess,
					NativeID:        request.NativeID,
				},
			}, nil
		}
		return nil, fmt.Errorf("deleting express gateway service: %w", err)
	}

	return &resource.DeleteResult{
		ProgressResult: &resource.ProgressResult{
			Operation:       resource.OperationDelete,
			OperationStatus: resource.OperationStatusSuccess,
			NativeID:        request.NativeID,
		},
	}, nil
}

func (e *ExpressGatewayService) Status(_ context.Context, _ *resource.StatusRequest) (*resource.StatusResult, error) {
	return nil, fmt.Errorf("express gateway service operations are synchronous - status polling not needed")
}

func (e *ExpressGatewayService) List(_ context.Context, _ *resource.ListRequest) (*resource.ListResult, error) {
	return nil, fmt.Errorf("operation not implemented - express gateway service is not discoverable")
}

// Property types for JSON deserialization

type expressGatewayProperties struct {
	Cluster              string                `json:"Cluster,omitempty"`
	Cpu                  string                `json:"Cpu,omitempty"`
	ExecutionRoleArn     string                `json:"ExecutionRoleArn"`
	HealthCheckPath      string                `json:"HealthCheckPath,omitempty"`
	InfrastructureRoleArn string               `json:"InfrastructureRoleArn"`
	Memory               string                `json:"Memory,omitempty"`
	NetworkConfiguration *networkConfiguration `json:"NetworkConfiguration,omitempty"`
	PrimaryContainer     containerProps        `json:"PrimaryContainer"`
	ScalingTarget        *scalingTarget        `json:"ScalingTarget,omitempty"`
	ServiceName          string                `json:"ServiceName,omitempty"`
	Tags                 []tagProp             `json:"Tags,omitempty"`
	TaskRoleArn          string                `json:"TaskRoleArn,omitempty"`
}

type containerProps struct {
	Image                string           `json:"Image"`
	ContainerPort        *int32           `json:"ContainerPort,omitempty"`
	Command              []string         `json:"Command,omitempty"`
	Environment          []keyValuePair   `json:"Environment,omitempty"`
	Secrets              []secretProp     `json:"Secrets,omitempty"`
	AwsLogsConfiguration *awsLogsConfig   `json:"AwsLogsConfiguration,omitempty"`
	RepositoryCredentials *repoCreds      `json:"RepositoryCredentials,omitempty"`
}

type keyValuePair struct {
	Name  string `json:"Name"`
	Value string `json:"Value"`
}

type secretProp struct {
	Name      string `json:"Name"`
	ValueFrom string `json:"ValueFrom"`
}

type awsLogsConfig struct {
	LogGroup        string `json:"LogGroup"`
	LogStreamPrefix string `json:"LogStreamPrefix"`
}

type repoCreds struct {
	CredentialsParameter string `json:"CredentialsParameter"`
}

type scalingTarget struct {
	AutoScalingMetric      string `json:"AutoScalingMetric,omitempty"`
	AutoScalingTargetValue *int32 `json:"AutoScalingTargetValue,omitempty"`
	MaxTaskCount           *int32 `json:"MaxTaskCount,omitempty"`
	MinTaskCount           *int32 `json:"MinTaskCount,omitempty"`
}

type networkConfiguration struct {
	SecurityGroups []string `json:"SecurityGroups,omitempty"`
	Subnets        []string `json:"Subnets,omitempty"`
}

type tagProp struct {
	Key   string `json:"Key"`
	Value string `json:"Value"`
}

func parseProperties(raw json.RawMessage) (*expressGatewayProperties, error) {
	var props expressGatewayProperties
	if err := json.Unmarshal(raw, &props); err != nil {
		return nil, fmt.Errorf("parsing express gateway properties: %w", err)
	}
	return &props, nil
}

func buildContainer(c containerProps) *ecstypes.ExpressGatewayContainer {
	container := &ecstypes.ExpressGatewayContainer{
		Image: aws.String(c.Image),
	}
	if c.ContainerPort != nil {
		container.ContainerPort = c.ContainerPort
	}
	if len(c.Command) > 0 {
		container.Command = c.Command
	}
	for _, env := range c.Environment {
		container.Environment = append(container.Environment, ecstypes.KeyValuePair{
			Name:  aws.String(env.Name),
			Value: aws.String(env.Value),
		})
	}
	for _, s := range c.Secrets {
		container.Secrets = append(container.Secrets, ecstypes.Secret{
			Name:      aws.String(s.Name),
			ValueFrom: aws.String(s.ValueFrom),
		})
	}
	if c.AwsLogsConfiguration != nil {
		container.AwsLogsConfiguration = &ecstypes.ExpressGatewayServiceAwsLogsConfiguration{
			LogGroup:        aws.String(c.AwsLogsConfiguration.LogGroup),
			LogStreamPrefix: aws.String(c.AwsLogsConfiguration.LogStreamPrefix),
		}
	}
	if c.RepositoryCredentials != nil {
		container.RepositoryCredentials = &ecstypes.ExpressGatewayRepositoryCredentials{
			CredentialsParameter: aws.String(c.RepositoryCredentials.CredentialsParameter),
		}
	}
	return container
}

func buildScalingTarget(s *scalingTarget) *ecstypes.ExpressGatewayScalingTarget {
	target := &ecstypes.ExpressGatewayScalingTarget{}
	if s.AutoScalingMetric != "" {
		target.AutoScalingMetric = ecstypes.ExpressGatewayServiceScalingMetric(s.AutoScalingMetric)
	}
	if s.AutoScalingTargetValue != nil {
		target.AutoScalingTargetValue = s.AutoScalingTargetValue
	}
	if s.MaxTaskCount != nil {
		target.MaxTaskCount = s.MaxTaskCount
	}
	if s.MinTaskCount != nil {
		target.MinTaskCount = s.MinTaskCount
	}
	return target
}

func buildNetworkConfiguration(n *networkConfiguration) *ecstypes.ExpressGatewayServiceNetworkConfiguration {
	nc := &ecstypes.ExpressGatewayServiceNetworkConfiguration{}
	if len(n.SecurityGroups) > 0 {
		nc.SecurityGroups = n.SecurityGroups
	}
	if len(n.Subnets) > 0 {
		nc.Subnets = n.Subnets
	}
	return nc
}

func buildTags(tags []tagProp) []ecstypes.Tag {
	result := make([]ecstypes.Tag, len(tags))
	for i, t := range tags {
		result[i] = ecstypes.Tag{
			Key:   aws.String(t.Key),
			Value: aws.String(t.Value),
		}
	}
	return result
}

func serializeService(svc *ecstypes.ECSExpressGatewayService) map[string]any {
	result := map[string]any{
		"ServiceArn":  aws.ToString(svc.ServiceArn),
		"ServiceName": aws.ToString(svc.ServiceName),
		"Cluster":     aws.ToString(svc.Cluster),
	}

	if svc.InfrastructureRoleArn != nil {
		result["InfrastructureRoleArn"] = aws.ToString(svc.InfrastructureRoleArn)
	}

	if len(svc.ActiveConfigurations) > 0 {
		cfg := svc.ActiveConfigurations[0]
		if cfg.Cpu != nil {
			result["Cpu"] = aws.ToString(cfg.Cpu)
		}
		if cfg.Memory != nil {
			result["Memory"] = aws.ToString(cfg.Memory)
		}
		if cfg.HealthCheckPath != nil {
			result["HealthCheckPath"] = aws.ToString(cfg.HealthCheckPath)
		}
		if cfg.ExecutionRoleArn != nil {
			result["ExecutionRoleArn"] = aws.ToString(cfg.ExecutionRoleArn)
		}
		if cfg.TaskRoleArn != nil {
			result["TaskRoleArn"] = aws.ToString(cfg.TaskRoleArn)
		}
		if cfg.PrimaryContainer != nil {
			container := map[string]any{
				"Image": aws.ToString(cfg.PrimaryContainer.Image),
			}
			if cfg.PrimaryContainer.ContainerPort != nil {
				container["ContainerPort"] = *cfg.PrimaryContainer.ContainerPort
			}
			if len(cfg.PrimaryContainer.Command) > 0 {
				container["Command"] = cfg.PrimaryContainer.Command
			}
			if len(cfg.PrimaryContainer.Environment) > 0 {
				envs := make([]map[string]string, len(cfg.PrimaryContainer.Environment))
				for i, e := range cfg.PrimaryContainer.Environment {
					envs[i] = map[string]string{
						"Name":  aws.ToString(e.Name),
						"Value": aws.ToString(e.Value),
					}
				}
				container["Environment"] = envs
			}
			if len(cfg.PrimaryContainer.Secrets) > 0 {
				secrets := make([]map[string]string, len(cfg.PrimaryContainer.Secrets))
				for i, s := range cfg.PrimaryContainer.Secrets {
					secrets[i] = map[string]string{
						"Name":      aws.ToString(s.Name),
						"ValueFrom": aws.ToString(s.ValueFrom),
					}
				}
				container["Secrets"] = secrets
			}
			result["PrimaryContainer"] = container
		}
		if len(cfg.IngressPaths) > 0 && cfg.IngressPaths[0].Endpoint != nil {
			result["Endpoint"] = aws.ToString(cfg.IngressPaths[0].Endpoint)
		}
		if cfg.ScalingTarget != nil {
			scaling := map[string]any{}
			if cfg.ScalingTarget.MinTaskCount != nil {
				scaling["MinTaskCount"] = *cfg.ScalingTarget.MinTaskCount
			}
			if cfg.ScalingTarget.MaxTaskCount != nil {
				scaling["MaxTaskCount"] = *cfg.ScalingTarget.MaxTaskCount
			}
			if cfg.ScalingTarget.AutoScalingMetric != "" {
				scaling["AutoScalingMetric"] = string(cfg.ScalingTarget.AutoScalingMetric)
			}
			if cfg.ScalingTarget.AutoScalingTargetValue != nil {
				scaling["AutoScalingTargetValue"] = *cfg.ScalingTarget.AutoScalingTargetValue
			}
			result["ScalingTarget"] = scaling
		}
		if cfg.NetworkConfiguration != nil {
			nc := map[string]any{}
			if len(cfg.NetworkConfiguration.Subnets) > 0 {
				nc["Subnets"] = cfg.NetworkConfiguration.Subnets
			}
			if len(cfg.NetworkConfiguration.SecurityGroups) > 0 {
				nc["SecurityGroups"] = cfg.NetworkConfiguration.SecurityGroups
			}
			result["NetworkConfiguration"] = nc
		}
	}

	if len(svc.Tags) > 0 {
		tags := make([]map[string]string, len(svc.Tags))
		for i, t := range svc.Tags {
			tags[i] = map[string]string{
				"Key":   aws.ToString(t.Key),
				"Value": aws.ToString(t.Value),
			}
		}
		result["Tags"] = tags
	}

	return result
}
