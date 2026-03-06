// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package ec2

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	ec2sdk "github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres/prov"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres/registry"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/config"
)

type networkAclEntryClientInterface interface {
	CreateNetworkAclEntry(ctx context.Context, params *ec2sdk.CreateNetworkAclEntryInput, optFns ...func(*ec2sdk.Options)) (*ec2sdk.CreateNetworkAclEntryOutput, error)
	ReplaceNetworkAclEntry(ctx context.Context, params *ec2sdk.ReplaceNetworkAclEntryInput, optFns ...func(*ec2sdk.Options)) (*ec2sdk.ReplaceNetworkAclEntryOutput, error)
	DeleteNetworkAclEntry(ctx context.Context, params *ec2sdk.DeleteNetworkAclEntryInput, optFns ...func(*ec2sdk.Options)) (*ec2sdk.DeleteNetworkAclEntryOutput, error)
	DescribeNetworkAcls(ctx context.Context, params *ec2sdk.DescribeNetworkAclsInput, optFns ...func(*ec2sdk.Options)) (*ec2sdk.DescribeNetworkAclsOutput, error)
}

type NetworkAclEntry struct {
	cfg *config.Config
}

var _ prov.Provisioner = &NetworkAclEntry{}

func init() {
	registry.Register("AWS::EC2::NetworkAclEntry",
		[]resource.Operation{
			resource.OperationCreate,
			resource.OperationRead,
			resource.OperationUpdate,
			resource.OperationDelete,
		},
		func(cfg *config.Config) prov.Provisioner {
			return &NetworkAclEntry{cfg: cfg}
		})
}

// NativeID format: networkAclId|ruleNumber|egress
func parseNetworkAclEntryNativeID(nativeID string) (aclID string, ruleNumber int32, egress bool, err error) {
	parts := strings.SplitN(nativeID, "|", 3)
	if len(parts) != 3 {
		return "", 0, false, fmt.Errorf("invalid NativeID format: expected networkAclId|ruleNumber|egress, got: %s", nativeID)
	}
	ruleNum, err := strconv.ParseInt(parts[1], 10, 32)
	if err != nil {
		return "", 0, false, fmt.Errorf("invalid rule number in NativeID: %w", err)
	}
	eg, err := strconv.ParseBool(parts[2])
	if err != nil {
		return "", 0, false, fmt.Errorf("invalid egress in NativeID: %w", err)
	}
	return parts[0], int32(ruleNum), eg, nil
}

func (nae *NetworkAclEntry) Create(ctx context.Context, request *resource.CreateRequest) (*resource.CreateResult, error) {
	awsCfg, err := nae.cfg.ToAwsConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading AWS config: %w", err)
	}
	client := ec2sdk.NewFromConfig(awsCfg)
	return nae.createWithClient(ctx, client, request)
}

func (nae *NetworkAclEntry) createWithClient(ctx context.Context, client networkAclEntryClientInterface, request *resource.CreateRequest) (*resource.CreateResult, error) {
	var props map[string]any
	if err := json.Unmarshal(request.Properties, &props); err != nil {
		return nil, fmt.Errorf("parsing properties: %w", err)
	}

	networkAclID, _ := props["NetworkAclId"].(string)
	ruleNumber := int32(props["RuleNumber"].(float64))
	protocol := strconv.Itoa(int(props["Protocol"].(float64)))
	ruleAction := props["RuleAction"].(string)
	egress, _ := props["Egress"].(bool)

	input := &ec2sdk.CreateNetworkAclEntryInput{
		NetworkAclId: aws.String(networkAclID),
		RuleNumber:   aws.Int32(ruleNumber),
		Protocol:     aws.String(protocol),
		RuleAction:   ec2types.RuleAction(ruleAction),
		Egress:       aws.Bool(egress),
	}

	if cidr, ok := props["CidrBlock"].(string); ok {
		input.CidrBlock = aws.String(cidr)
	}
	if cidr, ok := props["Ipv6CidrBlock"].(string); ok {
		input.Ipv6CidrBlock = aws.String(cidr)
	}
	if pr, ok := props["PortRange"].(map[string]any); ok {
		input.PortRange = &ec2types.PortRange{}
		if from, ok := pr["From"].(float64); ok {
			input.PortRange.From = aws.Int32(int32(from))
		}
		if to, ok := pr["To"].(float64); ok {
			input.PortRange.To = aws.Int32(int32(to))
		}
	}
	if icmp, ok := props["Icmp"].(map[string]any); ok {
		input.IcmpTypeCode = &ec2types.IcmpTypeCode{}
		if code, ok := icmp["Code"].(float64); ok {
			input.IcmpTypeCode.Code = aws.Int32(int32(code))
		}
		if typ, ok := icmp["Type"].(float64); ok {
			input.IcmpTypeCode.Type = aws.Int32(int32(typ))
		}
	}

	if _, err := client.CreateNetworkAclEntry(ctx, input); err != nil {
		return nil, fmt.Errorf("creating network ACL entry: %w", err)
	}

	nativeID := fmt.Sprintf("%s|%d|%t", networkAclID, ruleNumber, egress)
	return &resource.CreateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:       resource.OperationCreate,
			OperationStatus: resource.OperationStatusSuccess,
			NativeID:        nativeID,
		},
	}, nil
}

