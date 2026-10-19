// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package ses

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	sesv2types "github.com/aws/aws-sdk-go-v2/service/sesv2/types"

	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres/prov"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres/registry"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/config"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
)

// EventDestination is the AWS::SES::ConfigurationSetEventDestination provisioner.
//
// The type's primary identifier is the two-part composite
// "<Id>|<ConfigurationSetName>", where Id is the read-only event-destination
// name. CloudControl returns that identifier from Create, Status and List, and
// accepts it on Read and Update, so those operations need nothing from this
// package and fall through to the generic client.
//
// Two operations do need overriding:
//
//   - Delete, because CloudControl's DeleteResource reports InternalFailure for
//     this type. SESv2's DeleteConfigurationSetEventDestination takes the two
//     parts separately and applies the delete synchronously.
//   - List, because SES has no top-level "list every event destination" API and
//     CloudControl's ListResources needs a ConfigurationSetName to scope to.
//     Walking the configuration sets is the only way to enumerate them for
//     discovery.
type EventDestination struct {
	cfg              *config.Config
	sesClientFactory func(cfg *config.Config) (SesV2ClientInterface, error)
}

var _ prov.Provisioner = &EventDestination{}

func init() {
	registry.Register("AWS::SES::ConfigurationSetEventDestination",
		[]resource.Operation{
			resource.OperationDelete,
			resource.OperationList,
		},
		func(cfg *config.Config) prov.Provisioner {
			return &EventDestination{
				cfg:              cfg,
				sesClientFactory: defaultSesV2ClientFactory,
			}
		})
}

// The operations below are not registered, so the generic CloudControl client
// serves them; these exist only to satisfy the Provisioner interface.

func (e *EventDestination) Create(ctx context.Context, request *resource.CreateRequest) (*resource.CreateResult, error) {
	return nil, fmt.Errorf("ses eventdestination: create handled by cloudcontrol")
}

func (e *EventDestination) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	return nil, fmt.Errorf("ses eventdestination: read handled by cloudcontrol")
}

func (e *EventDestination) Update(ctx context.Context, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	return nil, fmt.Errorf("ses eventdestination: update handled by cloudcontrol")
}

func (e *EventDestination) Status(ctx context.Context, request *resource.StatusRequest) (*resource.StatusResult, error) {
	return nil, fmt.Errorf("ses eventdestination: status handled by cloudcontrol")
}

// Delete removes the event destination through the SESv2 SDK, because
// CloudControl's DeleteResource reports InternalFailure for this type.
//
// AWS treats deleting a non-existent destination as a NotFound error; we
// translate that to a success ProgressResult so the agent's destroy flow
// is idempotent (matches the ccx.DeleteResource behavior for the same
// case at pkg/ccx/client.go).
func (e *EventDestination) Delete(ctx context.Context, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	edName, csName, ok := splitComposite(request.NativeID)
	if !ok || csName == "" || edName == "" {
		return nil, fmt.Errorf("ses eventdestination Delete: NativeID %q is not a composite <edName>|<csName>", request.NativeID)
	}

	sesClient, err := e.sesClientFactory(e.cfg)
	if err != nil {
		return nil, fmt.Errorf("ses eventdestination Delete: build SES client: %w", err)
	}

	_, err = sesClient.DeleteConfigurationSetEventDestination(ctx, &sesv2.DeleteConfigurationSetEventDestinationInput{
		ConfigurationSetName: &csName,
		EventDestinationName: &edName,
	})
	if err != nil {
		var notFound *sesv2types.NotFoundException
		if errors.As(err, &notFound) {
			return &resource.DeleteResult{
				ProgressResult: &resource.ProgressResult{
					Operation:       resource.OperationDelete,
					OperationStatus: resource.OperationStatusSuccess,
					NativeID:        request.NativeID,
					ErrorCode:       resource.OperationErrorCodeNotFound,
				},
			}, nil
		}
		return nil, fmt.Errorf("ses eventdestination Delete: DeleteConfigurationSetEventDestination(%q, %q): %w", csName, edName, err)
	}

	return &resource.DeleteResult{
		ProgressResult: &resource.ProgressResult{
			Operation:       resource.OperationDelete,
			OperationStatus: resource.OperationStatusSuccess,
			NativeID:        request.NativeID,
		},
	}, nil
}

// List walks every configuration set in the account/region and returns
// composite "<edName>|<csName>" identifiers, the form CloudControl itself
// reports and accepts. SES has no top-level "list all event destinations" API,
// so the parent sets have to be enumerated first.
//
// Pagination: ListConfigurationSets returns up to 100 CSes per page;
// follow NextToken until exhausted. GetConfigurationSetEventDestinations
// returns all destinations for a single CS in one call (per the SES API
// docs, no pagination on that endpoint).
func (e *EventDestination) List(ctx context.Context, request *resource.ListRequest) (*resource.ListResult, error) {
	sesClient, err := e.sesClientFactory(e.cfg)
	if err != nil {
		return nil, fmt.Errorf("ses eventdestination List: build SES client: %w", err)
	}

	var composites []string
	var nextToken *string
	for {
		csOut, err := sesClient.ListConfigurationSets(ctx, &sesv2.ListConfigurationSetsInput{
			NextToken: nextToken,
		})
		if err != nil {
			return nil, fmt.Errorf("ses eventdestination List: ListConfigurationSets: %w", err)
		}
		for _, csName := range csOut.ConfigurationSets {
			cs := csName
			edOut, err := sesClient.GetConfigurationSetEventDestinations(ctx, &sesv2.GetConfigurationSetEventDestinationsInput{
				ConfigurationSetName: &cs,
			})
			if err != nil {
				// Skip CSes we can't read (e.g. just deleted, transient
				// AccessDenied) rather than fail the whole discovery scan.
				continue
			}
			for _, ed := range edOut.EventDestinations {
				if ed.Name == nil || *ed.Name == "" {
					continue
				}
				composites = append(composites, *ed.Name+"|"+cs)
			}
		}
		if csOut.NextToken == nil || *csOut.NextToken == "" {
			break
		}
		nextToken = csOut.NextToken
	}
	return &resource.ListResult{NativeIDs: composites}, nil
}

// splitComposite splits the type's primary identifier, "<edName>|<csName>",
// into its parts. ok is false if the identifier carries no "|" separator.
func splitComposite(nativeID string) (edName, csName string, ok bool) {
	idx := strings.Index(nativeID, "|")
	if idx < 0 {
		return "", "", false
	}
	return nativeID[:idx], nativeID[idx+1:], true
}
