// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package certificatemanager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/acm"
	acmtypes "github.com/aws/aws-sdk-go-v2/service/acm/types"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres/prov"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres/registry"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres/ses"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres/utils"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/config"
)

// Wait-for-ISSUED defaults. Operators with unusual DNS-propagation
// expectations can override these on the provisioner at construction
// time; tests do so to keep run-time in microseconds.
const (
	defaultPollInterval  = 10 * time.Second
	defaultIssuedTimeout = 15 * time.Minute
)

// Certificate is the AWS::CertificateManager::Certificate provisioner.
// ACM is NON_PROVISIONABLE in the CFN registry, so every operation goes
// through the ACM SDK directly rather than CloudControl.
//
// All ACM operations are synchronous:
//   - RequestCertificate returns the new ARN immediately
//   - DeleteCertificate completes synchronously
//   - Tag mutations are synchronous
//
// The cert's validation lifecycle (PENDING_VALIDATION -> ISSUED) is
// separate from the resource lifecycle: the cert resource is created
// the moment RequestCertificate returns. Read enriches the response
// with the DNS-validation records that DescribeCertificate returns
// (when the cert was created with DNS validation).
type Certificate struct {
	cfg              *config.Config
	acmClientFactory func(cfg *config.Config) (ACMClientInterface, error)
	// pollInterval is how long Create sleeps between DescribeCertificate
	// polls while waiting for Status=ISSUED. Zero means use the default.
	pollInterval time.Duration
	// issuedTimeout is the upper bound on the wait-for-ISSUED loop. Zero
	// means use the default.
	issuedTimeout time.Duration
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
			resource.OperationList,
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

// ----- Create -----

func (c *Certificate) Create(ctx context.Context, request *resource.CreateRequest) (*resource.CreateResult, error) {
	client, err := c.acmClientFactory(c.cfg)
	if err != nil {
		return nil, err
	}

	var properties map[string]any
	if err := json.Unmarshal(request.Properties, &properties); err != nil {
		return nil, fmt.Errorf("acm: parse properties: %w", err)
	}

	domainName, err := utils.GetStringProperty(properties, "DomainName")
	if err != nil {
		return nil, fmt.Errorf("acm: invalid DomainName: %w", err)
	}

	input := &acm.RequestCertificateInput{
		DomainName: aws.String(domainName),
	}

	if sansRaw, ok := properties["SubjectAlternativeNames"].([]any); ok {
		var sans []string
		for _, s := range sansRaw {
			if str, ok := s.(string); ok && str != "" {
				sans = append(sans, str)
			}
		}
		if len(sans) > 0 {
			input.SubjectAlternativeNames = sans
		}
	}

	if vm, ok := properties["ValidationMethod"].(string); ok && vm != "" {
		input.ValidationMethod = acmtypes.ValidationMethod(vm)
	}

	if ka, ok := properties["KeyAlgorithm"].(string); ok && ka != "" {
		input.KeyAlgorithm = acmtypes.KeyAlgorithm(ka)
	}

	if caArn, ok := properties["CertificateAuthorityArn"].(string); ok && caArn != "" {
		input.CertificateAuthorityArn = aws.String(caArn)
	}

	if dvoRaw, ok := properties["DomainValidationOptions"].([]any); ok {
		var dvos []acmtypes.DomainValidationOption
		for _, raw := range dvoRaw {
			dvoMap, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			dn, _ := dvoMap["DomainName"].(string)
			vd, _ := dvoMap["ValidationDomain"].(string)
			if dn == "" {
				continue
			}
			dvo := acmtypes.DomainValidationOption{
				DomainName: aws.String(dn),
			}
			if vd != "" {
				dvo.ValidationDomain = aws.String(vd)
			} else {
				// AWS requires ValidationDomain to be set when DomainValidationOptions is
				// non-empty; default to the domain name itself when not provided.
				dvo.ValidationDomain = aws.String(dn)
			}
			dvos = append(dvos, dvo)
		}
		if len(dvos) > 0 {
			input.DomainValidationOptions = dvos
		}
	}

	if tags := tagsFromProperties(properties); len(tags) > 0 {
		input.Tags = tags
	}

	resp, err := client.RequestCertificate(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("acm: RequestCertificate: %w", err)
	}
	if resp == nil || resp.CertificateArn == nil {
		return nil, errors.New("acm: RequestCertificate returned no ARN")
	}

	certArn := *resp.CertificateArn

	// Apply optional transparency logging preference via UpdateCertificateOptions
	// (RequestCertificate doesn't accept this field directly).
	if pref, ok := properties["CertificateTransparencyLoggingPreference"].(string); ok && pref != "" {
		_, err := client.UpdateCertificateOptions(ctx, &acm.UpdateCertificateOptionsInput{
			CertificateArn: aws.String(certArn),
			Options: &acmtypes.CertificateOptions{
				CertificateTransparencyLoggingPreference: acmtypes.CertificateTransparencyLoggingPreference(pref),
			},
		})
		if err != nil {
			slog.Warn("acm: UpdateCertificateOptions failed; cert created but transparency pref not applied",
				"error", err, "cert_arn", certArn)
		}
	}

	// Block until the cert reaches ISSUED so downstream resources that
	// depend on the ARN (e.g. CloudFront::Distribution.viewerCertificate)
	// can be created in the same apply. The DNS publisher is wired
	// upstream via runtimeDependency; this loop only observes ACM's view
	// of the validation outcome.
	if err := c.waitForIssued(ctx, client, certArn); err != nil {
		return nil, err
	}

	// Read back so callers get the fully-enriched property set (including
	// any validation records that ACM has already populated).
	props, _ := c.readProperties(ctx, client, certArn)
	propsBytes, _ := json.Marshal(props)

	return &resource.CreateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationCreate,
			OperationStatus:    resource.OperationStatusSuccess,
			NativeID:           certArn,
			ResourceProperties: propsBytes,
		},
	}, nil
}

