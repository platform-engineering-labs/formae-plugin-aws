// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package cloudfront

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudfront"
	cftypes "github.com/aws/aws-sdk-go-v2/service/cloudfront/types"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres/prov"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres/registry"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/config"
)

// Function is the AWS::CloudFront::Function provisioner. We only take
// over Update; Create/Read/Delete/List/Status fall through to CCAPI.
//
// CCAPI's generic Update for AWS::CloudFront::Function silently drops
// FunctionCode: the Update call reports Success after ~44s but neither
// AWS nor the agent's DB reflects the new code. We replace it with a
// direct SDK call: DescribeFunction (capture ETag) -> UpdateFunction
// (with FunctionCode + IfMatch) -> PublishFunction (if AutoPublish).
type Function struct {
	cfg                     *config.Config
	cloudFrontClientFactory func(cfg *config.Config) (CloudFrontClientInterface, error)
}

var _ prov.Provisioner = &Function{}

func init() {
	registry.Register("AWS::CloudFront::Function",
		[]resource.Operation{resource.OperationUpdate},
		func(cfg *config.Config) prov.Provisioner {
			return &Function{
				cfg:                     cfg,
				cloudFrontClientFactory: defaultCloudFrontClientFactory,
			}
		})
}

func (f *Function) Update(ctx context.Context, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	client, err := f.cloudFrontClientFactory(f.cfg)
	if err != nil {
		return nil, err
	}

	var desired map[string]any
	if len(request.DesiredProperties) > 0 {
		if err := json.Unmarshal(request.DesiredProperties, &desired); err != nil {
			return nil, fmt.Errorf("cloudfront function: parse desired properties: %w", err)
		}
	}

	name, _ := desired["Name"].(string)
	if name == "" {
		return nil, fmt.Errorf("cloudfront function: Name missing from desired properties")
	}

	// 1. DescribeFunction (DEVELOPMENT stage) for the current ETag.
	descResp, err := client.DescribeFunction(ctx, &cloudfront.DescribeFunctionInput{
		Name: aws.String(name),
	})
	if err != nil {
		return nil, fmt.Errorf("cloudfront function: DescribeFunction: %w", err)
	}
	if descResp == nil || descResp.ETag == nil {
		return nil, fmt.Errorf("cloudfront function: DescribeFunction returned no ETag for %s", name)
	}

	functionCode, _ := desired["FunctionCode"].(string)
	if functionCode == "" {
		return nil, fmt.Errorf("cloudfront function: FunctionCode missing from desired properties")
	}

	functionConfig, err := functionConfigFromDesired(desired["FunctionConfig"])
	if err != nil {
		return nil, err
	}

	// 2. UpdateFunction with new code + config + IfMatch.
	updResp, err := client.UpdateFunction(ctx, &cloudfront.UpdateFunctionInput{
		Name:           aws.String(name),
		IfMatch:        descResp.ETag,
		FunctionCode:   []byte(functionCode),
		FunctionConfig: functionConfig,
	})
	if err != nil {
		return nil, fmt.Errorf("cloudfront function: UpdateFunction: %w", err)
	}
	if updResp == nil || updResp.ETag == nil {
		return nil, fmt.Errorf("cloudfront function: UpdateFunction returned no ETag for %s", name)
	}

	// 3. PublishFunction if AutoPublish — uses the post-update ETag,
	//    because UpdateFunction bumped the version. Track which stage
	//    we read back from so the result reflects the user-facing code.
	stage := cftypes.FunctionStageDevelopment
	if autoPublish, ok := desired["AutoPublish"].(bool); ok && autoPublish {
		if _, err := client.PublishFunction(ctx, &cloudfront.PublishFunctionInput{
			Name:    aws.String(name),
			IfMatch: updResp.ETag,
		}); err != nil {
			return nil, fmt.Errorf("cloudfront function: PublishFunction: %w", err)
		}
		stage = cftypes.FunctionStageLive
	}

	// 4. Read back actual AWS state and use it as ResourceProperties.
	//    Returning DesiredProperties verbatim would mask silent
	//    UpdateFunction failures (Success reported, function code
	//    unchanged on AWS) — the agent's DB would diverge from reality
	//    until the next periodic sync.
	props, err := readFunctionProperties(ctx, client, name, stage, request.NativeID)
	if err != nil {
		return nil, err
	}
	propsBytes, err := json.Marshal(props)
	if err != nil {
		return nil, fmt.Errorf("cloudfront function: marshal readback: %w", err)
	}

	return &resource.UpdateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationUpdate,
			OperationStatus:    resource.OperationStatusSuccess,
			NativeID:           request.NativeID,
			ResourceProperties: propsBytes,
		},
	}, nil
}

