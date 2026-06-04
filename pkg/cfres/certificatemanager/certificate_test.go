// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package certificatemanager

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/acm"
	acmtypes "github.com/aws/aws-sdk-go-v2/service/acm/types"
)

type fakeACMClient struct {
	resp *acm.DescribeCertificateOutput
	err  error
}

func (f *fakeACMClient) DescribeCertificate(_ context.Context, _ *acm.DescribeCertificateInput, _ ...func(*acm.Options)) (*acm.DescribeCertificateOutput, error) {
	return f.resp, f.err
}

func TestSynthesizeValidationRecords_DNSValidation(t *testing.T) {
	resp := &acm.DescribeCertificateOutput{
		Certificate: &acmtypes.CertificateDetail{
			DomainValidationOptions: []acmtypes.DomainValidation{
				{
					DomainName: aws.String("example.com"),
					ResourceRecord: &acmtypes.ResourceRecord{
						Name:  aws.String("_abc.example.com."),
						Type:  acmtypes.RecordTypeCname,
						Value: aws.String("_xyz.acm-validations.aws."),
					},
				},
				{
					DomainName: aws.String("www.example.com"),
					ResourceRecord: &acmtypes.ResourceRecord{
						Name:  aws.String("_def.www.example.com."),
						Type:  acmtypes.RecordTypeCname,
						Value: aws.String("_uvw.acm-validations.aws."),
					},
				},
			},
		},
	}
	client := &fakeACMClient{resp: resp}

	records, err := synthesizeValidationRecords(context.Background(), client, "arn:aws:acm:us-east-1:111111111111:certificate/abcd")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("expected 2 validation records, got %d", len(records))
	}
	if records[0].Type != "CNAME" {
		t.Errorf("record 0 Type: want CNAME, got %q", records[0].Type)
	}
	if records[0].Name != "_abc.example.com." {
		t.Errorf("record 0 Name: want _abc.example.com., got %q", records[0].Name)
	}
	if len(records[0].Values) != 1 || records[0].Values[0] != "_xyz.acm-validations.aws." {
		t.Errorf("record 0 Values: want [_xyz.acm-validations.aws.], got %v", records[0].Values)
	}
}

func TestSynthesizeValidationRecords_PendingNoRecordsYet(t *testing.T) {
	// Immediately after Create, ACM returns DomainValidationOptions entries
	// without ResourceRecord populated. We should return an empty slice.
	resp := &acm.DescribeCertificateOutput{
		Certificate: &acmtypes.CertificateDetail{
			DomainValidationOptions: []acmtypes.DomainValidation{
				{DomainName: aws.String("example.com"), ResourceRecord: nil},
			},
		},
	}
	client := &fakeACMClient{resp: resp}
	records, err := synthesizeValidationRecords(context.Background(), client, "arn:fake")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("expected 0 records (none populated yet), got %d", len(records))
	}
}

func TestSynthesizeValidationRecords_EmailValidation(t *testing.T) {
	// EMAIL-validation certs have no ResourceRecord.
	resp := &acm.DescribeCertificateOutput{
		Certificate: &acmtypes.CertificateDetail{
			DomainValidationOptions: []acmtypes.DomainValidation{
				{DomainName: aws.String("example.com")},
			},
		},
	}
	client := &fakeACMClient{resp: resp}
	records, err := synthesizeValidationRecords(context.Background(), client, "arn:fake")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("expected 0 records (EMAIL validation), got %d", len(records))
	}
}

func TestSynthesizeValidationRecords_APIError_PropagatesNil(t *testing.T) {
	client := &fakeACMClient{err: errors.New("throttled")}
	records, err := synthesizeValidationRecords(context.Background(), client, "arn:fake")
	if err == nil {
		t.Fatal("expected error from API, got nil")
	}
	if records != nil {
		t.Errorf("expected nil records on error, got %v", records)
	}
}
