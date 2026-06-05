// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package certificatemanager

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/acm"
	acmtypes "github.com/aws/aws-sdk-go-v2/service/acm/types"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/config"
)

// ----- fake ACM client -----

type fakeACMClient struct {
	requestCertificateInput  *acm.RequestCertificateInput
	requestCertificateOut    *acm.RequestCertificateOutput
	requestCertificateErr    error
	describeCertificateOut   *acm.DescribeCertificateOutput
	describeCertificateOuts  []*acm.DescribeCertificateOutput // when set, consumed per call (last item repeats)
	describeCertificateErr   error
	describeCertificateCalls int
	describeCertificateHook  func(call int) // observe each call; useful for ctx-cancel tests
	deleteCertificateInput   *acm.DeleteCertificateInput
	deleteCertificateErr     error
	addTagsInput             *acm.AddTagsToCertificateInput
	removeTagsInput          *acm.RemoveTagsFromCertificateInput
	listTagsOut              *acm.ListTagsForCertificateOutput
	listCertificatesOut      *acm.ListCertificatesOutput
	listCertificatesErr      error
	updateOptionsInput       *acm.UpdateCertificateOptionsInput
}

func (f *fakeACMClient) RequestCertificate(_ context.Context, in *acm.RequestCertificateInput, _ ...func(*acm.Options)) (*acm.RequestCertificateOutput, error) {
	f.requestCertificateInput = in
	if f.requestCertificateErr != nil {
		return nil, f.requestCertificateErr
	}
	return f.requestCertificateOut, nil
}

func (f *fakeACMClient) DescribeCertificate(_ context.Context, _ *acm.DescribeCertificateInput, _ ...func(*acm.Options)) (*acm.DescribeCertificateOutput, error) {
	f.describeCertificateCalls++
	if f.describeCertificateHook != nil {
		f.describeCertificateHook(f.describeCertificateCalls)
	}
	if f.describeCertificateErr != nil {
		return nil, f.describeCertificateErr
	}
	if len(f.describeCertificateOuts) > 0 {
		idx := f.describeCertificateCalls - 1
		if idx >= len(f.describeCertificateOuts) {
			idx = len(f.describeCertificateOuts) - 1
		}
		return f.describeCertificateOuts[idx], nil
	}
	return f.describeCertificateOut, nil
}

func (f *fakeACMClient) DeleteCertificate(_ context.Context, in *acm.DeleteCertificateInput, _ ...func(*acm.Options)) (*acm.DeleteCertificateOutput, error) {
	f.deleteCertificateInput = in
	return &acm.DeleteCertificateOutput{}, f.deleteCertificateErr
}

func (f *fakeACMClient) AddTagsToCertificate(_ context.Context, in *acm.AddTagsToCertificateInput, _ ...func(*acm.Options)) (*acm.AddTagsToCertificateOutput, error) {
	f.addTagsInput = in
	return &acm.AddTagsToCertificateOutput{}, nil
}

func (f *fakeACMClient) RemoveTagsFromCertificate(_ context.Context, in *acm.RemoveTagsFromCertificateInput, _ ...func(*acm.Options)) (*acm.RemoveTagsFromCertificateOutput, error) {
	f.removeTagsInput = in
	return &acm.RemoveTagsFromCertificateOutput{}, nil
}

func (f *fakeACMClient) ListTagsForCertificate(_ context.Context, _ *acm.ListTagsForCertificateInput, _ ...func(*acm.Options)) (*acm.ListTagsForCertificateOutput, error) {
	if f.listTagsOut == nil {
		return &acm.ListTagsForCertificateOutput{}, nil
	}
	return f.listTagsOut, nil
}

func (f *fakeACMClient) ListCertificates(_ context.Context, _ *acm.ListCertificatesInput, _ ...func(*acm.Options)) (*acm.ListCertificatesOutput, error) {
	return f.listCertificatesOut, f.listCertificatesErr
}

