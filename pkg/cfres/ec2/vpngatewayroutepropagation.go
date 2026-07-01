// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package ec2

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ec2sdk "github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/smithy-go"

	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres/prov"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres/registry"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres/utils"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/config"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
)

// AWS::EC2::VPNGatewayRoutePropagation is NON_PROVISIONABLE in Cloud Control, so
// it goes through a custom provisioner driving the EC2 Enable/DisableVgwRoutePropagation
// API directly. There is no dedicated Describe call: current state is read from the
// route table's PropagatingVgws via DescribeRouteTables.
const vpnGatewayRoutePropagationType = "AWS::EC2::VPNGatewayRoutePropagation"

type vpnGatewayRoutePropagationClientInterface interface {
	EnableVgwRoutePropagation(ctx context.Context, params *ec2sdk.EnableVgwRoutePropagationInput, optFns ...func(*ec2sdk.Options)) (*ec2sdk.EnableVgwRoutePropagationOutput, error)
	DisableVgwRoutePropagation(ctx context.Context, params *ec2sdk.DisableVgwRoutePropagationInput, optFns ...func(*ec2sdk.Options)) (*ec2sdk.DisableVgwRoutePropagationOutput, error)
	DescribeRouteTables(ctx context.Context, params *ec2sdk.DescribeRouteTablesInput, optFns ...func(*ec2sdk.Options)) (*ec2sdk.DescribeRouteTablesOutput, error)
}

type VPNGatewayRoutePropagation struct {
	cfg *config.Config

	// readAttempts and readBackoff bound the read-after-enable consistency poll on
	// Create; sleep is the wait between polls (injectable for tests).
	readAttempts int
	readBackoff  time.Duration
	sleep        func(time.Duration)
}

var _ prov.Provisioner = &VPNGatewayRoutePropagation{}

func init() {
	registry.Register(vpnGatewayRoutePropagationType,
		[]resource.Operation{
			resource.OperationCreate,
			resource.OperationRead,
			resource.OperationDelete,
		},
		func(cfg *config.Config) prov.Provisioner {
			return &VPNGatewayRoutePropagation{
				cfg:          cfg,
				readAttempts: 10,
				readBackoff:  2 * time.Second,
				sleep:        time.Sleep,
			}
		})
}

// parseVPNGatewayRoutePropagationNativeID parses the composite NativeID
// routeTableId|vpnGatewayId. It requires exactly two non-empty parts, rejecting
// "", "|", "rtb-1|", "|vgw-1" and "a|b|c".
func parseVPNGatewayRoutePropagationNativeID(nativeID string) (routeTableID, vpnGatewayID string, err error) {
	parts := strings.Split(nativeID, "|")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid NativeID format: expected routeTableId|vpnGatewayId, got: %q", nativeID)
	}
	return parts[0], parts[1], nil
}

// isRouteTableNotFoundErr reports whether err is the AWS InvalidRouteTableID.NotFound
// error (the route table no longer exists). Only this error is treated as NotFound;
// any other AWS error (auth, throttle, validation, outage) is surfaced.
func isRouteTableNotFoundErr(err error) bool {
	var ae smithy.APIError
	if errors.As(err, &ae) {
		return ae.ErrorCode() == "InvalidRouteTableID.NotFound"
	}
	return false
}

// vgwIsPropagating reports whether vpnGatewayID is propagating routes into
// routeTableID. A missing route table reads as not-propagating (false, nil); any
// other DescribeRouteTables error is surfaced.
func vgwIsPropagating(ctx context.Context, client vpnGatewayRoutePropagationClientInterface, routeTableID, vpnGatewayID string) (bool, error) {
	resp, err := client.DescribeRouteTables(ctx, &ec2sdk.DescribeRouteTablesInput{
		RouteTableIds: []string{routeTableID},
	})
	if err != nil {
		if isRouteTableNotFoundErr(err) {
			return false, nil
		}
		return false, err
	}
	if len(resp.RouteTables) == 0 {
		return false, nil
	}
	for _, vgw := range resp.RouteTables[0].PropagatingVgws {
		if vgw.GatewayId != nil && *vgw.GatewayId == vpnGatewayID {
			return true, nil
		}
	}
	return false, nil
}

func (p *VPNGatewayRoutePropagation) Create(ctx context.Context, request *resource.CreateRequest) (*resource.CreateResult, error) {
	awsCfg, err := p.cfg.ToAwsConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading AWS config: %w", err)
	}
	client := ec2sdk.NewFromConfig(awsCfg)
	return p.createWithClient(ctx, client, request)
}