// readFunctionProperties reads the function's actual state from AWS at
// the given stage and returns a property map matching the CFN schema
// shape. Used to populate the Update result so the agent's DB tracks
// AWS state (not the operator's intent).
func readFunctionProperties(ctx context.Context, client CloudFrontClientInterface, name string, stage cftypes.FunctionStage, nativeID string) (map[string]any, error) {
	getResp, err := client.GetFunction(ctx, &cloudfront.GetFunctionInput{
		Name:  aws.String(name),
		Stage: stage,
	})
	if err != nil {
		return nil, fmt.Errorf("cloudfront function: GetFunction(%s): %w", stage, err)
	}
	descResp, err := client.DescribeFunction(ctx, &cloudfront.DescribeFunctionInput{
		Name:  aws.String(name),
		Stage: stage,
	})
	if err != nil {
		return nil, fmt.Errorf("cloudfront function: DescribeFunction(%s) for readback: %w", stage, err)
	}

	props := map[string]any{
		"Name":         name,
		"FunctionCode": string(getResp.FunctionCode),
		"FunctionARN":  nativeID,
	}
	if descResp.FunctionSummary != nil && descResp.FunctionSummary.FunctionConfig != nil {
		cfg := descResp.FunctionSummary.FunctionConfig
		fc := map[string]any{}
		if cfg.Comment != nil {
			fc["Comment"] = *cfg.Comment
		}
		if cfg.Runtime != "" {
			fc["Runtime"] = string(cfg.Runtime)
		}
		if cfg.KeyValueStoreAssociations != nil && len(cfg.KeyValueStoreAssociations.Items) > 0 {
			var items []map[string]any
			for _, kva := range cfg.KeyValueStoreAssociations.Items {
				if kva.KeyValueStoreARN != nil {
					items = append(items, map[string]any{"KeyValueStoreARN": *kva.KeyValueStoreARN})
				}
			}
			if len(items) > 0 {
				fc["KeyValueStoreAssociations"] = items
			}
		}
		props["FunctionConfig"] = fc
	}
	return props, nil
}

func functionConfigFromDesired(raw any) (*cftypes.FunctionConfig, error) {
	m, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("cloudfront function: FunctionConfig missing or wrong type")
	}
	cfg := &cftypes.FunctionConfig{}
	if c, ok := m["Comment"].(string); ok {
		cfg.Comment = aws.String(c)
	}
	if r, ok := m["Runtime"].(string); ok {
		cfg.Runtime = cftypes.FunctionRuntime(r)
	}
	if assocsRaw, ok := m["KeyValueStoreAssociations"]; ok {
		assocs := keyValueStoreAssociationsFromDesired(assocsRaw)
		if assocs != nil {
			cfg.KeyValueStoreAssociations = assocs
		}
	}
	if cfg.Comment == nil || cfg.Runtime == "" {
		return nil, fmt.Errorf("cloudfront function: FunctionConfig requires Comment and Runtime")
	}
	return cfg, nil
}

func keyValueStoreAssociationsFromDesired(raw any) *cftypes.KeyValueStoreAssociations {
	items, ok := raw.([]any)
	if !ok || len(items) == 0 {
		// Also accept the wrapped {Items, Quantity} form CCAPI may produce.
		if wrap, ok := raw.(map[string]any); ok {
			if its, ok := wrap["Items"].([]any); ok {
				items = its
			}
		}
		if len(items) == 0 {
			return nil
		}
	}
	var out []cftypes.KeyValueStoreAssociation
	for _, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		arn, _ := m["KeyValueStoreARN"].(string)
		if arn == "" {
			continue
		}
		out = append(out, cftypes.KeyValueStoreAssociation{
			KeyValueStoreARN: aws.String(arn),
		})
	}
	if len(out) == 0 {
		return nil
	}
	q := int32(len(out))
	return &cftypes.KeyValueStoreAssociations{
		Items:    out,
		Quantity: &q,
	}
}

// ----- Unused operations (CCAPI handles them) -----

func (f *Function) Create(_ context.Context, _ *resource.CreateRequest) (*resource.CreateResult, error) {
	return nil, fmt.Errorf("cloudfront function: create handled by cloudcontrol")
}

func (f *Function) Read(_ context.Context, _ *resource.ReadRequest) (*resource.ReadResult, error) {
	return nil, fmt.Errorf("cloudfront function: read handled by cloudcontrol")
}

func (f *Function) Delete(_ context.Context, _ *resource.DeleteRequest) (*resource.DeleteResult, error) {
	return nil, fmt.Errorf("cloudfront function: delete handled by cloudcontrol")
}

func (f *Function) Status(_ context.Context, _ *resource.StatusRequest) (*resource.StatusResult, error) {
	return nil, fmt.Errorf("cloudfront function: status handled by cloudcontrol")
}

func (f *Function) List(_ context.Context, _ *resource.ListRequest) (*resource.ListResult, error) {
	return nil, fmt.Errorf("cloudfront function: list handled by cloudcontrol")
}