func (f *fakeACMClient) UpdateCertificateOptions(_ context.Context, in *acm.UpdateCertificateOptionsInput, _ ...func(*acm.Options)) (*acm.UpdateCertificateOptionsOutput, error) {
	f.updateOptionsInput = in
	return &acm.UpdateCertificateOptionsOutput{}, nil
}

// newCertificateWithFake wires a Certificate provisioner against the
// supplied fake client with a very short poll interval so wait-for-ISSUED
// tests complete in microseconds rather than seconds. Tests that need
// to drive the timeout path override issuedTimeout directly afterwards.
func newCertificateWithFake(fake *fakeACMClient) *Certificate {
	return &Certificate{
		cfg: &config.Config{Region: "us-east-1"},
		acmClientFactory: func(_ *config.Config) (ACMClientInterface, error) {
			return fake, nil
		},
		pollInterval:  time.Microsecond,
		issuedTimeout: 5 * time.Second,
	}
}

// ----- Create -----

func TestCreate_DnsValidation_RequestsCertificateAndReturnsArn(t *testing.T) {
	fake := &fakeACMClient{
		requestCertificateOut: &acm.RequestCertificateOutput{
			CertificateArn: aws.String("arn:aws:acm:us-east-1:111:certificate/abcd"),
		},
		describeCertificateOut: &acm.DescribeCertificateOutput{
			Certificate: &acmtypes.CertificateDetail{
				CertificateArn: aws.String("arn:aws:acm:us-east-1:111:certificate/abcd"),
				DomainName:     aws.String("example.com"),
				KeyAlgorithm:   acmtypes.KeyAlgorithmRsa2048,
				Status:         acmtypes.CertificateStatusIssued,
			},
		},
	}
	cert := newCertificateWithFake(fake)

	props := map[string]any{
		"DomainName":       "example.com",
		"ValidationMethod": "DNS",
		"KeyAlgorithm":     "RSA_2048",
	}
	body, _ := json.Marshal(props)
	res, err := cert.Create(context.Background(), &resource.CreateRequest{
		Properties: body,
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	if res.ProgressResult.OperationStatus != resource.OperationStatusSuccess {
		t.Errorf("OperationStatus: want Success, got %v", res.ProgressResult.OperationStatus)
	}
	if res.ProgressResult.NativeID != "arn:aws:acm:us-east-1:111:certificate/abcd" {
		t.Errorf("NativeID: want cert ARN, got %q", res.ProgressResult.NativeID)
	}
	if fake.requestCertificateInput == nil {
		t.Fatal("expected RequestCertificate to be called")
	}
	if aws.ToString(fake.requestCertificateInput.DomainName) != "example.com" {
		t.Errorf("RequestCertificate DomainName: want example.com, got %q", aws.ToString(fake.requestCertificateInput.DomainName))
	}
	if fake.requestCertificateInput.ValidationMethod != acmtypes.ValidationMethodDns {
		t.Errorf("ValidationMethod: want DNS, got %v", fake.requestCertificateInput.ValidationMethod)
	}
	if fake.requestCertificateInput.KeyAlgorithm != acmtypes.KeyAlgorithmRsa2048 {
		t.Errorf("KeyAlgorithm: want RSA_2048, got %v", fake.requestCertificateInput.KeyAlgorithm)
	}
}

func TestCreate_SansAndTags_PassThroughToAPI(t *testing.T) {
	fake := &fakeACMClient{
		requestCertificateOut: &acm.RequestCertificateOutput{
			CertificateArn: aws.String("arn:fake"),
		},
		describeCertificateOut: &acm.DescribeCertificateOutput{
			Certificate: &acmtypes.CertificateDetail{
				DomainName: aws.String("example.com"),
				Status:     acmtypes.CertificateStatusIssued,
			},
		},
	}
	cert := newCertificateWithFake(fake)

	props := map[string]any{
		"DomainName":              "example.com",
		"SubjectAlternativeNames": []any{"www.example.com", "api.example.com"},
		"ValidationMethod":        "DNS",
		"Tags": []any{
			map[string]any{"Key": "Owner", "Value": "team"},
			map[string]any{"Key": "Env", "Value": "test"},
		},
	}
	body, _ := json.Marshal(props)
	_, err := cert.Create(context.Background(), &resource.CreateRequest{Properties: body})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	if len(fake.requestCertificateInput.SubjectAlternativeNames) != 2 {
		t.Errorf("SANs: want 2, got %d", len(fake.requestCertificateInput.SubjectAlternativeNames))
	}
	if len(fake.requestCertificateInput.Tags) != 2 {
		t.Errorf("Tags: want 2, got %d", len(fake.requestCertificateInput.Tags))
	}
}

func TestCreate_TransparencyPreference_AppliedViaUpdateOptions(t *testing.T) {
	fake := &fakeACMClient{
		requestCertificateOut: &acm.RequestCertificateOutput{
			CertificateArn: aws.String("arn:fake"),
		},
		describeCertificateOut: &acm.DescribeCertificateOutput{
			Certificate: &acmtypes.CertificateDetail{
				DomainName: aws.String("example.com"),
				Status:     acmtypes.CertificateStatusIssued,
			},
		},
	}
	cert := newCertificateWithFake(fake)

	props := map[string]any{
		"DomainName":                               "example.com",
		"ValidationMethod":                         "DNS",
		"CertificateTransparencyLoggingPreference": "DISABLED",
	}
	body, _ := json.Marshal(props)
	_, err := cert.Create(context.Background(), &resource.CreateRequest{Properties: body})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	if fake.updateOptionsInput == nil {
		t.Fatal("expected UpdateCertificateOptions to be called for transparency pref")
	}
	if fake.updateOptionsInput.Options.CertificateTransparencyLoggingPreference != acmtypes.CertificateTransparencyLoggingPreferenceDisabled {
		t.Errorf("transparency pref: want DISABLED, got %v",
			fake.updateOptionsInput.Options.CertificateTransparencyLoggingPreference)
	}
}

func TestCreate_RequestCertificateError_BubblesUp(t *testing.T) {
	fake := &fakeACMClient{
		requestCertificateErr: errors.New("rate exceeded"),
	}
	cert := newCertificateWithFake(fake)

	props := map[string]any{"DomainName": "example.com"}
	body, _ := json.Marshal(props)
	_, err := cert.Create(context.Background(), &resource.CreateRequest{Properties: body})
	if err == nil {
		t.Fatal("expected Create to error when RequestCertificate fails")
	}
}

// ----- Create: wait-for-ISSUED -----
//
// ACM RequestCertificate returns the new ARN while the cert is still
// PENDING_VALIDATION. CloudFront's Distribution.viewerCertificate
// requires the cert to be ISSUED before it accepts the ARN, so Create
// must block until ACM reports ISSUED (or a terminal failure) so the
// changeset can wire downstream resources in a single apply. The DNS
// publisher is wired upstream via runtimeDependency; this loop does not
// query Route53 (the publisher could be Cloudflare or operator-manual).

func TestCreate_WaitForIssued_TransitionsFromPendingToIssued(t *testing.T) {
	fake := &fakeACMClient{
		requestCertificateOut: &acm.RequestCertificateOutput{
			CertificateArn: aws.String("arn:fake"),
		},
		describeCertificateOuts: []*acm.DescribeCertificateOutput{
			{Certificate: &acmtypes.CertificateDetail{
				CertificateArn: aws.String("arn:fake"),
				DomainName:     aws.String("example.com"),
				Status:         acmtypes.CertificateStatusPendingValidation,
			}},
			{Certificate: &acmtypes.CertificateDetail{
				CertificateArn: aws.String("arn:fake"),
				DomainName:     aws.String("example.com"),
				Status:         acmtypes.CertificateStatusPendingValidation,
			}},
			{Certificate: &acmtypes.CertificateDetail{
				CertificateArn: aws.String("arn:fake"),
				DomainName:     aws.String("example.com"),
				Status:         acmtypes.CertificateStatusIssued,
			}},
		},
	}
	cert := newCertificateWithFake(fake)

	props := map[string]any{"DomainName": "example.com", "ValidationMethod": "DNS"}
	body, _ := json.Marshal(props)
	res, err := cert.Create(context.Background(), &resource.CreateRequest{Properties: body})
	if err != nil {
		t.Fatalf("Create failed after eventual ISSUED: %v", err)
	}
	if res.ProgressResult.OperationStatus != resource.OperationStatusSuccess {
		t.Errorf("OperationStatus: want Success, got %v", res.ProgressResult.OperationStatus)
	}
	// 3 polls for state transitions + 1 readback at the end.
	if fake.describeCertificateCalls < 3 {
		t.Errorf("expected at least 3 DescribeCertificate calls during wait, got %d", fake.describeCertificateCalls)
	}
}

func TestCreate_WaitForIssued_StatusFailed_ReturnsError(t *testing.T) {
	fake := &fakeACMClient{
		requestCertificateOut: &acm.RequestCertificateOutput{
			CertificateArn: aws.String("arn:fake"),
		},
		describeCertificateOut: &acm.DescribeCertificateOutput{
			Certificate: &acmtypes.CertificateDetail{
				CertificateArn: aws.String("arn:fake"),
				Status:         acmtypes.CertificateStatusFailed,
			},
		},
	}
	cert := newCertificateWithFake(fake)

	props := map[string]any{"DomainName": "example.com", "ValidationMethod": "DNS"}
	body, _ := json.Marshal(props)
	_, err := cert.Create(context.Background(), &resource.CreateRequest{Properties: body})
	if err == nil {
		t.Fatal("expected Create to error on terminal FAILED status")
	}
}

func TestCreate_WaitForIssued_StatusRevoked_ReturnsError(t *testing.T) {
	fake := &fakeACMClient{
		requestCertificateOut: &acm.RequestCertificateOutput{
			CertificateArn: aws.String("arn:fake"),
		},
		describeCertificateOut: &acm.DescribeCertificateOutput{
			Certificate: &acmtypes.CertificateDetail{
				CertificateArn: aws.String("arn:fake"),
				Status:         acmtypes.CertificateStatusRevoked,
			},
		},
	}
	cert := newCertificateWithFake(fake)

	props := map[string]any{"DomainName": "example.com", "ValidationMethod": "DNS"}
	body, _ := json.Marshal(props)
	_, err := cert.Create(context.Background(), &resource.CreateRequest{Properties: body})
	if err == nil {
		t.Fatal("expected Create to error on terminal REVOKED status")
	}
}

func TestCreate_WaitForIssued_StatusValidationTimedOut_ReturnsError(t *testing.T) {
	fake := &fakeACMClient{
		requestCertificateOut: &acm.RequestCertificateOutput{
			CertificateArn: aws.String("arn:fake"),
		},
		describeCertificateOut: &acm.DescribeCertificateOutput{
			Certificate: &acmtypes.CertificateDetail{
				CertificateArn: aws.String("arn:fake"),
				Status:         acmtypes.CertificateStatusValidationTimedOut,
			},
		},
	}
	cert := newCertificateWithFake(fake)

	props := map[string]any{"DomainName": "example.com", "ValidationMethod": "DNS"}
	body, _ := json.Marshal(props)
	_, err := cert.Create(context.Background(), &resource.CreateRequest{Properties: body})
	if err == nil {
		t.Fatal("expected Create to error on terminal VALIDATION_TIMED_OUT status")
	}
}

func TestCreate_WaitForIssued_ContextCancelled_ReturnsError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	fake := &fakeACMClient{
		requestCertificateOut: &acm.RequestCertificateOutput{
			CertificateArn: aws.String("arn:fake"),
		},
		describeCertificateOut: &acm.DescribeCertificateOutput{
			Certificate: &acmtypes.CertificateDetail{
				CertificateArn: aws.String("arn:fake"),
				Status:         acmtypes.CertificateStatusPendingValidation,
			},
		},
	}
	// Cancel on the second describe call (first one returns PENDING and loop continues).
	fake.describeCertificateHook = func(call int) {
		if call == 2 {
			cancel()
		}
	}
	cert := newCertificateWithFake(fake)

	props := map[string]any{"DomainName": "example.com", "ValidationMethod": "DNS"}
	body, _ := json.Marshal(props)
	_, err := cert.Create(ctx, &resource.CreateRequest{Properties: body})
	if err == nil {
		t.Fatal("expected Create to error when ctx is cancelled mid-wait")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected wrapped context.Canceled, got %v", err)
	}
}

func TestCreate_WaitForIssued_EnvOverride_SkipsWait(t *testing.T) {
	t.Setenv("FORMAE_AWS_CERT_SKIP_ISSUED_WAIT", "1")
	fake := &fakeACMClient{
		requestCertificateOut: &acm.RequestCertificateOutput{
			CertificateArn: aws.String("arn:fake"),
		},
		// Status is PENDING_VALIDATION but the env override should make
		// Create return without polling further than the readback.
		describeCertificateOut: &acm.DescribeCertificateOutput{
			Certificate: &acmtypes.CertificateDetail{
				CertificateArn: aws.String("arn:fake"),
				Status:         acmtypes.CertificateStatusPendingValidation,
			},
		},
	}
	cert := newCertificateWithFake(fake)
	cert.issuedTimeout = time.Hour // would normally block; the override must skip the wait

	props := map[string]any{"DomainName": "test.example", "ValidationMethod": "DNS"}
	body, _ := json.Marshal(props)
	res, err := cert.Create(context.Background(), &resource.CreateRequest{Properties: body})
	if err != nil {
		t.Fatalf("Create should succeed when env override is set, got %v", err)
	}
	if res.ProgressResult.OperationStatus != resource.OperationStatusSuccess {
		t.Errorf("OperationStatus: want Success, got %v", res.ProgressResult.OperationStatus)
	}
}

func TestCreate_WaitForIssued_Timeout_ReturnsError(t *testing.T) {
	fake := &fakeACMClient{
		requestCertificateOut: &acm.RequestCertificateOutput{
			CertificateArn: aws.String("arn:fake"),
		},
		describeCertificateOut: &acm.DescribeCertificateOutput{
			Certificate: &acmtypes.CertificateDetail{
				CertificateArn: aws.String("arn:fake"),
				Status:         acmtypes.CertificateStatusPendingValidation,
			},
		},
	}
	cert := newCertificateWithFake(fake)
	cert.issuedTimeout = 5 * time.Millisecond

	props := map[string]any{"DomainName": "example.com", "ValidationMethod": "DNS"}
	body, _ := json.Marshal(props)
	_, err := cert.Create(context.Background(), &resource.CreateRequest{Properties: body})
	if err == nil {
		t.Fatal("expected Create to error when wait times out without ISSUED")
	}
}

// ----- Read -----

func TestRead_PopulatesValidationRecords(t *testing.T) {
	fake := &fakeACMClient{
		describeCertificateOut: &acm.DescribeCertificateOutput{
			Certificate: &acmtypes.CertificateDetail{
				CertificateArn: aws.String("arn:fake"),
				DomainName:     aws.String("example.com"),
				DomainValidationOptions: []acmtypes.DomainValidation{
					{
						DomainName: aws.String("example.com"),
						ResourceRecord: &acmtypes.ResourceRecord{
							Name:  aws.String("_abc.example.com."),
							Type:  acmtypes.RecordTypeCname,
							Value: aws.String("_xyz.acm-validations.aws."),
						},
					},
				},
			},
		},
	}
	cert := newCertificateWithFake(fake)

	res, err := cert.Read(context.Background(), &resource.ReadRequest{
		NativeID:     "arn:fake",
		ResourceType: "AWS::CertificateManager::Certificate",
	})
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	if res.ErrorCode != "" {
		t.Errorf("unexpected ErrorCode: %v", res.ErrorCode)
	}

	var props map[string]any
	if err := json.Unmarshal([]byte(res.Properties), &props); err != nil {
		t.Fatalf("unmarshal properties: %v", err)
	}
	records, ok := props["ValidationRecords"].([]any)
	if !ok {
		t.Fatalf("expected ValidationRecords list, got %T", props["ValidationRecords"])
	}
	if len(records) != 1 {
		t.Errorf("ValidationRecords: want 1, got %d", len(records))
	}
}

func TestRead_PopulatesValidationMethodFromDomainValidationOption(t *testing.T) {
	fake := &fakeACMClient{
		describeCertificateOut: &acm.DescribeCertificateOutput{
			Certificate: &acmtypes.CertificateDetail{
				CertificateArn: aws.String("arn:fake"),
				DomainName:     aws.String("example.com"),
				DomainValidationOptions: []acmtypes.DomainValidation{
					{
						DomainName:       aws.String("example.com"),
						ValidationMethod: acmtypes.ValidationMethodDns,
					},
				},
			},
		},
	}
	cert := newCertificateWithFake(fake)

	res, err := cert.Read(context.Background(), &resource.ReadRequest{
		NativeID:     "arn:fake",
		ResourceType: "AWS::CertificateManager::Certificate",
	})
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}

	var props map[string]any
	if err := json.Unmarshal([]byte(res.Properties), &props); err != nil {
		t.Fatalf("unmarshal properties: %v", err)
	}
	vm, ok := props["ValidationMethod"].(string)
	if !ok {
		t.Fatalf("expected ValidationMethod string, got %T", props["ValidationMethod"])
	}
	if vm != "DNS" {
		t.Errorf("ValidationMethod: want DNS, got %q", vm)
	}
}

