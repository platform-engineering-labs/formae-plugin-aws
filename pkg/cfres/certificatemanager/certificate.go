// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package certificatemanager

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/acm"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/ccx"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres/prov"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres/registry"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres/ses"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/config"
)

// Certificate is the AWS::CertificateManager::Certificate provisioner. Read
// is custom: it enriches the CloudControl response with synthesized DNS
// validation records (from ACM DescribeCertificate). All other operations
// delegate to CloudControl.
type Certificate struct {
	cfg              *config.Config
	acmClientFactory func(cfg *config.Config) (ACMClientInterface, error)
}

var _ prov.Provisioner = &Certificate{}

func init() {
	registry.Register("AWS::CertificateManager::Certificate",
		[]resource.Operation{
			resource.OperationRead,
			resource.OperationCreate,
			resource.OperationUpdate,
			resource.OperationCheckStatus,
			resource.OperationDelete,
		},
		func(cfg *config.Config) prov.Provisioner {
			return &Certificate{
				cfg:              cfg,
				acmClientFactory: defaultACMClientFactory,
			}
		})
}

func defaultACMClientFactory(cfg *config.Config) (ACMClientInterface, error) {
	awsCfg, err := cfg.ToAwsConfig(context.Background())
	if err != nil {
		return nil, fmt.Errorf("acm: build AWS config: %w", err)
	}
	return acm.NewFromConfig(awsCfg), nil
}

// synthesizeValidationRecords calls ACM DescribeCertificate and converts
// DomainValidationOptions[].ResourceRecord into ses.DnsRecord entries
// (reusing the SES DnsRecord shape). Records may be empty if the cert
// uses EMAIL validation or is too new for ACM to have populated the
// CNAMEs yet.
func synthesizeValidationRecords(
	ctx context.Context,
	client ACMClientInterface,
	certArn string,
) ([]ses.DnsRecord, error) {
	resp, err := client.DescribeCertificate(ctx, &acm.DescribeCertificateInput{
		CertificateArn: aws.String(certArn),
	})
	if err != nil {
		return nil, err
	}
	if resp == nil || resp.Certificate == nil {
		return nil, nil
	}
	var records []ses.DnsRecord
	for _, opt := range resp.Certificate.DomainValidationOptions {
		rr := opt.ResourceRecord
		if rr == nil || rr.Name == nil || rr.Value == nil {
			continue
		}
		records = append(records, ses.DnsRecord{
			Type:           string(rr.Type),
			Name:           *rr.Name,
			Values:         []string{*rr.Value},
			RecommendedTtl: 300,
		})
	}
	return records, nil
}

// Read enriches the CloudControl read with synthesized ACM DNS validation
// records via DescribeCertificate.
func (c *Certificate) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	ccxClient, err := ccx.NewClient(c.cfg)
	if err != nil {
		slog.Error("ACM Certificate: ccx client init failed", "error", err)
		return nil, err
	}
	result, err := ccxClient.ReadResource(ctx, request)
	if err != nil {
		slog.Error("ACM Certificate: ccx ReadResource failed", "error", err)
		return nil, err
	}

	acmClient, err := c.acmClientFactory(c.cfg)
	if err != nil {
		slog.Warn("ACM Certificate: SDK client unavailable; returning unenriched CCAPI result", "error", err)
		return result, nil
	}

	// The cert ARN is the CloudControl NativeID for AWS::CertificateManager::Certificate
	// (the CFN primary identifier `Id` is the ARN string).
	records, err := synthesizeValidationRecords(ctx, acmClient, request.NativeID)
	if err != nil {
		slog.Warn("ACM Certificate: DescribeCertificate failed; returning unenriched CCAPI result",
			"error", err, "cert_arn", request.NativeID)
		return result, nil
	}

	props := map[string]any{}
	if raw := strings.TrimSpace(result.Properties); raw != "" {
		if err := json.Unmarshal([]byte(raw), &props); err != nil {
			slog.Warn("ACM Certificate: ccx Properties not valid JSON; defaulting to empty map", "error", err)
			props = map[string]any{}
		}
	}
	props["ValidationRecords"] = records

	enriched, err := json.Marshal(props)
	if err != nil {
		slog.Warn("ACM Certificate: marshal enriched Properties failed; returning ccx result", "error", err)
		return result, nil
	}
	result.Properties = string(enriched)
	return result, nil
}

func (c *Certificate) Create(ctx context.Context, request *resource.CreateRequest) (*resource.CreateResult, error) {
	ccxClient, err := ccx.NewClient(c.cfg)
	if err != nil {
		return nil, err
	}
	return ccxClient.CreateResource(ctx, request)
}

func (c *Certificate) Update(ctx context.Context, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	ccxClient, err := ccx.NewClient(c.cfg)
	if err != nil {
		return nil, err
	}
	return ccxClient.UpdateResource(ctx, request)
}

func (c *Certificate) Delete(ctx context.Context, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	ccxClient, err := ccx.NewClient(c.cfg)
	if err != nil {
		return nil, err
	}
	return ccxClient.DeleteResource(ctx, request)
}

func (c *Certificate) Status(ctx context.Context, request *resource.StatusRequest) (*resource.StatusResult, error) {
	ccxClient, err := ccx.NewClient(c.cfg)
	if err != nil {
		return nil, err
	}
	return ccxClient.StatusResource(ctx, request, c.Read)
}

func (c *Certificate) List(ctx context.Context, request *resource.ListRequest) (*resource.ListResult, error) {
	return nil, fmt.Errorf("list not implemented for Certificate provisioner - cloudcontrol natively supports this operation")
}