// waitForIssued polls DescribeCertificate until Status transitions to
// ISSUED, hits a terminal failure (FAILED / REVOKED /
// VALIDATION_TIMED_OUT), the context is cancelled, or the configured
// timeout elapses.
//
// The provisioner makes no assumption about who publishes the validation
// CNAME — that's the responsibility of upstream resources wired via
// runtimeDependency (Route53::RecordSet from this plugin today, or any
// other DNS plugin tomorrow). We only observe ACM's view; AWS owns the
// validation check itself, so DNS-provider work composes via the
// resource graph rather than via provider-aware wait logic here.
func (c *Certificate) waitForIssued(ctx context.Context, client ACMClientInterface, certArn string) error {
	pollInterval := c.pollInterval
	if pollInterval <= 0 {
		pollInterval = defaultPollInterval
	}
	issuedTimeout := c.issuedTimeout
	if issuedTimeout <= 0 {
		issuedTimeout = defaultIssuedTimeout
	}

	deadline := time.Now().Add(issuedTimeout)
	for {
		desc, err := client.DescribeCertificate(ctx, &acm.DescribeCertificateInput{
			CertificateArn: aws.String(certArn),
		})
		if err != nil {
			return fmt.Errorf("acm: DescribeCertificate while waiting for ISSUED: %w", err)
		}
		if desc != nil && desc.Certificate != nil {
			switch desc.Certificate.Status {
			case acmtypes.CertificateStatusIssued:
				return nil
			case acmtypes.CertificateStatusFailed,
				acmtypes.CertificateStatusRevoked,
				acmtypes.CertificateStatusValidationTimedOut:
				return fmt.Errorf("acm: certificate %s reached terminal status %s before issuance", certArn, desc.Certificate.Status)
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("acm: certificate %s still not ISSUED after %s", certArn, issuedTimeout)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("acm: certificate %s wait cancelled: %w", certArn, ctx.Err())
		case <-time.After(pollInterval):
		}
	}
}

// ----- Read -----

func (c *Certificate) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	client, err := c.acmClientFactory(c.cfg)
	if err != nil {
		return nil, fmt.Errorf("acm: build client: %w", err)
	}

	props, err := c.readProperties(ctx, client, request.NativeID)
	if err != nil {
		var rnfe *acmtypes.ResourceNotFoundException
		if errors.As(err, &rnfe) {
			return &resource.ReadResult{
				ResourceType: request.ResourceType,
				ErrorCode:    resource.OperationErrorCodeNotFound,
			}, nil
		}
		return nil, err
	}

	propsBytes, err := json.Marshal(props)
	if err != nil {
		return nil, fmt.Errorf("acm: marshal properties: %w", err)
	}

	return &resource.ReadResult{
		ResourceType: "AWS::CertificateManager::Certificate",
		Properties:   string(propsBytes),
	}, nil
}