func (nae *NetworkAclEntry) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	awsCfg, err := nae.cfg.ToAwsConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading AWS config: %w", err)
	}
	client := ec2sdk.NewFromConfig(awsCfg)
	return nae.readWithClient(ctx, client, request)
}

func (nae *NetworkAclEntry) readWithClient(ctx context.Context, client networkAclEntryClientInterface, request *resource.ReadRequest) (*resource.ReadResult, error) {
	aclID, ruleNumber, egress, err := parseNetworkAclEntryNativeID(request.NativeID)
	if err != nil {
		return nil, err
	}

	resp, err := client.DescribeNetworkAcls(ctx, &ec2sdk.DescribeNetworkAclsInput{
		NetworkAclIds: []string{aclID},
	})
	if err != nil {
		return &resource.ReadResult{
			ResourceType: "AWS::EC2::NetworkAclEntry",
			ErrorCode:    resource.OperationErrorCodeNotFound,
		}, nil
	}
	if len(resp.NetworkAcls) == 0 {
		return &resource.ReadResult{
			ResourceType: "AWS::EC2::NetworkAclEntry",
			ErrorCode:    resource.OperationErrorCodeNotFound,
		}, nil
	}

	for _, entry := range resp.NetworkAcls[0].Entries {
		if entry.RuleNumber == nil || *entry.RuleNumber != ruleNumber {
			continue
		}
		if entry.Egress == nil || *entry.Egress != egress {
			continue
		}

		props := map[string]any{
			"NetworkAclId": aclID,
			"RuleNumber":   float64(*entry.RuleNumber),
			"Protocol":     *entry.Protocol,
			"RuleAction":   string(entry.RuleAction),
			"Egress":       *entry.Egress,
		}
		if entry.CidrBlock != nil {
			props["CidrBlock"] = *entry.CidrBlock
		}
		if entry.Ipv6CidrBlock != nil {
			props["Ipv6CidrBlock"] = *entry.Ipv6CidrBlock
		}
		if entry.PortRange != nil {
			pr := map[string]any{}
			if entry.PortRange.From != nil {
				pr["From"] = float64(*entry.PortRange.From)
			}
			if entry.PortRange.To != nil {
				pr["To"] = float64(*entry.PortRange.To)
			}
			props["PortRange"] = pr
		}
		if entry.IcmpTypeCode != nil {
			icmp := map[string]any{}
			if entry.IcmpTypeCode.Code != nil {
				icmp["Code"] = float64(*entry.IcmpTypeCode.Code)
			}
			if entry.IcmpTypeCode.Type != nil {
				icmp["Type"] = float64(*entry.IcmpTypeCode.Type)
			}
			props["Icmp"] = icmp
		}

		propBytes, err := json.Marshal(props)
		if err != nil {
			return nil, fmt.Errorf("marshaling properties: %w", err)
		}
		return &resource.ReadResult{
			ResourceType: "AWS::EC2::NetworkAclEntry",
			Properties:   string(propBytes),
		}, nil
	}

	return &resource.ReadResult{
		ResourceType: "AWS::EC2::NetworkAclEntry",
		ErrorCode:    resource.OperationErrorCodeNotFound,
	}, nil
}

