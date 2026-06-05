// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package cloudfront

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudfront"
	cftypes "github.com/aws/aws-sdk-go-v2/service/cloudfront/types"
	jsonpatch "github.com/evanphx/json-patch/v5"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres/prov"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres/registry"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/config"
)

// Distribution is the AWS::CloudFront::Distribution provisioner. We
// only take over Update; Create/Read/Delete/List/Status fall through to
// CCAPI.
//
// CloudFront's UpdateDistribution API requires the FULL DistributionConfig
// payload on every call: fields not in the payload are reset to defaults,
// and ViewerCertificate enforces an "exactly one of [AcmCertificateArn,
// CloudFrontDefaultCertificate, IamCertificateId]" rule that hard-fails
// when CCAPI sends a diff-only payload (e.g. just a Comment change).
//
// We fetch the live DistributionConfig + ETag, apply the operator's
// JSON Patch on top of it, and submit the merged payload via the
// CloudFront SDK directly. Tags are handled separately via
// TagResource/UntagResource on the distribution ARN.
type Distribution struct {
	cfg                     *config.Config
	cloudFrontClientFactory func(cfg *config.Config) (CloudFrontClientInterface, error)
}

var _ prov.Provisioner = &Distribution{}

func init() {
	registry.Register("AWS::CloudFront::Distribution",
		[]resource.Operation{resource.OperationUpdate},
		func(cfg *config.Config) prov.Provisioner {
			return &Distribution{
				cfg:                     cfg,
				cloudFrontClientFactory: defaultCloudFrontClientFactory,
			}
		})
}

func defaultCloudFrontClientFactory(cfg *config.Config) (CloudFrontClientInterface, error) {
	awsCfg, err := cfg.ToAwsConfig(context.Background())
	if err != nil {
		return nil, fmt.Errorf("cloudfront: build AWS config: %w", err)
	}
	return cloudfront.NewFromConfig(awsCfg), nil
}

// ----- Update -----

func (d *Distribution) Update(ctx context.Context, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	client, err := d.cloudFrontClientFactory(d.cfg)
	if err != nil {
		return nil, err
	}

	// 1. Fetch live DistributionConfig and ETag.
	liveResp, err := client.GetDistributionConfig(ctx, &cloudfront.GetDistributionConfigInput{
		Id: aws.String(request.NativeID),
	})
	if err != nil {
		return nil, fmt.Errorf("cloudfront: GetDistributionConfig: %w", err)
	}
	if liveResp == nil || liveResp.DistributionConfig == nil || liveResp.ETag == nil {
		return nil, fmt.Errorf("cloudfront: GetDistributionConfig returned empty body for %s", request.NativeID)
	}

	// 2. Merge desired-state diff onto live config.
	mergedCfg, err := mergeDistributionConfig(liveResp.DistributionConfig, request.PatchDocument, request.DesiredProperties)
	if err != nil {
		return nil, err
	}

	// 3. Submit UpdateDistribution with captured ETag.
	if _, err := client.UpdateDistribution(ctx, &cloudfront.UpdateDistributionInput{
		Id:                 aws.String(request.NativeID),
		IfMatch:            liveResp.ETag,
		DistributionConfig: mergedCfg,
	}); err != nil {
		return nil, fmt.Errorf("cloudfront: UpdateDistribution: %w", err)
	}

	// 4. Sync tags (TagResource / UntagResource).
	if err := d.syncDistributionTags(ctx, client, request); err != nil {
		return nil, err
	}

	// 5. Build the response. We don't re-read here; the immediate caller
	//    only needs Success + the desired properties echoed back. The
	//    background sync will refresh from AWS shortly.
	return &resource.UpdateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationUpdate,
			OperationStatus:    resource.OperationStatusSuccess,
			NativeID:           request.NativeID,
			ResourceProperties: request.DesiredProperties,
		},
	}, nil
}

