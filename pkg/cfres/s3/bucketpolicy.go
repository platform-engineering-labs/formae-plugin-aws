// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package s3

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres/prov"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres/registry"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/config"
)

type s3ClientInterface interface {
	PutBucketPolicy(ctx context.Context, params *s3.PutBucketPolicyInput, optFns ...func(*s3.Options)) (*s3.PutBucketPolicyOutput, error)
}

type BucketPolicy struct {
	cfg *config.Config
}

var _ prov.Provisioner = &BucketPolicy{}

func init() {
	registry.Register("AWS::S3::BucketPolicy",
		[]resource.Operation{resource.OperationUpdate},
		func(cfg *config.Config) prov.Provisioner {
			return &BucketPolicy{cfg: cfg}
		})
}

func (bp *BucketPolicy) Update(ctx context.Context, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	awsCfg, err := bp.cfg.ToAwsConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading AWS config: %w", err)
	}
	client := s3.NewFromConfig(awsCfg)
	return bp.updateWithClient(ctx, client, request)
}

func (bp *BucketPolicy) updateWithClient(ctx context.Context, client s3ClientInterface, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	// NativeID is the bucket name
	bucketName := request.NativeID

	var desired map[string]any
	if err := json.Unmarshal(request.DesiredProperties, &desired); err != nil {
		return nil, fmt.Errorf("parsing desired properties: %w", err)
	}

	policyDoc, ok := desired["PolicyDocument"]
	if !ok {
		return nil, fmt.Errorf("policyDocument is required for update")
	}

	policyJSON, err := json.Marshal(policyDoc)
	if err != nil {
		return nil, fmt.Errorf("marshaling policy document: %w", err)
	}

	policyStr := string(policyJSON)
	if _, err := client.PutBucketPolicy(ctx, &s3.PutBucketPolicyInput{
		Bucket: &bucketName,
		Policy: &policyStr,
	}); err != nil {
		return nil, err
	}

	return &resource.UpdateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationUpdate,
			OperationStatus:    resource.OperationStatusSuccess,
			NativeID:           request.NativeID,
			ResourceProperties: json.RawMessage(request.DesiredProperties),
		},
	}, nil
}

func (bp *BucketPolicy) Create(_ context.Context, _ *resource.CreateRequest) (*resource.CreateResult, error) {
	return nil, fmt.Errorf("operation not implemented - cloudcontrol handles this")
}

func (bp *BucketPolicy) Read(_ context.Context, _ *resource.ReadRequest) (*resource.ReadResult, error) {
	return nil, fmt.Errorf("operation not implemented - cloudcontrol handles this")
}

func (bp *BucketPolicy) Delete(_ context.Context, _ *resource.DeleteRequest) (*resource.DeleteResult, error) {
	return nil, fmt.Errorf("operation not implemented - cloudcontrol handles this")
}

func (bp *BucketPolicy) Status(_ context.Context, _ *resource.StatusRequest) (*resource.StatusResult, error) {
	return nil, fmt.Errorf("operation not implemented - cloudcontrol handles this")
}

func (bp *BucketPolicy) List(_ context.Context, _ *resource.ListRequest) (*resource.ListResult, error) {
	return nil, fmt.Errorf("operation not implemented - cloudcontrol handles this")
}
