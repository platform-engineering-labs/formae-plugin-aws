// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package apigateway

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/platform-engineering-labs/formae/pkg/plugin"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/ccx"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres/prov"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres/registry"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/config"
)

type Method struct {
	cfg *config.Config
}

var _ prov.Provisioner = &Method{}

func init() {
	registry.Register("AWS::ApiGateway::Method",
		[]resource.Operation{
			resource.OperationRead,
			resource.OperationCreate,
			resource.OperationUpdate,
			resource.OperationDelete},
		func(cfg *config.Config) prov.Provisioner {
			return &Method{cfg: cfg}
		})
}

func (m *Method) Create(ctx context.Context, request *resource.CreateRequest) (*resource.CreateResult, error) {
	transformedProperties, err := m.handleLambdaIntegration(ctx, request.Properties)
	if err != nil {
		plugin.LoggerFromContext(ctx).Error("ApiGateway::Method: Failed to transform Lambda integration", "error", err)
		return nil, err
	}

	request.Properties = transformedProperties

	ccxClient, err := ccx.NewClient(m.cfg)
	if err != nil {
		return nil, err
	}

	return ccxClient.CreateResource(ctx, request)
}

func (m *Method) Update(ctx context.Context, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	// CloudControl updates apply the patch document, not DesiredProperties, so the
	// Lambda integration transform has to run on the patch the same way Create
	// runs it on the properties.
	if request.PatchDocument != nil {
		transformedPatch, err := m.transformLambdaIntegrationPatch(*request.PatchDocument)
		if err != nil {
			plugin.LoggerFromContext(ctx).Error("ApiGateway::Method: Failed to transform Lambda integration patch", "error", err)
			return nil, err
		}
		request.PatchDocument = &transformedPatch
	}

	ccxClient, err := ccx.NewClient(m.cfg)
	if err != nil {
		return nil, err
	}
	return ccxClient.UpdateResource(ctx, request)
}

func (m *Method) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	ccxClient, err := ccx.NewClient(m.cfg)
	if err != nil {
		return nil, err
	}
	result, err := ccxClient.ReadResource(ctx, request)
	if err != nil || result == nil || result.Properties == "" {
		return result, err
	}

	normalized, err := normalizeIntegrationOnRead(result.Properties)
	if err != nil {
		// Pass through; CloudControl's representation is the source of truth.
		plugin.LoggerFromContext(ctx).Warn("ApiGateway::Method: failed to normalize Integration on read; passing through", "error", err)
		return result, nil
	}
	result.Properties = normalized
	return result, nil
}

func (m *Method) Delete(ctx context.Context, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	ccxClient, err := ccx.NewClient(m.cfg)
	if err != nil {
		return nil, err
	}
	return ccxClient.DeleteResource(ctx, request)
}

func (m *Method) Status(ctx context.Context, request *resource.StatusRequest) (*resource.StatusResult, error) {
	ccxClient, err := ccx.NewClient(m.cfg)
	if err != nil {
		return nil, err
	}
	return ccxClient.StatusResource(ctx, request, m.Read)
}

func (m *Method) List(ctx context.Context, request *resource.ListRequest) (*resource.ListResult, error) {
	return nil, fmt.Errorf("apiGateway::Method: list operation not supported")
}

// handleLambdaIntegration transforms the Lambda integration properties for API Gateway.
// This solves the issue of using Lambda ARNs directly in API Gateway integrations and
// allows the API Gateway to $ref the Lambda function.
func (m *Method) handleLambdaIntegration(ctx context.Context, properties []byte) ([]byte, error) {
	var props map[string]any
	if err := json.Unmarshal(properties, &props); err != nil {
		return properties, err
	}

	integration, ok := props["Integration"].(map[string]any)
	if !ok {
		return properties, nil
	}

	if _, hasLambdaArn := integration["LambdaFunctionArn"]; !hasLambdaArn {
		return properties, nil
	}

	if err := m.integrationLambdaArnToURI(integration); err != nil {
		return nil, err
	}

	transformedProps, err := json.Marshal(props)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal transformed properties: %w", err)
	}

	plugin.LoggerFromContext(ctx).Debug("ApiGateway::Method: Transformed Lambda integration",
		"uri", integration["Uri"])

	return transformedProps, nil
}

// integrationLambdaArnToURI converts the formae-only LambdaFunctionArn on an
// Integration into the execute-api invocation Uri CloudControl expects and drops
// LambdaFunctionArn. When both are set, LambdaFunctionArn wins (the derived Uri
// overwrites any supplied uri). It is a no-op when LambdaFunctionArn is absent
// (HTTP/HTTP_PROXY/MOCK integrations), so they pass through untouched.
func (m *Method) integrationLambdaArnToURI(integration map[string]any) error {
	lambdaArn, hasLambdaArn := integration["LambdaFunctionArn"]
	if !hasLambdaArn {
		return nil
	}

	lambdaArnStr, ok := lambdaArn.(string)
	if !ok {
		return fmt.Errorf("expected LambdaFunctionArn to be resolved string, got %T", lambdaArn)
	}

	region, err := m.extractRegionFromLambdaArn(lambdaArnStr)
	if err != nil {
		return fmt.Errorf("failed to extract region from Lambda ARN: %w", err)
	}

	integration["Uri"] = fmt.Sprintf("arn:aws:apigateway:%s:lambda:path/2015-03-31/functions/%s/invocations",
		region, lambdaArnStr)
	delete(integration, "LambdaFunctionArn")

	return nil
}