// readProperties calls DescribeCertificate + ListTagsForCertificate and
// builds the property map matching the CFN schema shape.
func (c *Certificate) readProperties(
	ctx context.Context,
	client ACMClientInterface,
	certArn string,
) (map[string]any, error) {
	resp, err := client.DescribeCertificate(ctx, &acm.DescribeCertificateInput{
		CertificateArn: aws.String(certArn),
	})
	if err != nil {
		return nil, fmt.Errorf("acm: DescribeCertificate: %w", err)
	}
	if resp == nil || resp.Certificate == nil {
		return nil, fmt.Errorf("acm: DescribeCertificate returned no certificate body for %s", certArn)
	}
	cert := resp.Certificate

	props := map[string]any{
		"Id": certArn,
	}
	if cert.DomainName != nil {
		props["DomainName"] = *cert.DomainName
	}
	// ACM populates SubjectAlternativeNames with [DomainName] when the
	// operator doesn't specify any SANs explicitly. Skip the field when
	// the readback only contains the domain name itself — that's the
	// server default and would otherwise look like drift to the
	// reconciler.
	if len(cert.SubjectAlternativeNames) > 0 {
		domainName := ""
		if cert.DomainName != nil {
			domainName = *cert.DomainName
		}
		isJustDomain := len(cert.SubjectAlternativeNames) == 1 &&
			cert.SubjectAlternativeNames[0] == domainName
		if !isJustDomain {
			props["SubjectAlternativeNames"] = cert.SubjectAlternativeNames
		}
	}
	if cert.KeyAlgorithm != "" {
		// ACM API returns KeyAlgorithm with hyphens (e.g. "RSA-2048"),
		// but the CFN schema enum uses underscores ("RSA_2048"). Mirror
		// the CFN translation so the readback matches the operator-side
		// schema.
		props["KeyAlgorithm"] = normalizeKeyAlgorithmForCFN(string(cert.KeyAlgorithm))
	}
	// ValidationMethod isn't surfaced as a top-level field on the
	// DescribeCertificate response, but ACM stamps every entry in
	// DomainValidationOptions with the method that was used at request
	// time. Pull it from the first entry — all entries carry the same
	// value — so the readback round-trips the createOnly field that the
	// operator declared.
	if len(cert.DomainValidationOptions) > 0 && cert.DomainValidationOptions[0].ValidationMethod != "" {
		props["ValidationMethod"] = string(cert.DomainValidationOptions[0].ValidationMethod)
	}
	if cert.Options != nil && cert.Options.CertificateTransparencyLoggingPreference != "" {
		props["CertificateTransparencyLoggingPreference"] = string(cert.Options.CertificateTransparencyLoggingPreference)
	}

	// ValidationRecords (synthesized from DomainValidationOptions[].ResourceRecord)
	props["ValidationRecords"] = validationRecordsFromCert(cert)

	// Tags
	tagsResp, err := client.ListTagsForCertificate(ctx, &acm.ListTagsForCertificateInput{
		CertificateArn: aws.String(certArn),
	})
	if err == nil && tagsResp != nil && len(tagsResp.Tags) > 0 {
		var tags []map[string]any
		for _, t := range tagsResp.Tags {
			if t.Key == nil {
				continue
			}
			tag := map[string]any{"Key": *t.Key}
			if t.Value != nil {
				tag["Value"] = *t.Value
			}
			tags = append(tags, tag)
		}
		props["Tags"] = tags
	}

	return props, nil
}

// normalizeKeyAlgorithmForCFN translates ACM API key-algorithm values
// (which use hyphens, e.g. "RSA-2048") to the CFN schema's underscore
// form ("RSA_2048"). The two enums are equivalent in meaning but use
// different punctuation; CFN translates internally and the schema
// declares the underscore form, so the Read provisioner has to mirror
// that translation.
func normalizeKeyAlgorithmForCFN(s string) string {
	switch s {
	case "RSA-1024":
		return "RSA_1024"
	case "RSA-2048":
		return "RSA_2048"
	case "RSA-3072":
		return "RSA_3072"
	case "RSA-4096":
		return "RSA_4096"
	}
	// EC values (EC_prime256v1, EC_secp384r1, EC_secp521r1) already use
	// the same form in both APIs; pass through anything we don't
	// explicitly translate.
	return s
}