func (nae *NetworkAclEntry) Update(ctx context.Context, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	awsCfg, err := nae.cfg.ToAwsConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading AWS config: %w", err)
	}
	client := ec2sdk.NewFromConfig(awsCfg)
	return nae.updateWithClient(ctx, client, request)
}

func (nae *NetworkAclEntry) updateWithClient(ctx context.Context, client networkAclEntryClientInterface, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	aclID, ruleNumber, egress, err := parseNetworkAclEntryNativeID(request.NativeID)
	if err != nil {
		return nil, err
	}

	var desired map[string]any
	if err := json.Unmarshal(request.DesiredProperties, &desired); err != nil {
		return nil, fmt.Errorf("parsing desired properties: %w", err)
	}

	protocol := strconv.Itoa(int(desired["Protocol"].(float64)))
	ruleAction := desired["RuleAction"].(string)

	input := &ec2sdk.ReplaceNetworkAclEntryInput{
		NetworkAclId: aws.String(aclID),
		RuleNumber:   aws.Int32(ruleNumber),
		Egress:       aws.Bool(egress),
		Protocol:     aws.String(protocol),
		RuleAction:   ec2types.RuleAction(ruleAction),
	}

	if cidr, ok := desired["CidrBlock"].(string); ok {
		input.CidrBlock = aws.String(cidr)
	}
	if cidr, ok := desired["Ipv6CidrBlock"].(string); ok {
		input.Ipv6CidrBlock = aws.String(cidr)
	}
	if pr, ok := desired["PortRange"].(map[string]any); ok {
		input.PortRange = &ec2types.PortRange{}
		if from, ok := pr["From"].(float64); ok {
			input.PortRange.From = aws.Int32(int32(from))
		}
		if to, ok := pr["To"].(float64); ok {
			input.PortRange.To = aws.Int32(int32(to))
		}
	}
	if icmp, ok := desired["Icmp"].(map[string]any); ok {
		input.IcmpTypeCode = &ec2types.IcmpTypeCode{}
		if code, ok := icmp["Code"].(float64); ok {
			input.IcmpTypeCode.Code = aws.Int32(int32(code))
		}
		if typ, ok := icmp["Type"].(float64); ok {
			input.IcmpTypeCode.Type = aws.Int32(int32(typ))
		}
	}

	if _, err := client.ReplaceNetworkAclEntry(ctx, input); err != nil {
		return nil, fmt.Errorf("replacing network ACL entry: %w", err)
	}

	return &resource.UpdateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationUpdate,
			OperationStatus:    resource.OperationStatusSuccess,
			NativeID:           request.NativeID,
			ResourceProperties: json.RawMessage(request.DesiredProperties),
		},
	}, nil
}

func (nae *NetworkAclEntry) Delete(ctx context.Context, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	awsCfg, err := nae.cfg.ToAwsConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading AWS config: %w", err)
	}
	client := ec2sdk.NewFromConfig(awsCfg)
	return nae.deleteWithClient(ctx, client, request)
}

func (nae *NetworkAclEntry) deleteWithClient(ctx context.Context, client networkAclEntryClientInterface, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	aclID, ruleNumber, egress, err := parseNetworkAclEntryNativeID(request.NativeID)
	if err != nil {
		return nil, err
	}

	if _, err := client.DeleteNetworkAclEntry(ctx, &ec2sdk.DeleteNetworkAclEntryInput{
		NetworkAclId: aws.String(aclID),
		RuleNumber:   aws.Int32(ruleNumber),
		Egress:       aws.Bool(egress),
	}); err != nil {
		return nil, fmt.Errorf("deleting network ACL entry: %w", err)
	}

	return &resource.DeleteResult{
		ProgressResult: &resource.ProgressResult{
			Operation:       resource.OperationDelete,
			OperationStatus: resource.OperationStatusSuccess,
			NativeID:        request.NativeID,
		},
	}, nil
}

func (nae *NetworkAclEntry) Status(_ context.Context, _ *resource.StatusRequest) (*resource.StatusResult, error) {
	return nil, fmt.Errorf("status check is not implemented for AWS::EC2::NetworkAclEntry")
}

func (nae *NetworkAclEntry) List(_ context.Context, _ *resource.ListRequest) (*resource.ListResult, error) {
	return &resource.ListResult{
		NativeIDs: []string{},
	}, nil
}