func TestRead_NotFound_ReturnsErrorCode(t *testing.T) {
	fake := &fakeACMClient{
		describeCertificateErr: &acmtypes.ResourceNotFoundException{Message: aws.String("nope")},
	}
	cert := newCertificateWithFake(fake)

	res, err := cert.Read(context.Background(), &resource.ReadRequest{
		NativeID:     "arn:gone",
		ResourceType: "AWS::CertificateManager::Certificate",
	})
	if err != nil {
		t.Fatalf("Read should not error on NotFound, got: %v", err)
	}
	if res.ErrorCode != resource.OperationErrorCodeNotFound {
		t.Errorf("ErrorCode: want NotFound, got %v", res.ErrorCode)
	}
}

// ----- Update -----

func TestUpdate_TagsAddedAndRemoved(t *testing.T) {
	fake := &fakeACMClient{
		describeCertificateOut: &acm.DescribeCertificateOutput{
			Certificate: &acmtypes.CertificateDetail{DomainName: aws.String("example.com")},
		},
	}
	cert := newCertificateWithFake(fake)

	prior := map[string]any{
		"DomainName": "example.com",
		"Tags": []any{
			map[string]any{"Key": "Owner", "Value": "team"},
			map[string]any{"Key": "Stale", "Value": "yes"},
		},
	}
	desired := map[string]any{
		"DomainName": "example.com",
		"Tags": []any{
			map[string]any{"Key": "Owner", "Value": "team"},
			map[string]any{"Key": "Env", "Value": "prod"},
		},
	}
	priorBody, _ := json.Marshal(prior)
	desiredBody, _ := json.Marshal(desired)
	_, err := cert.Update(context.Background(), &resource.UpdateRequest{
		NativeID:          "arn:fake",
		PriorProperties:   priorBody,
		DesiredProperties: desiredBody,
	})
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}
	if fake.addTagsInput == nil {
		t.Fatal("expected AddTagsToCertificate to be called for new tags")
	}
	addedKeys := tagKeys(fake.addTagsInput.Tags)
	if !equalStringSlices(addedKeys, []string{"Env"}) {
		t.Errorf("added tag keys: want [Env], got %v", addedKeys)
	}
	if fake.removeTagsInput == nil {
		t.Fatal("expected RemoveTagsFromCertificate to be called for stale tags")
	}
	removedKeys := tagKeys(fake.removeTagsInput.Tags)
	if !equalStringSlices(removedKeys, []string{"Stale"}) {
		t.Errorf("removed tag keys: want [Stale], got %v", removedKeys)
	}
}

