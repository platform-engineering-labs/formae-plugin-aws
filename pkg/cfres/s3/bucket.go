// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package s3

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/ccx"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres/prov"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres/registry"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/config"
)

// Bucket wraps CloudControl's Read for AWS::S3::Bucket to derive a
// hostname-only WebsiteEndpoint from the WebsiteURL that CCAPI returns.
// CloudFront's Distribution.Origin.domainName wants a hostname, not a
// URL; the CFN GetAtt schema only exposes WebsiteURL (with the http://
// prefix), so without this enrichment operators have to string-compose
// the hostname themselves and lose the Resolvable edge.
//
// All other operations (Create / Update / Delete / List / Status) fall
// through to CCAPI because only the Read result needs enrichment.
type Bucket struct {
	cfg *config.Config
}

var _ prov.Provisioner = &Bucket{}

func init() {
	registry.Register("AWS::S3::Bucket",
		[]resource.Operation{resource.OperationRead},
		func(cfg *config.Config) prov.Provisioner {
			return &Bucket{cfg: cfg}
		})
}

func (b *Bucket) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	client, err := ccx.NewClient(b.cfg)
	if err != nil {
		return nil, err
	}
	result, err := client.ReadResource(ctx, request)
	if err != nil || result == nil || result.Properties == "" {
		return result, err
	}

	var props map[string]any
	if err := json.Unmarshal([]byte(result.Properties), &props); err != nil {
		// Pass through; CCAPI's representation is the source of truth.
		return result, nil
	}
	enrichBucketProperties(props)

	enriched, err := json.Marshal(props)
	if err != nil {
		return nil, fmt.Errorf("s3 bucket: re-marshal enriched properties: %w", err)
	}
	result.Properties = string(enriched)
	return result, nil
}

// enrichBucketProperties derives WebsiteEndpoint from WebsiteURL by
// stripping the `http://` scheme and any trailing slash. Idempotent: if
// WebsiteURL is missing or empty (bucket without website configuration),
// no key is added.
func enrichBucketProperties(props map[string]any) {
	raw, ok := props["WebsiteURL"].(string)
	if !ok || raw == "" {
		return
	}
	endpoint := strings.TrimSuffix(strings.TrimPrefix(raw, "http://"), "/")
	if endpoint == "" {
		return
	}
	props["WebsiteEndpoint"] = endpoint
}

// The remaining operations fall through to CCAPI; they are unimplemented
// here so the dispatcher in aws.go bypasses this provisioner for them.

func (b *Bucket) Create(ctx context.Context, request *resource.CreateRequest) (*resource.CreateResult, error) {
	return nil, fmt.Errorf("s3 bucket: create handled by cloudcontrol")
}

func (b *Bucket) Update(ctx context.Context, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	return nil, fmt.Errorf("s3 bucket: update handled by cloudcontrol")
}

func (b *Bucket) Delete(ctx context.Context, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	return nil, fmt.Errorf("s3 bucket: delete handled by cloudcontrol")
}

func (b *Bucket) Status(ctx context.Context, request *resource.StatusRequest) (*resource.StatusResult, error) {
	return nil, fmt.Errorf("s3 bucket: status handled by cloudcontrol")
}

func (b *Bucket) List(ctx context.Context, request *resource.ListRequest) (*resource.ListResult, error) {
	return nil, fmt.Errorf("s3 bucket: list handled by cloudcontrol")
}
