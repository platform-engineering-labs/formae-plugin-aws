// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

// Package kms carries provisioner overrides for KMS resource types.
package kms

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

// Alias wraps CloudControl's Read for AWS::KMS::Alias to keep TargetKeyId in
// the form the stored model uses. CCAPI echoes TargetKeyId as a bare key ID
// even when the alias was created with the key ARN, and a stored ARN and the
// echoed ID name the same key, so reporting the ID back makes every re-apply
// of an ARN-declared alias diff on a value that never changed. When the prior
// model holds a key ARN whose trailing ID equals the freshly read one, Read
// reports the ARN; an alias repointed to a different key keeps the raw read
// value, so real drift still surfaces.
//
// All other operations fall through to CCAPI.
type Alias struct {
	cfg *config.Config
}

var _ prov.Provisioner = &Alias{}

func init() {
	registry.Register("AWS::KMS::Alias",
		[]resource.Operation{resource.OperationRead},
		func(cfg *config.Config) prov.Provisioner {
			return &Alias{cfg: cfg}
		})
}

func (a *Alias) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	client, err := ccx.NewClient(a.cfg)
	if err != nil {
		return nil, err
	}
	result, err := client.ReadResource(ctx, request)
	if err != nil || result == nil || result.Properties == "" || len(request.PriorProperties) == 0 {
		return result, err
	}

	var prior struct {
		TargetKeyId string `json:"TargetKeyId"`
	}
	if err := json.Unmarshal(request.PriorProperties, &prior); err != nil {
		return result, nil
	}

	var props map[string]any
	if err := json.Unmarshal([]byte(result.Properties), &props); err != nil {
		// Pass through; CCAPI's representation is the source of truth.
		return result, nil
	}
	read, _ := props["TargetKeyId"].(string)
	preserved := preserveTargetKeyIDForm(prior.TargetKeyId, read)
	if preserved == read {
		return result, nil
	}

	props["TargetKeyId"] = preserved
	adjusted, err := json.Marshal(props)
	if err != nil {
		return result, nil
	}
	result.Properties = string(adjusted)
	return result, nil
}

func (a *Alias) Create(ctx context.Context, request *resource.CreateRequest) (*resource.CreateResult, error) {
	return nil, fmt.Errorf("kms alias: create handled by cloudcontrol")
}

func (a *Alias) Update(ctx context.Context, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	return nil, fmt.Errorf("kms alias: update handled by cloudcontrol")
}

func (a *Alias) Delete(ctx context.Context, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	return nil, fmt.Errorf("kms alias: delete handled by cloudcontrol")
}

func (a *Alias) Status(ctx context.Context, request *resource.StatusRequest) (*resource.StatusResult, error) {
	return nil, fmt.Errorf("kms alias: status handled by cloudcontrol")
}

func (a *Alias) List(ctx context.Context, request *resource.ListRequest) (*resource.ListResult, error) {
	return nil, fmt.Errorf("kms alias: list handled by cloudcontrol")
}

// preserveTargetKeyIDForm returns prior when it is a key ARN whose trailing
// key ID equals read, and read otherwise. Only the exact same key keeps the
// stored form.
func preserveTargetKeyIDForm(prior, read string) string {
	if prior == "" || read == "" || prior == read {
		return read
	}
	if strings.HasPrefix(prior, "arn:") && strings.Contains(prior, ":key/") && strings.HasSuffix(prior, "/"+read) {
		return prior
	}
	return read
}