// validationRecordsFromCert converts ACM's DomainValidationOptions[].ResourceRecord
// into the ses.DnsRecord shape that CertificateResolvable.validationRecords expects.
// Records may be empty if the cert uses EMAIL validation or is too new for ACM
// to have populated the CNAMEs yet.
func validationRecordsFromCert(cert *acmtypes.CertificateDetail) []ses.DnsRecord {
	var records []ses.DnsRecord
	for _, opt := range cert.DomainValidationOptions {
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
	return records
}

// ----- Update -----

func (c *Certificate) Update(ctx context.Context, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	client, err := c.acmClientFactory(c.cfg)
	if err != nil {
		return nil, err
	}

	var prior, desired map[string]any
	if err := json.Unmarshal(request.PriorProperties, &prior); err != nil {
		return nil, fmt.Errorf("acm: parse prior properties: %w", err)
	}
	if err := json.Unmarshal(request.DesiredProperties, &desired); err != nil {
		return nil, fmt.Errorf("acm: parse desired properties: %w", err)
	}

	certArn := request.NativeID

	// 1. Sync tags (additive + subtractive).
	priorTags := tagSetFromProperties(prior)
	desiredTags := tagSetFromProperties(desired)
	toAdd, toRemove := diffTags(priorTags, desiredTags)

	if len(toRemove) > 0 {
		_, err := client.RemoveTagsFromCertificate(ctx, &acm.RemoveTagsFromCertificateInput{
			CertificateArn: aws.String(certArn),
			Tags:           toRemove,
		})
		if err != nil {
			return nil, fmt.Errorf("acm: RemoveTagsFromCertificate: %w", err)
		}
	}
	if len(toAdd) > 0 {
		_, err := client.AddTagsToCertificate(ctx, &acm.AddTagsToCertificateInput{
			CertificateArn: aws.String(certArn),
			Tags:           toAdd,
		})
		if err != nil {
			return nil, fmt.Errorf("acm: AddTagsToCertificate: %w", err)
		}
	}

	// 2. Sync transparency-logging preference (the only other mutable field).
	priorPref, _ := prior["CertificateTransparencyLoggingPreference"].(string)
	desiredPref, _ := desired["CertificateTransparencyLoggingPreference"].(string)
	if desiredPref != "" && desiredPref != priorPref {
		_, err := client.UpdateCertificateOptions(ctx, &acm.UpdateCertificateOptionsInput{
			CertificateArn: aws.String(certArn),
			Options: &acmtypes.CertificateOptions{
				CertificateTransparencyLoggingPreference: acmtypes.CertificateTransparencyLoggingPreference(desiredPref),
			},
		})
		if err != nil {
			return nil, fmt.Errorf("acm: UpdateCertificateOptions: %w", err)
		}
	}

	// 3. Read back for fresh property state.
	props, _ := c.readProperties(ctx, client, certArn)
	propsBytes, _ := json.Marshal(props)

	return &resource.UpdateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationUpdate,
			OperationStatus:    resource.OperationStatusSuccess,
			NativeID:           certArn,
			ResourceProperties: propsBytes,
		},
	}, nil
}

// ----- Delete -----

func (c *Certificate) Delete(ctx context.Context, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	client, err := c.acmClientFactory(c.cfg)
	if err != nil {
		return nil, err
	}

	_, err = client.DeleteCertificate(ctx, &acm.DeleteCertificateInput{
		CertificateArn: aws.String(request.NativeID),
	})
	if err != nil {
		var rnfe *acmtypes.ResourceNotFoundException
		if errors.As(err, &rnfe) {
			return &resource.DeleteResult{
				ProgressResult: &resource.ProgressResult{
					Operation:       resource.OperationDelete,
					OperationStatus: resource.OperationStatusSuccess,
					NativeID:        request.NativeID,
				},
			}, nil
		}
		return nil, fmt.Errorf("acm: DeleteCertificate: %w", err)
	}

	return &resource.DeleteResult{
		ProgressResult: &resource.ProgressResult{
			Operation:       resource.OperationDelete,
			OperationStatus: resource.OperationStatusSuccess,
			NativeID:        request.NativeID,
		},
	}, nil
}

// ----- Status -----