func TestUpdate_TransparencyPrefChange_AppliesOptions(t *testing.T) {
	fake := &fakeACMClient{
		describeCertificateOut: &acm.DescribeCertificateOutput{
			Certificate: &acmtypes.CertificateDetail{DomainName: aws.String("example.com")},
		},
	}
	cert := newCertificateWithFake(fake)

	prior := map[string]any{
		"CertificateTransparencyLoggingPreference": "ENABLED",
	}
	desired := map[string]any{
		"CertificateTransparencyLoggingPreference": "DISABLED",
	}
	priorBody, _ := json.Marshal(prior)
	desiredBody, _ := json.Marshal(desired)
	_, err := cert.Update(context.Background(), &resource.UpdateRequest{
		NativeID:          "arn:fake",
		PriorProperties:   priorBody,
		DesiredProperties: desiredBody,
	})
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}
	if fake.updateOptionsInput == nil {
		t.Fatal("expected UpdateCertificateOptions to be called")
	}
	if fake.updateOptionsInput.Options.CertificateTransparencyLoggingPreference != acmtypes.CertificateTransparencyLoggingPreferenceDisabled {
		t.Errorf("transparency pref: want DISABLED, got %v",
			fake.updateOptionsInput.Options.CertificateTransparencyLoggingPreference)
	}
}