// transformLambdaIntegrationPatch is the write-time Lambda integration transform
// applied to a CloudControl update patch. The Integration field uses an Atomic
// update method, so a re-pointed Lambda integration arrives as a single
// add/replace op at /Integration carrying the whole object; without converting
// its LambdaFunctionArn to the invocation Uri (as Create does for properties),
// CloudControl rejects the formae-only field. Ops for other paths, and
// Integration ops without LambdaFunctionArn (HTTP/HTTP_PROXY/MOCK), pass through.
func (m *Method) transformLambdaIntegrationPatch(patchDoc string) (string, error) {
	var ops []map[string]any
	if err := json.Unmarshal([]byte(patchDoc), &ops); err != nil {
		return patchDoc, err
	}

	modified := false
	for _, op := range ops {
		if name, _ := op["op"].(string); name != "add" && name != "replace" {
			continue
		}
		if path, _ := op["path"].(string); path != "/Integration" {
			continue
		}
		integration, ok := op["value"].(map[string]any)
		if !ok {
			continue
		}
		if _, hasLambdaArn := integration["LambdaFunctionArn"]; !hasLambdaArn {
			continue
		}
		if err := m.integrationLambdaArnToURI(integration); err != nil {
			return patchDoc, err
		}
		modified = true
	}

	if !modified {
		return patchDoc, nil
	}

	transformed, err := json.Marshal(ops)
	if err != nil {
		return patchDoc, fmt.Errorf("failed to marshal transformed patch: %w", err)
	}

	return string(transformed), nil
}

func (m *Method) extractRegionFromLambdaArn(arn string) (string, error) {

	// Note: Lambda ARN format = arn:aws:lambda:region:account:function:name
	parts := strings.Split(arn, ":")
	if len(parts) >= 4 && parts[0] == "arn" && parts[1] == "aws" && parts[2] == "lambda" {
		return parts[3], nil
	}
	return "", fmt.Errorf("invalid Lambda ARN format: %s", arn)
}

// lambdaInvocationURIPattern matches a Lambda-proxy integration Uri of the form
// arn:<partition>:apigateway:<region>:lambda:path/2015-03-31/functions/<lambdaArn>/invocations
// — the exact shape handleLambdaIntegration builds. The single capture group is
// the full Lambda ARN between /functions/ and /invocations, which preserves any
// alias/version qualifier (e.g. ...:function:Fn:prod). The partition and region
// segments are matched generically so aws, aws-us-gov, and aws-cn round-trip.
var lambdaInvocationURIPattern = regexp.MustCompile(
	`^arn:[^:]+:apigateway:[^:]+:lambda:path/2015-03-31/functions/(.+)/invocations$`)

// lambdaArnFromInvocationURI is the precise inverse of the Uri builder in
// handleLambdaIntegration: given a Lambda-proxy invocation Uri it returns the
// embedded Lambda ARN and true; for any other value (HTTP/HTTP_PROXY uri, empty,
// malformed) it returns false so the caller leaves the integration untouched.
func lambdaArnFromInvocationURI(uri string) (string, bool) {
	matches := lambdaInvocationURIPattern.FindStringSubmatch(uri)
	if matches == nil {
		return "", false
	}
	return matches[1], true
}

// reverseLambdaIntegrationURI restores the formae-only LambdaFunctionArn field
// onto a read-back Integration. LambdaFunctionArn is not a CloudControl property
// (the write handler converts it to Uri), so a generic read returns only Uri;
// without this inverse the Atomic Integration compare would diff the
// desired-only LambdaFunctionArn against the actual-only Uri on every reconcile.
// Only a Lambda-proxy Uri is rewritten — HTTP integrations (literal uri) and
// MOCK integrations (no uri) are left as-is, so no drift is inverted.
func reverseLambdaIntegrationURI(integration map[string]any) {
	uri, ok := integration["Uri"].(string)
	if !ok {
		return
	}
	lambdaArn, ok := lambdaArnFromInvocationURI(uri)
	if !ok {
		return
	}
	integration["LambdaFunctionArn"] = lambdaArn
	delete(integration, "Uri")
}

// normalizeIntegrationOnRead applies reverseLambdaIntegrationURI to the
// Integration block of a CloudControl read-back properties document, returning
// the re-marshaled JSON. Properties without an Integration object pass through
// unchanged.
func normalizeIntegrationOnRead(properties string) (string, error) {
	var props map[string]any
	if err := json.Unmarshal([]byte(properties), &props); err != nil {
		return "", err
	}

	integration, ok := props["Integration"].(map[string]any)
	if !ok {
		return properties, nil
	}
	reverseLambdaIntegrationURI(integration)

	normalized, err := json.Marshal(props)
	if err != nil {
		return "", fmt.Errorf("failed to marshal normalized properties: %w", err)
	}
	return string(normalized), nil
}
