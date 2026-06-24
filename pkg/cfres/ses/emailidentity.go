// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package ses

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	sesv2types "github.com/aws/aws-sdk-go-v2/service/sesv2/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	"github.com/platform-engineering-labs/formae/pkg/plugin"
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
		plugin.LoggerFromContext(ctx).Error("SES EmailIdentity: ccx client init failed", "error", err)
		return nil, err
	}
	result, err := ccxClient.ReadResource(ctx, request)
	if err != nil {
		plugin.LoggerFromContext(ctx).Error("SES EmailIdentity: ccx ReadResource failed", "error", err)
		return nil, err
	}

	sesClient, err := e.sesClientFactory(e.cfg)
	if err != nil {
		plugin.LoggerFromContext(ctx).Warn("SES EmailIdentity: SDK client unavailable; returning unenriched CCAPI result", "error", err)
		return result, nil
	}
	region := e.cfg.Region

	records, status, dkim, err := synthesizeFromIdentity(ctx, sesClient, request.NativeID, region)
	if err != nil {
		plugin.LoggerFromContext(ctx).Warn("SES EmailIdentity: GetEmailIdentity failed; returning unenriched CCAPI result",
			"error", err, "identity", request.NativeID)
		return result, nil
	}

	props := map[string]any{}
	if raw := strings.TrimSpace(result.Properties); raw != "" {
		if err := json.Unmarshal([]byte(raw), &props); err != nil {
			plugin.LoggerFromContext(ctx).Warn("SES EmailIdentity: ccx Properties not valid JSON; defaulting to empty map", "error", err)
			props = map[string]any{}
		}
	}
	props["RequiredDnsRecords"] = records
	props["VerificationStatus"] = string(status)
	props["DkimVerified"] = dkim

	enriched, err := json.Marshal(props)
	if err != nil {
		plugin.LoggerFromContext(ctx).Warn("SES EmailIdentity: marshal enriched Properties failed; returning ccx result", "error", err)
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

// Update is custom (like Read). CloudControl's async AWS::SES::EmailIdentity
// update handler fails intermittently with GeneralServiceException "security
// token included in the request is invalid" (an AWS-side credential propagation
// fault we cannot influence), so we apply each attribute group directly via the
// SESv2 SDK instead of delegating to CloudControl.
func (e *EmailIdentity) Update(ctx context.Context, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	sesClient, err := e.sesClientFactory(e.cfg)
	if err != nil {
		return nil, fmt.Errorf("ses: build SESv2 client: %w", err)
	}
	awsCfg, err := e.cfg.ToAwsConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("ses: build AWS config: %w", err)
	}
	return e.updateWithClient(ctx, sesClient, sts.NewFromConfig(awsCfg), request)
}