// mergeDistributionConfig applies the operator's JSON Patch onto the
// live DistributionConfig and returns the merged config as the SDK
// type. The envelope wraps the live config under a "DistributionConfig"
// key so patch paths like "/DistributionConfig/Comment" resolve
// correctly.
//
// When no PatchDocument is provided we fall back to the DesiredProperties
// snapshot — this is the path the synchronizer takes if it ever invokes
// Update without a precomputed diff.
func mergeDistributionConfig(liveCfg *cftypes.DistributionConfig, patchDoc *string, desired json.RawMessage) (*cftypes.DistributionConfig, error) {
	liveJSON, err := json.Marshal(liveCfg)
	if err != nil {
		return nil, fmt.Errorf("cloudfront: marshal live DistributionConfig: %w", err)
	}

	envelope := map[string]json.RawMessage{"DistributionConfig": liveJSON}
	envelopeJSON, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("cloudfront: marshal envelope: %w", err)
	}

	if patchDoc != nil && *patchDoc != "" {
		filtered, ferr := filterPatchToDistributionConfig(*patchDoc)
		if ferr != nil {
			return nil, ferr
		}
		if len(filtered) > 0 {
			patch, perr := jsonpatch.DecodePatch(filtered)
			if perr != nil {
				return nil, fmt.Errorf("cloudfront: decode patch: %w", perr)
			}
			envelopeJSON, perr = patch.Apply(envelopeJSON)
			if perr != nil {
				return nil, fmt.Errorf("cloudfront: apply patch: %w", perr)
			}
		}
	} else if len(desired) > 0 {
		// No patch — overlay DesiredProperties.DistributionConfig if present.
		var desiredProps map[string]json.RawMessage
		if uerr := json.Unmarshal(desired, &desiredProps); uerr == nil {
			if cfgRaw, ok := desiredProps["DistributionConfig"]; ok && len(cfgRaw) > 0 {
				envelope["DistributionConfig"] = cfgRaw
				envelopeJSON, _ = json.Marshal(envelope)
			}
		}
	}

	var merged struct {
		DistributionConfig json.RawMessage `json:"DistributionConfig"`
	}
	if err := json.Unmarshal(envelopeJSON, &merged); err != nil {
		return nil, fmt.Errorf("cloudfront: unmarshal merged envelope: %w", err)
	}

	var mergedCfg cftypes.DistributionConfig
	if err := json.Unmarshal(merged.DistributionConfig, &mergedCfg); err != nil {
		return nil, fmt.Errorf("cloudfront: unmarshal merged DistributionConfig: %w", err)
	}
	return &mergedCfg, nil
}

// filterPatchToDistributionConfig drops any patch ops that don't target
// /DistributionConfig (e.g. /Tags/... is handled separately).
func filterPatchToDistributionConfig(patchDoc string) ([]byte, error) {
	var ops []map[string]any
	if err := json.Unmarshal([]byte(patchDoc), &ops); err != nil {
		return nil, fmt.Errorf("cloudfront: parse patch: %w", err)
	}
	filtered := make([]map[string]any, 0, len(ops))
	for _, op := range ops {
		path, _ := op["path"].(string)
		if len(path) >= len("/DistributionConfig") && path[:len("/DistributionConfig")] == "/DistributionConfig" {
			filtered = append(filtered, op)
		}
	}
	if len(filtered) == 0 {
		return nil, nil
	}
	out, err := json.Marshal(filtered)
	if err != nil {
		return nil, fmt.Errorf("cloudfront: marshal filtered patch: %w", err)
	}
	return out, nil
}

// syncDistributionTags diffs prior vs desired tags and issues
// TagResource / UntagResource as needed. The distribution ARN is
// resolved via GetDistribution if not present in the desired properties.
func (d *Distribution) syncDistributionTags(ctx context.Context, client CloudFrontClientInterface, request *resource.UpdateRequest) error {
	priorTags := tagSetFromProperties(request.PriorProperties)
	desiredTags := tagSetFromProperties(request.DesiredProperties)

	toAdd, toRemove := diffTagSets(priorTags, desiredTags)
	if len(toAdd) == 0 && len(toRemove) == 0 {
		return nil
	}

	arn, err := resolveDistributionARN(ctx, client, request)
	if err != nil {
		return err
	}

	if len(toRemove) > 0 {
		keys := make([]string, 0, len(toRemove))
		for _, t := range toRemove {
			keys = append(keys, *t.Key)
		}
		sort.Strings(keys)
		if _, err := client.UntagResource(ctx, &cloudfront.UntagResourceInput{
			Resource: aws.String(arn),
			TagKeys:  &cftypes.TagKeys{Items: keys},
		}); err != nil {
			return fmt.Errorf("cloudfront: UntagResource: %w", err)
		}
	}
	if len(toAdd) > 0 {
		sort.Slice(toAdd, func(i, j int) bool {
			return aws.ToString(toAdd[i].Key) < aws.ToString(toAdd[j].Key)
		})
		if _, err := client.TagResource(ctx, &cloudfront.TagResourceInput{
			Resource: aws.String(arn),
			Tags:     &cftypes.Tags{Items: toAdd},
		}); err != nil {
			return fmt.Errorf("cloudfront: TagResource: %w", err)
		}
	}
	return nil
}

