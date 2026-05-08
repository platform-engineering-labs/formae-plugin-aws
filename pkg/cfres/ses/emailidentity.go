// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package ses

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	sesv2types "github.com/aws/aws-sdk-go-v2/service/sesv2/types"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/ccx"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres/prov"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres/registry"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/config"
)

// DnsRecord is the Go representation of the schema/pkl/ses/ses.pkl DnsRecord
// value-object.
type DnsRecord struct {
	Type           string   `json:"Type"`
	Name           string   `json:"Name"`
	Values         []string `json:"Values"`
	RecommendedTtl int      `json:"RecommendedTtl"`
	Priority       *int     `json:"Priority,omitempty"`
}

// EmailIdentity is the AWS::SES::EmailIdentity provisioner. Read is custom
// (enriches the CloudControl response with synthesized DNS records and live
// verification status); all other operations delegate to CloudControl.
type EmailIdentity struct {
	cfg *config.Config
	// sesClientFactory allows tests to inject a fake. Production uses the
	// default factory, which builds an SDK sesv2.Client from cfg.
	sesClientFactory func(cfg *config.Config) (SesV2ClientInterface, error)
}

var _ prov.Provisioner = &EmailIdentity{}

func init() {
	registry.Register("AWS::SES::EmailIdentity",
		[]resource.Operation{
			resource.OperationRead,
			resource.OperationCreate,
			resource.OperationUpdate,
			resource.OperationCheckStatus,
			resource.OperationDelete,
		},
		func(cfg *config.Config) prov.Provisioner {
			return &EmailIdentity{
				cfg:              cfg,
				sesClientFactory: defaultSesV2ClientFactory,
			}
		})
}

func defaultSesV2ClientFactory(cfg *config.Config) (SesV2ClientInterface, error) {
	awsCfg, err := cfg.ToAwsConfig(context.Background())
	if err != nil {
		return nil, fmt.Errorf("ses: build AWS config: %w", err)
	}
	return sesv2.NewFromConfig(awsCfg), nil
}

// synthesizeFromIdentity calls SESv2 GetEmailIdentity and converts the
// response into the formae-side outputs (requiredDnsRecords, verification
// status, dkim verified). region is used to construct MAIL FROM MX targets.
func synthesizeFromIdentity(
	ctx context.Context,
	client SesV2ClientInterface,
	emailIdentity string,
	region string,
) (records []DnsRecord, status sesv2types.VerificationStatus, dkimVerified bool, err error) {
	resp, err := client.GetEmailIdentity(ctx, &sesv2.GetEmailIdentityInput{
		EmailIdentity: &emailIdentity,
	})
	if err != nil {
		return nil, "", false, err
	}

	status = resp.VerificationStatus
	dkimVerified = resp.VerifiedForSendingStatus

	if resp.IdentityType != sesv2types.IdentityTypeDomain {
		return nil, status, dkimVerified, nil
	}

	if resp.DkimAttributes != nil {
		for _, tok := range resp.DkimAttributes.Tokens {
			records = append(records, DnsRecord{
				Type:           "CNAME",
				Name:           tok + "._domainkey." + emailIdentity,
				Values:         []string{tok + ".dkim.amazonses.com"},
				RecommendedTtl: 300,
			})
		}
	}

	if resp.MailFromAttributes != nil && resp.MailFromAttributes.MailFromDomain != nil &&
		*resp.MailFromAttributes.MailFromDomain != "" {
		mailFrom := *resp.MailFromAttributes.MailFromDomain
		mxPriority := 10
		// Values has the priority baked in (Route53's MX wire format).
		// Priority stays populated for DNS providers (e.g. Cloudflare) that
		// want priority as a discrete field — they can derive it from this
		// field and use Values[0] minus the prefix.
		records = append(records, DnsRecord{
			Type:           "MX",
			Name:           mailFrom,
			Values:         []string{"10 feedback-smtp." + region + ".amazonses.com"},
			RecommendedTtl: 300,
			Priority:       &mxPriority,
		})
		records = append(records, DnsRecord{
			Type:           "TXT",
			Name:           mailFrom,
			Values:         []string{"v=spf1 include:amazonses.com ~all"},
			RecommendedTtl: 300,
		})
	}
	return records, status, dkimVerified, nil
}

// Read enriches the CloudControl read with synthesized DNS records and SES
// verification state.
func (e *EmailIdentity) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	ccxClient, err := ccx.NewClient(e.cfg)
	if err != nil {
		slog.Error("SES EmailIdentity: ccx client init failed", "error", err)
		return nil, err
	}
	result, err := ccxClient.ReadResource(ctx, request)
	if err != nil {
		slog.Error("SES EmailIdentity: ccx ReadResource failed", "error", err)
		return nil, err
	}

	sesClient, err := e.sesClientFactory(e.cfg)
	if err != nil {
		slog.Warn("SES EmailIdentity: SDK client unavailable; returning unenriched CCAPI result", "error", err)
		return result, nil
	}
	region := e.cfg.Region

	records, status, dkim, err := synthesizeFromIdentity(ctx, sesClient, request.NativeID, region)
	if err != nil {
		slog.Warn("SES EmailIdentity: GetEmailIdentity failed; returning unenriched CCAPI result",
			"error", err, "identity", request.NativeID)
		return result, nil
	}

	props := map[string]any{}
	if raw := strings.TrimSpace(result.Properties); raw != "" {
		if err := json.Unmarshal([]byte(raw), &props); err != nil {
			slog.Warn("SES EmailIdentity: ccx Properties not valid JSON; defaulting to empty map", "error", err)
			props = map[string]any{}
		}
	}
	props["RequiredDnsRecords"] = records
	props["VerificationStatus"] = string(status)
	props["DkimVerified"] = dkim

	enriched, err := json.Marshal(props)
	if err != nil {
		slog.Warn("SES EmailIdentity: marshal enriched Properties failed; returning ccx result", "error", err)
		return result, nil
	}
	result.Properties = string(enriched)
	return result, nil
}

func (e *EmailIdentity) Create(ctx context.Context, request *resource.CreateRequest) (*resource.CreateResult, error) {
	ccxClient, err := ccx.NewClient(e.cfg)
	if err != nil {
		return nil, err
	}
	return ccxClient.CreateResource(ctx, request)
}

func (e *EmailIdentity) Update(ctx context.Context, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	ccxClient, err := ccx.NewClient(e.cfg)
	if err != nil {
		return nil, err
	}
	return ccxClient.UpdateResource(ctx, request)
}

func (e *EmailIdentity) Delete(ctx context.Context, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	ccxClient, err := ccx.NewClient(e.cfg)
	if err != nil {
		return nil, err
	}
	return ccxClient.DeleteResource(ctx, request)
}

func (e *EmailIdentity) Status(ctx context.Context, request *resource.StatusRequest) (*resource.StatusResult, error) {
	ccxClient, err := ccx.NewClient(e.cfg)
	if err != nil {
		return nil, err
	}
	return ccxClient.StatusResource(ctx, request, e.Read)
}

func (e *EmailIdentity) List(ctx context.Context, request *resource.ListRequest) (*resource.ListResult, error) {
	return nil, fmt.Errorf("list not implemented for EmailIdentity provisioner - cloudcontrol natively supports this operation")
}
