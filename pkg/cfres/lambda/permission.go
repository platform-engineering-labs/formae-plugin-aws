// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package lambda

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	awslambda "github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres/prov"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres/registry"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/config"
)

// Permission is a custom provisioner for AWS::Lambda::Permission that only
// overrides List. A Lambda function keeps a separate resource policy per
// qualifier: statements added against the bare function name live on the
// unqualified policy, while statements added against a published version's
// or an alias's ARN live on that qualifier's policy. CloudControl's list
// handler only returns the unqualified policy's statements, so permissions
// attached to a version or alias are invisible to it. Discovery therefore
// enumerates every policy scope through the Lambda control plane. All other
// operations fall through to CloudControl.
type Permission struct {
	cfg *config.Config
}

var _ prov.Provisioner = &Permission{}

func init() {
	registry.Register("AWS::Lambda::Permission",
		[]resource.Operation{resource.OperationList},
		func(cfg *config.Config) prov.Provisioner {
			return &Permission{cfg: cfg}
		})
}

// lambdaPermissionClient is the narrow Lambda-SDK subset used by List.
type lambdaPermissionClient interface {
	GetPolicy(ctx context.Context, params *awslambda.GetPolicyInput, optFns ...func(*awslambda.Options)) (*awslambda.GetPolicyOutput, error)
	ListVersionsByFunction(ctx context.Context, params *awslambda.ListVersionsByFunctionInput, optFns ...func(*awslambda.Options)) (*awslambda.ListVersionsByFunctionOutput, error)
	ListAliases(ctx context.Context, params *awslambda.ListAliasesInput, optFns ...func(*awslambda.Options)) (*awslambda.ListAliasesOutput, error)
}

func (p *Permission) List(ctx context.Context, request *resource.ListRequest) (*resource.ListResult, error) {
	awsCfg, err := p.cfg.ToAwsConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading AWS config: %w", err)
	}
	// Discovery has no operator-level retry loop around List, and registry
	// dispatch bypasses the ccx retry budget, so give this client the same
	// discovery-grade budget ccx uses (10 attempts, backoff capped at 30s)
	// instead of the SDK default of 3 attempts.
	client := awslambda.NewFromConfig(awsCfg, func(o *awslambda.Options) {
		o.Retryer = retry.NewStandard(func(so *retry.StandardOptions) {
			so.MaxAttempts = 10
			so.MaxBackoff = 30 * time.Second
		})
	})
	return p.listWithClient(ctx, client, request)
}

func (p *Permission) listWithClient(ctx context.Context, client lambdaPermissionClient, request *resource.ListRequest) (*resource.ListResult, error) {
	functionName, ok := request.AdditionalProperties["FunctionName"]
	if !ok || functionName == "" {
		return nil, fmt.Errorf("AWS::Lambda::Permission list requires FunctionName filter")
	}

	// Every qualifier scope that can carry its own resource policy: the bare
	// function, each published version, and each alias. $LATEST shares the
	// unqualified policy and is skipped to avoid double-listing it.
	qualifiers := []string{""}
	var marker *string
	for {
		versions, err := client.ListVersionsByFunction(ctx, &awslambda.ListVersionsByFunctionInput{
			FunctionName: aws.String(functionName),
			Marker:       marker,
		})
		if err != nil {
			// Treat a missing parent as an empty list rather than a discovery
			// failure: the function may have been deleted between the list
			// operation being queued and the call landing.
			var notFound *lambdatypes.ResourceNotFoundException
			if errors.As(err, &notFound) {
				return &resource.ListResult{NativeIDs: []string{}}, nil
			}
			return nil, fmt.Errorf("listing versions of function %s: %w", functionName, err)
		}
		for _, v := range versions.Versions {
			if v.Version == nil || *v.Version == "$LATEST" {
				continue
			}
			qualifiers = append(qualifiers, *v.Version)
		}
		if versions.NextMarker == nil {
			break
		}
		marker = versions.NextMarker
	}
	aliases, err := client.ListAliases(ctx, &awslambda.ListAliasesInput{FunctionName: aws.String(functionName)})
	if err != nil {
		var notFound *lambdatypes.ResourceNotFoundException
		if errors.As(err, &notFound) {
			return &resource.ListResult{NativeIDs: []string{}}, nil
		}
		return nil, fmt.Errorf("listing aliases of function %s: %w", functionName, err)
	}
	for _, a := range aliases.Aliases {
		if a.Name != nil {
			qualifiers = append(qualifiers, *a.Name)
		}
	}

	nativeIDs := []string{}
	for _, qualifier := range qualifiers {
		input := &awslambda.GetPolicyInput{FunctionName: aws.String(functionName)}
		if qualifier != "" {
			input.Qualifier = aws.String(qualifier)
		}
		policy, err := client.GetPolicy(ctx, input)
		if err != nil {
			// A scope without any permission statements has no policy at all.
			var notFound *lambdatypes.ResourceNotFoundException
			if errors.As(err, &notFound) {
				continue
			}
			return nil, fmt.Errorf("reading policy of function %s qualifier %q: %w", functionName, qualifier, err)
		}
		if policy.Policy == nil {
			continue
		}
		ids, err := nativeIDsFromPolicy(*policy.Policy)
		if err != nil {
			return nil, fmt.Errorf("parsing policy of function %s qualifier %q: %w", functionName, qualifier, err)
		}
		nativeIDs = append(nativeIDs, ids...)
	}

	return &resource.ListResult{NativeIDs: nativeIDs}, nil
}

// nativeIDsFromPolicy extracts one composite native id per policy statement.
// The id mirrors the CloudControl CRUD path for Permission:
//
//	<function or qualified ARN>|<Sid>
//
// where the ARN is the statement's Resource, which Lambda always writes as
// the ARN of the scope the permission was added to. Discovery must produce
// the same shape or inventory lookups diverge between discovered and managed
// permissions.
func nativeIDsFromPolicy(policyJSON string) ([]string, error) {
	var doc struct {
		Statement []struct {
			Sid      string `json:"Sid"`
			Resource any    `json:"Resource"`
		} `json:"Statement"`
	}
	if err := json.Unmarshal([]byte(policyJSON), &doc); err != nil {
		return nil, err
	}
	ids := []string{}
	for _, stmt := range doc.Statement {
		arn, ok := stmt.Resource.(string)
		if !ok || arn == "" || stmt.Sid == "" {
			continue
		}
		ids = append(ids, fmt.Sprintf("%s|%s", arn, stmt.Sid))
	}
	return ids, nil
}

func (p *Permission) Create(ctx context.Context, request *resource.CreateRequest) (*resource.CreateResult, error) {
	return nil, fmt.Errorf("create is handled by CloudControl for AWS::Lambda::Permission")
}

func (p *Permission) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	return nil, fmt.Errorf("read is handled by CloudControl for AWS::Lambda::Permission")
}

func (p *Permission) Update(ctx context.Context, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	return nil, fmt.Errorf("update is handled by CloudControl for AWS::Lambda::Permission")
}

func (p *Permission) Delete(ctx context.Context, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	return nil, fmt.Errorf("delete is handled by CloudControl for AWS::Lambda::Permission")
}

func (p *Permission) Status(ctx context.Context, request *resource.StatusRequest) (*resource.StatusResult, error) {
	return nil, fmt.Errorf("status is handled by CloudControl for AWS::Lambda::Permission")
}