// ----- Delete -----

func TestDelete_NotFound_IsIdempotentSuccess(t *testing.T) {
	fake := &fakeACMClient{
		deleteCertificateErr: &acmtypes.ResourceNotFoundException{Message: aws.String("gone")},
	}
	cert := newCertificateWithFake(fake)

	res, err := cert.Delete(context.Background(), &resource.DeleteRequest{
		NativeID: "arn:gone",
	})
	if err != nil {
		t.Fatalf("Delete should not error on NotFound, got: %v", err)
	}
	if res.ProgressResult.OperationStatus != resource.OperationStatusSuccess {
		t.Errorf("OperationStatus on NotFound: want Success, got %v", res.ProgressResult.OperationStatus)
	}
}

func TestDelete_OtherError_BubblesUp(t *testing.T) {
	fake := &fakeACMClient{
		deleteCertificateErr: errors.New("ResourceInUse"),
	}
	cert := newCertificateWithFake(fake)

	_, err := cert.Delete(context.Background(), &resource.DeleteRequest{
		NativeID: "arn:in-use",
	})
	if err == nil {
		t.Fatal("expected Delete to bubble up non-NotFound errors")
	}
}

// ----- List -----

func TestList_ReturnsArnsAndNextToken(t *testing.T) {
	fake := &fakeACMClient{
		listCertificatesOut: &acm.ListCertificatesOutput{
			CertificateSummaryList: []acmtypes.CertificateSummary{
				{CertificateArn: aws.String("arn:1")},
				{CertificateArn: aws.String("arn:2")},
			},
			NextToken: aws.String("page-2"),
		},
	}
	cert := newCertificateWithFake(fake)

	res, err := cert.List(context.Background(), &resource.ListRequest{})
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if !equalStringSlices(res.NativeIDs, []string{"arn:1", "arn:2"}) {
		t.Errorf("NativeIDs: %v", res.NativeIDs)
	}
	if aws.ToString(res.NextPageToken) != "page-2" {
		t.Errorf("NextPageToken: want page-2, got %q", aws.ToString(res.NextPageToken))
	}
}

// ----- helpers used only by tests -----

func tagKeys(tags []acmtypes.Tag) []string {
	keys := make([]string, 0, len(tags))
	for _, t := range tags {
		keys = append(keys, aws.ToString(t.Key))
	}
	sort.Strings(keys)
	return keys
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