func (p *VPNGatewayRoutePropagation) createWithClient(ctx context.Context, client vpnGatewayRoutePropagationClientInterface, request *resource.CreateRequest) (*resource.CreateResult, error) {
	var props map[string]any
	if err := json.Unmarshal(request.Properties, &props); err != nil {
		return nil, fmt.Errorf("parsing properties: %w", err)
	}
	routeTableID, err := utils.GetStringProperty(props, "RouteTableId")
	if err != nil {
		return nil, fmt.Errorf("invalid RouteTableId: %w", err)
	}
	vpnGatewayID, err := utils.GetStringProperty(props, "VpnGatewayId")
	if err != nil {
		return nil, fmt.Errorf("invalid VpnGatewayId: %w", err)
	}

	nativeID := fmt.Sprintf("%s|%s", routeTableID, vpnGatewayID)

	// Idempotent: if the pair is already propagating, succeed without re-enabling.
	present, err := vgwIsPropagating(ctx, client, routeTableID, vpnGatewayID)
	if err != nil {
		return nil, fmt.Errorf("checking existing route propagation: %w", err)
	}

	if !present {
		if _, err := client.EnableVgwRoutePropagation(ctx, &ec2sdk.EnableVgwRoutePropagationInput{
			RouteTableId: aws.String(routeTableID),
			GatewayId:    aws.String(vpnGatewayID),
		}); err != nil {
			return nil, fmt.Errorf("enabling VGW route propagation: %w", err)
		}

		// EnableVgwRoutePropagation is synchronous, but DescribeRouteTables may not
		// reflect the new VGW immediately. Poll briefly so a Read right after Create
		// does not spuriously report NotFound.
		for attempt := 0; attempt < p.readAttempts; attempt++ {
			present, err = vgwIsPropagating(ctx, client, routeTableID, vpnGatewayID)
			if err != nil {
				return nil, fmt.Errorf("confirming route propagation: %w", err)
			}
			if present {
				break
			}
			if attempt < p.readAttempts-1 {
				p.sleep(p.readBackoff)
			}
		}
	}

	return &resource.CreateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:       resource.OperationCreate,
			OperationStatus: resource.OperationStatusSuccess,
			NativeID:        nativeID,
		},
	}, nil
}

func (p *VPNGatewayRoutePropagation) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	awsCfg, err := p.cfg.ToAwsConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading AWS config: %w", err)
	}
	client := ec2sdk.NewFromConfig(awsCfg)
	return p.readWithClient(ctx, client, request)
}

func (p *VPNGatewayRoutePropagation) readWithClient(ctx context.Context, client vpnGatewayRoutePropagationClientInterface, request *resource.ReadRequest) (*resource.ReadResult, error) {
	routeTableID, vpnGatewayID, err := parseVPNGatewayRoutePropagationNativeID(request.NativeID)
	if err != nil {
		return nil, err
	}

	present, err := vgwIsPropagating(ctx, client, routeTableID, vpnGatewayID)
	if err != nil {
		return nil, fmt.Errorf("describing route tables: %w", err)
	}
	if !present {
		return &resource.ReadResult{
			ResourceType: vpnGatewayRoutePropagationType,
			ErrorCode:    resource.OperationErrorCodeNotFound,
		}, nil
	}

	propBytes, err := json.Marshal(map[string]any{
		"RouteTableId": routeTableID,
		"VpnGatewayId": vpnGatewayID,
	})
	if err != nil {
		return nil, fmt.Errorf("marshaling properties: %w", err)
	}
	return &resource.ReadResult{
		ResourceType: vpnGatewayRoutePropagationType,
		Properties:   string(propBytes),
	}, nil
}

func (p *VPNGatewayRoutePropagation) Delete(ctx context.Context, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	awsCfg, err := p.cfg.ToAwsConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading AWS config: %w", err)
	}
	client := ec2sdk.NewFromConfig(awsCfg)
	return p.deleteWithClient(ctx, client, request)
}

func (p *VPNGatewayRoutePropagation) deleteWithClient(ctx context.Context, client vpnGatewayRoutePropagationClientInterface, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	routeTableID, vpnGatewayID, err := parseVPNGatewayRoutePropagationNativeID(request.NativeID)
	if err != nil {
		return nil, err
	}

	// Idempotent: if the pair is already not propagating, there is nothing to disable.
	present, err := vgwIsPropagating(ctx, client, routeTableID, vpnGatewayID)
	if err != nil {
		return nil, fmt.Errorf("checking route propagation before delete: %w", err)
	}
	if present {
		if _, err := client.DisableVgwRoutePropagation(ctx, &ec2sdk.DisableVgwRoutePropagationInput{
			RouteTableId: aws.String(routeTableID),
			GatewayId:    aws.String(vpnGatewayID),
		}); err != nil {
			return nil, fmt.Errorf("disabling VGW route propagation: %w", err)
		}
	}

	return &resource.DeleteResult{
		ProgressResult: &resource.ProgressResult{
			Operation:       resource.OperationDelete,
			OperationStatus: resource.OperationStatusSuccess,
			NativeID:        request.NativeID,
		},
	}, nil
}

// Update is never invoked: both schema fields are createOnly, so any change is a
// replace (Delete then Create). The method exists only to satisfy prov.Provisioner.
func (p *VPNGatewayRoutePropagation) Update(_ context.Context, _ *resource.UpdateRequest) (*resource.UpdateResult, error) {
	return nil, fmt.Errorf("update is not supported for %s; a change is a replace (delete then create)", vpnGatewayRoutePropagationType)
}

// Status is not registered: EnableVgwRoutePropagation is synchronous, so Create
// returns success directly with no status polling.
func (p *VPNGatewayRoutePropagation) Status(_ context.Context, _ *resource.StatusRequest) (*resource.StatusResult, error) {
	return nil, fmt.Errorf("status check is not implemented for %s", vpnGatewayRoutePropagationType)
}

// List is not registered: the resource is not discoverable.
func (p *VPNGatewayRoutePropagation) List(_ context.Context, _ *resource.ListRequest) (*resource.ListResult, error) {
	return &resource.ListResult{
		NativeIDs: []string{},
	}, nil
}