func resolveDistributionARN(ctx context.Context, client CloudFrontClientInterface, request *resource.UpdateRequest) (string, error) {
	var desired map[string]any
	if len(request.DesiredProperties) > 0 {
		_ = json.Unmarshal(request.DesiredProperties, &desired)
	}
	if arn, ok := desired["Arn"].(string); ok && arn != "" {
		return arn, nil
	}
	// Fall back to GetDistribution.
	resp, err := client.GetDistribution(ctx, &cloudfront.GetDistributionInput{Id: aws.String(request.NativeID)})
	if err != nil {
		return "", fmt.Errorf("cloudfront: GetDistribution for ARN lookup: %w", err)
	}
	if resp == nil || resp.Distribution == nil || resp.Distribution.ARN == nil {
		return "", fmt.Errorf("cloudfront: distribution %s has no ARN", request.NativeID)
	}
	return *resp.Distribution.ARN, nil
}

// tagSetFromProperties parses a "Tags" array-of-{Key,Value} property
// into a key->value map.
func tagSetFromProperties(raw json.RawMessage) map[string]string {
	out := map[string]string{}
	if len(raw) == 0 {
		return out
	}
	var props map[string]any
	if err := json.Unmarshal(raw, &props); err != nil {
		return out
	}
	tags, ok := props["Tags"].([]any)
	if !ok {
		return out
	}
	for _, r := range tags {
		m, ok := r.(map[string]any)
		if !ok {
			continue
		}
		k, _ := m["Key"].(string)
		if k == "" {
			continue
		}
		v, _ := m["Value"].(string)
		out[k] = v
	}
	return out
}

func diffTagSets(prior, desired map[string]string) (toAdd []cftypes.Tag, toRemove []cftypes.Tag) {
	for k, dv := range desired {
		pv, present := prior[k]
		if !present || pv != dv {
			toAdd = append(toAdd, cftypes.Tag{Key: aws.String(k), Value: aws.String(dv)})
		}
	}
	for k, pv := range prior {
		if _, present := desired[k]; !present {
			toRemove = append(toRemove, cftypes.Tag{Key: aws.String(k), Value: aws.String(pv)})
		}
	}
	return toAdd, toRemove
}

// ----- Unused operations (CCAPI handles them) -----

func (d *Distribution) Create(_ context.Context, _ *resource.CreateRequest) (*resource.CreateResult, error) {
	return nil, fmt.Errorf("cloudfront distribution: create handled by cloudcontrol")
}

func (d *Distribution) Read(_ context.Context, _ *resource.ReadRequest) (*resource.ReadResult, error) {
	return nil, fmt.Errorf("cloudfront distribution: read handled by cloudcontrol")
}

func (d *Distribution) Delete(_ context.Context, _ *resource.DeleteRequest) (*resource.DeleteResult, error) {
	return nil, fmt.Errorf("cloudfront distribution: delete handled by cloudcontrol")
}

func (d *Distribution) Status(_ context.Context, _ *resource.StatusRequest) (*resource.StatusResult, error) {
	return nil, fmt.Errorf("cloudfront distribution: status handled by cloudcontrol")
}

func (d *Distribution) List(_ context.Context, _ *resource.ListRequest) (*resource.ListResult, error) {
	return nil, fmt.Errorf("cloudfront distribution: list handled by cloudcontrol")
}