// All ACM operations are synchronous, so Status is only invoked via the
// generic StatusResource path; it does a simple Read to confirm the
// resource exists.
func (c *Certificate) Status(ctx context.Context, request *resource.StatusRequest) (*resource.StatusResult, error) {
	readRes, err := c.Read(ctx, &resource.ReadRequest{
		NativeID:     request.NativeID,
		ResourceType: request.ResourceType,
		TargetConfig: request.TargetConfig,
	})
	if err != nil {
		return nil, err
	}
	status := resource.OperationStatusSuccess
	if readRes.ErrorCode == resource.OperationErrorCodeNotFound {
		// Treat NotFound as Success for Delete-status polls; not all callers
		// distinguish, and ACM has no async polling concept here.
		status = resource.OperationStatusSuccess
	}
	return &resource.StatusResult{
		ProgressResult: &resource.ProgressResult{
			OperationStatus:    status,
			NativeID:           request.NativeID,
			ResourceProperties: json.RawMessage(readRes.Properties),
		},
	}, nil
}

// ----- List -----

func (c *Certificate) List(ctx context.Context, request *resource.ListRequest) (*resource.ListResult, error) {
	client, err := c.acmClientFactory(c.cfg)
	if err != nil {
		return nil, err
	}

	// ACM's ListCertificates defaults Includes.KeyTypes to RSA_1024 and
	// RSA_2048 only. Without an explicit override, EC certs (and RSA-3072
	// / RSA-4096) are silently missing from discovery. Enumerate every
	// algorithm the schema permits so List is complete.
	input := &acm.ListCertificatesInput{
		Includes: &acmtypes.Filters{
			KeyTypes: []acmtypes.KeyAlgorithm{
				acmtypes.KeyAlgorithmRsa1024,
				acmtypes.KeyAlgorithmRsa2048,
				acmtypes.KeyAlgorithmRsa3072,
				acmtypes.KeyAlgorithmRsa4096,
				acmtypes.KeyAlgorithmEcPrime256v1,
				acmtypes.KeyAlgorithmEcSecp384r1,
				acmtypes.KeyAlgorithmEcSecp521r1,
			},
		},
	}
	if request.PageSize > 0 {
		input.MaxItems = aws.Int32(int32(request.PageSize))
	}
	if request.PageToken != nil && *request.PageToken != "" {
		input.NextToken = request.PageToken
	}

	resp, err := client.ListCertificates(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("acm: ListCertificates: %w", err)
	}

	var ids []string
	for _, s := range resp.CertificateSummaryList {
		if s.CertificateArn != nil {
			ids = append(ids, *s.CertificateArn)
		}
	}
	return &resource.ListResult{
		NativeIDs:     ids,
		NextPageToken: resp.NextToken,
	}, nil
}

// ----- helpers -----

// tagsFromProperties parses an "Tags" property of shape
//
//	[{"Key": "...", "Value": "..."}, ...]
//
// into the ACM SDK's Tag slice.
func tagsFromProperties(properties map[string]any) []acmtypes.Tag {
	raw, ok := properties["Tags"].([]any)
	if !ok {
		return nil
	}
	var tags []acmtypes.Tag
	for _, r := range raw {
		m, ok := r.(map[string]any)
		if !ok {
			continue
		}
		k, _ := m["Key"].(string)
		if k == "" {
			continue
		}
		t := acmtypes.Tag{Key: aws.String(k)}
		if v, ok := m["Value"].(string); ok {
			t.Value = aws.String(v)
		}
		tags = append(tags, t)
	}
	return tags
}

// tagSetFromProperties returns the Tags as a key->value map for diffing.
func tagSetFromProperties(properties map[string]any) map[string]string {
	out := map[string]string{}
	raw, ok := properties["Tags"].([]any)
	if !ok {
		return out
	}
	for _, r := range raw {
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

// diffTags compares two tag sets and returns the ACM Tag slices needed to
// reconcile prior -> desired. toRemove includes any keys that are absent
// from desired OR whose value changes (set semantics require removal
// before re-add).
func diffTags(prior, desired map[string]string) (toAdd, toRemove []acmtypes.Tag) {
	for k, dv := range desired {
		pv, present := prior[k]
		if !present || pv != dv {
			toAdd = append(toAdd, acmtypes.Tag{
				Key:   aws.String(k),
				Value: aws.String(dv),
			})
		}
	}
	for k, pv := range prior {
		dv, present := desired[k]
		if !present {
			toRemove = append(toRemove, acmtypes.Tag{
				Key:   aws.String(k),
				Value: aws.String(pv),
			})
		} else if dv != pv {
			toRemove = append(toRemove, acmtypes.Tag{
				Key:   aws.String(k),
				Value: aws.String(pv),
			})
		}
	}
	return toAdd, toRemove
}