func (e *EmailIdentity) updateWithClient(
	ctx context.Context,
	sesClient SesV2ClientInterface,
	stsClient stsClientInterface,
	request *resource.UpdateRequest,
) (*resource.UpdateResult, error) {
	identity := request.NativeID

	var desired map[string]any
	if err := json.Unmarshal(request.DesiredProperties, &desired); err != nil {
		return nil, fmt.Errorf("ses: parse desired properties: %w", err)
	}
	// Prior is best-effort: it only drives diffs (DKIM once-per-day guard, tag
	// removals). An unparseable prior degrades to "apply everything".
	var prior map[string]any
	if len(request.PriorProperties) > 0 {
		_ = json.Unmarshal(request.PriorProperties, &prior)
	}

	if mf, ok := desired["MailFromAttributes"].(map[string]any); ok {
		in := &sesv2.PutEmailIdentityMailFromAttributesInput{EmailIdentity: &identity}
		if v, ok := mf["BehaviorOnMxFailure"].(string); ok {
			in.BehaviorOnMxFailure = sesv2types.BehaviorOnMxFailure(v)
		}
		if v, ok := mf["MailFromDomain"].(string); ok && v != "" {
			in.MailFromDomain = aws.String(v)
		}
		if _, err := sesClient.PutEmailIdentityMailFromAttributes(ctx, in); err != nil {
			return nil, fmt.Errorf("ses: put mail-from attributes: %w", err)
		}
	}

	if fb, ok := desired["FeedbackAttributes"].(map[string]any); ok {
		in := &sesv2.PutEmailIdentityFeedbackAttributesInput{EmailIdentity: &identity}
		if v, ok := fb["EmailForwardingEnabled"].(bool); ok {
			in.EmailForwardingEnabled = v
		}
		if _, err := sesClient.PutEmailIdentityFeedbackAttributes(ctx, in); err != nil {
			return nil, fmt.Errorf("ses: put feedback attributes: %w", err)
		}
	}

	if cs, ok := desired["ConfigurationSet"].(string); ok && cs != "" {
		if _, err := sesClient.PutEmailIdentityConfigurationSetAttributes(ctx, &sesv2.PutEmailIdentityConfigurationSetAttributesInput{
			EmailIdentity:        &identity,
			ConfigurationSetName: aws.String(cs),
		}); err != nil {
			return nil, fmt.Errorf("ses: put configuration-set attributes: %w", err)
		}
	}

	// Easy-DKIM key length can be changed at most once per day, so only call
	// when it actually changed.
	desiredDkim := dkimKeyLength(desired)
	if desiredDkim != "" && desiredDkim != dkimKeyLength(prior) {
		if _, err := sesClient.PutEmailIdentityDkimSigningAttributes(ctx, &sesv2.PutEmailIdentityDkimSigningAttributesInput{
			EmailIdentity:           &identity,
			SigningAttributesOrigin: sesv2types.DkimSigningAttributesOriginAwsSes,
			SigningAttributes: &sesv2types.DkimSigningAttributes{
				NextSigningKeyLength: sesv2types.DkimSigningKeyLength(desiredDkim),
			},
		}); err != nil {
			return nil, fmt.Errorf("ses: put dkim signing attributes: %w", err)
		}
	}

	if err := e.reconcileTags(ctx, sesClient, stsClient, identity, prior, desired); err != nil {
		return nil, err
	}

	return &resource.UpdateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationUpdate,
			OperationStatus:    resource.OperationStatusSuccess,
			NativeID:           identity,
			ResourceProperties: json.RawMessage(request.DesiredProperties),
		},
	}, nil
}

// dkimKeyLength digs DkimSigningAttributes.NextSigningKeyLength out of a
// properties map, returning "" when absent.
func dkimKeyLength(props map[string]any) string {
	dkim, ok := props["DkimSigningAttributes"].(map[string]any)
	if !ok {
		return ""
	}
	v, _ := dkim["NextSigningKeyLength"].(string)
	return v
}

// reconcileTags upserts changed/new tags and removes tags absent from the
// desired state. It only resolves the account ID (an STS round-trip needed to
// build the identity ARN) when there is at least one tag change to apply.
func (e *EmailIdentity) reconcileTags(
	ctx context.Context,
	sesClient SesV2ClientInterface,
	stsClient stsClientInterface,
	identity string,
	prior, desired map[string]any,
) error {
	priorTags := parseTags(prior)
	desiredTags := parseTags(desired)

	var upsert []sesv2types.Tag
	for k, v := range desiredTags {
		if old, ok := priorTags[k]; !ok || old != v {
			upsert = append(upsert, sesv2types.Tag{Key: aws.String(k), Value: aws.String(v)})
		}
	}
	var remove []string
	for k := range priorTags {
		if _, ok := desiredTags[k]; !ok {
			remove = append(remove, k)
		}
	}

	if len(upsert) == 0 && len(remove) == 0 {
		return nil
	}

	caller, err := stsClient.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return fmt.Errorf("ses: resolve account for tag ARN: %w", err)
	}
	arn := fmt.Sprintf("arn:aws:ses:%s:%s:identity/%s", e.cfg.Region, aws.ToString(caller.Account), identity)

	if len(upsert) > 0 {
		if _, err := sesClient.TagResource(ctx, &sesv2.TagResourceInput{ResourceArn: &arn, Tags: upsert}); err != nil {
			return fmt.Errorf("ses: tag identity: %w", err)
		}
	}
	if len(remove) > 0 {
		if _, err := sesClient.UntagResource(ctx, &sesv2.UntagResourceInput{ResourceArn: &arn, TagKeys: remove}); err != nil {
			return fmt.Errorf("ses: untag identity: %w", err)
		}
	}
	return nil
}

// parseTags converts a properties map's "Tags" list ([{Key,Value},...]) into a
// key→value map, ignoring malformed entries.
func parseTags(props map[string]any) map[string]string {
	out := map[string]string{}
	list, ok := props["Tags"].([]any)
	if !ok {
		return out
	}
	for _, raw := range list {
		tag, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		key, _ := tag["Key"].(string)
		val, _ := tag["Value"].(string)
		if key != "" {
			out[key] = val
		}
	}
	return out
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
