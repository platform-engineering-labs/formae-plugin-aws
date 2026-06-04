#!/bin/bash
# © 2025 Platform Engineering Labs Inc.
# SPDX-License-Identifier: FSL-1.1-ALv2
#
# Clean AWS Environment Hook
# ==========================
# This script cleans up AWS test resources before AND after conformance tests.
# It is idempotent - safe to run multiple times.
#
# Test resources are identified by the "plugin-sdk-test" prefix in their names
# or by FormaeResourceLabel/FormaeStackLabel tags.

set -euo pipefail

REGION="${AWS_REGION:-${AWS_DEFAULT_REGION:-us-east-1}}"
TEST_PREFIX="plugin-sdk-test"
FORMAE_PREFIX="formae-plugin-sdk-test"
SDK_PREFIX="formae-sdk-test"
# Legacy prefix for ecs-service-with-lb pre-2026-05-24, when the fixture
# used short names like `formae-sdk-svc-lb-cluster-*` to fit ALB's 32-char
# limit. Fixture renamed to `formae-sdk-test-*`; this entry is kept so
# legacy account orphans (which were blocking VPC delete via their ALB
# ENIs) still get cleaned up.
LEGACY_LB_PREFIX="formae-sdk-svc-lb"

echo "=== Cleaning AWS test resources in $REGION ==="
echo "Looking for resources with '$TEST_PREFIX' in name or tags..."
echo ""

# ============================================================================
# IAM Resources (global, no region)
# ============================================================================

# 1. Delete IAM instance profiles with test prefix (before roles)
echo "Cleaning IAM test instance profiles..."
aws iam list-instance-profiles --query "InstanceProfiles[?contains(InstanceProfileName, '$FORMAE_PREFIX') || contains(InstanceProfileName, '$SDK_PREFIX') || contains(InstanceProfileName, '$TEST_PREFIX')].InstanceProfileName" --output text 2>/dev/null | tr '\t' '\n' | while read -r profile; do
    if [[ -n "$profile" ]]; then
        echo "  Deleting IAM instance profile: $profile"
        # Remove all roles from instance profile
        aws iam get-instance-profile --instance-profile-name "$profile" --query "InstanceProfile.Roles[].RoleName" --output text 2>/dev/null | tr '\t' '\n' | while read -r role; do
            [[ -n "$role" ]] && aws iam remove-role-from-instance-profile --instance-profile-name "$profile" --role-name "$role" 2>/dev/null || true
        done
        aws iam delete-instance-profile --instance-profile-name "$profile" 2>/dev/null || true
    fi
done

# 2. Delete IAM roles with test prefix
echo "Cleaning IAM test roles..."
aws iam list-roles --query "Roles[?contains(RoleName, '$FORMAE_PREFIX') || contains(RoleName, '$SDK_PREFIX') || contains(RoleName, '$TEST_PREFIX')].RoleName" --output text 2>/dev/null | tr '\t' '\n' | while read -r role; do
    if [[ -n "$role" ]]; then
        echo "  Deleting IAM role: $role"
        # Detach managed policies
        aws iam list-attached-role-policies --role-name "$role" --query "AttachedPolicies[].PolicyArn" --output text 2>/dev/null | tr '\t' '\n' | while read -r policy; do
            [[ -n "$policy" ]] && aws iam detach-role-policy --role-name "$role" --policy-arn "$policy" 2>/dev/null || true
        done
        # Delete inline policies
        aws iam list-role-policies --role-name "$role" --query "PolicyNames[]" --output text 2>/dev/null | tr '\t' '\n' | while read -r policy; do
            [[ -n "$policy" ]] && aws iam delete-role-policy --role-name "$role" --policy-name "$policy" 2>/dev/null || true
        done
        # Remove from instance profiles (safety net)
        aws iam list-instance-profiles-for-role --role-name "$role" --query "InstanceProfiles[].InstanceProfileName" --output text 2>/dev/null | tr '\t' '\n' | while read -r profile; do
            [[ -n "$profile" ]] && {
                aws iam remove-role-from-instance-profile --instance-profile-name "$profile" --role-name "$role" 2>/dev/null || true
                aws iam delete-instance-profile --instance-profile-name "$profile" 2>/dev/null || true
            }
        done
        aws iam delete-role --role-name "$role" 2>/dev/null || true
    fi
done

# 3. Delete IAM users with test prefix
echo "Cleaning IAM test users..."
aws iam list-users --query "Users[?contains(UserName, '$FORMAE_PREFIX') || contains(UserName, '$SDK_PREFIX') || contains(UserName, '$TEST_PREFIX')].UserName" --output text 2>/dev/null | tr '\t' '\n' | while read -r user; do
    if [[ -n "$user" ]]; then
        echo "  Deleting IAM user: $user"
        # Delete login profile
        aws iam delete-login-profile --user-name "$user" 2>/dev/null || true
        # Delete access keys
        aws iam list-access-keys --user-name "$user" --query "AccessKeyMetadata[].AccessKeyId" --output text 2>/dev/null | tr '\t' '\n' | while read -r key; do
            [[ -n "$key" ]] && aws iam delete-access-key --user-name "$user" --access-key-id "$key" 2>/dev/null || true
        done
        # Detach managed policies
        aws iam list-attached-user-policies --user-name "$user" --query "AttachedPolicies[].PolicyArn" --output text 2>/dev/null | tr '\t' '\n' | while read -r policy; do
            [[ -n "$policy" ]] && aws iam detach-user-policy --user-name "$user" --policy-arn "$policy" 2>/dev/null || true
        done
        # Delete inline policies
        aws iam list-user-policies --user-name "$user" --query "PolicyNames[]" --output text 2>/dev/null | tr '\t' '\n' | while read -r policy; do
            [[ -n "$policy" ]] && aws iam delete-user-policy --user-name "$user" --policy-name "$policy" 2>/dev/null || true
        done
        # Remove from groups
        aws iam list-groups-for-user --user-name "$user" --query "Groups[].GroupName" --output text 2>/dev/null | tr '\t' '\n' | while read -r group; do
            [[ -n "$group" ]] && aws iam remove-user-from-group --user-name "$user" --group-name "$group" 2>/dev/null || true
        done
        aws iam delete-user --user-name "$user" 2>/dev/null || true
    fi
done

# 4. Delete IAM groups with test prefix
echo "Cleaning IAM test groups..."
aws iam list-groups --query "Groups[?contains(GroupName, '$FORMAE_PREFIX') || contains(GroupName, '$SDK_PREFIX') || contains(GroupName, '$TEST_PREFIX')].GroupName" --output text 2>/dev/null | tr '\t' '\n' | while read -r group; do
    if [[ -n "$group" ]]; then
        echo "  Deleting IAM group: $group"
        # Remove users from group
        aws iam get-group --group-name "$group" --query "Users[].UserName" --output text 2>/dev/null | tr '\t' '\n' | while read -r user; do
            [[ -n "$user" ]] && aws iam remove-user-from-group --user-name "$user" --group-name "$group" 2>/dev/null || true
        done
        # Detach managed policies
        aws iam list-attached-group-policies --group-name "$group" --query "AttachedPolicies[].PolicyArn" --output text 2>/dev/null | tr '\t' '\n' | while read -r policy; do
            [[ -n "$policy" ]] && aws iam detach-group-policy --group-name "$group" --policy-arn "$policy" 2>/dev/null || true
        done
        # Delete inline policies
        aws iam list-group-policies --group-name "$group" --query "PolicyNames[]" --output text 2>/dev/null | tr '\t' '\n' | while read -r policy; do
            [[ -n "$policy" ]] && aws iam delete-group-policy --group-name "$group" --policy-name "$policy" 2>/dev/null || true
        done
        aws iam delete-group --group-name "$group" 2>/dev/null || true
    fi
done

# 5. Delete IAM managed policies with test prefix
echo "Cleaning IAM test managed policies..."
aws iam list-policies --scope Local --query "Policies[?contains(PolicyName, '$FORMAE_PREFIX') || contains(PolicyName, '$TEST_PREFIX') || contains(PolicyName, '$SDK_PREFIX')].Arn" --output text 2>/dev/null | tr '\t' '\n' | while read -r arn; do
    if [[ -n "$arn" ]]; then
        echo "  Deleting IAM managed policy: $arn"
        # Detach from all entities
        aws iam list-entities-for-policy --policy-arn "$arn" --query "PolicyGroups[].GroupName" --output text 2>/dev/null | tr '\t' '\n' | while read -r group; do
            [[ -n "$group" ]] && aws iam detach-group-policy --group-name "$group" --policy-arn "$arn" 2>/dev/null || true
        done
        aws iam list-entities-for-policy --policy-arn "$arn" --query "PolicyRoles[].RoleName" --output text 2>/dev/null | tr '\t' '\n' | while read -r role; do
            [[ -n "$role" ]] && aws iam detach-role-policy --role-name "$role" --policy-arn "$arn" 2>/dev/null || true
        done
        aws iam list-entities-for-policy --policy-arn "$arn" --query "PolicyUsers[].UserName" --output text 2>/dev/null | tr '\t' '\n' | while read -r user; do
            [[ -n "$user" ]] && aws iam detach-user-policy --user-name "$user" --policy-arn "$arn" 2>/dev/null || true
        done
        # Delete non-default policy versions
        aws iam list-policy-versions --policy-arn "$arn" --query "Versions[?!IsDefaultVersion].VersionId" --output text 2>/dev/null | tr '\t' '\n' | while read -r version; do
            [[ -n "$version" ]] && aws iam delete-policy-version --policy-arn "$arn" --version-id "$version" 2>/dev/null || true
        done
        aws iam delete-policy --policy-arn "$arn" 2>/dev/null || true
    fi
done

# 6. Delete IAM OIDC providers with test prefix
echo "Cleaning IAM test OIDC providers..."
aws iam list-open-id-connect-providers --query "OpenIDConnectProviderList[].Arn" --output text 2>/dev/null | tr '\t' '\n' | while read -r arn; do
    if [[ -n "$arn" ]]; then
        url=$(aws iam get-open-id-connect-provider --open-id-connect-provider-arn "$arn" --query "Url" --output text 2>/dev/null || true)
        if [[ "$url" == *"sdk-test"* ]]; then
            echo "  Deleting IAM OIDC provider: $arn"
            aws iam delete-open-id-connect-provider --open-id-connect-provider-arn "$arn" 2>/dev/null || true
        fi
    fi
done

# 7. Delete IAM SAML providers with test prefix
echo "Cleaning IAM test SAML providers..."
aws iam list-saml-providers --query "SAMLProviderList[].Arn" --output text 2>/dev/null | tr '\t' '\n' | while read -r arn; do
    if [[ -n "$arn" && "$arn" == *"$FORMAE_PREFIX"* ]]; then
        echo "  Deleting IAM SAML provider: $arn"
        aws iam delete-saml-provider --saml-provider-arn "$arn" 2>/dev/null || true
    fi
done

# ============================================================================
# S3 Resources (global) - delete access points before buckets
# ============================================================================

# 8a. Delete S3 access points with test prefix
echo "Cleaning S3 test access points..."
aws s3control list-access-points --account-id "$(aws sts get-caller-identity --query Account --output text 2>/dev/null)" --region "$REGION" \
    --query "AccessPointList[?contains(Name, '$FORMAE_PREFIX') || contains(Name, '$SDK_PREFIX') || contains(Name, '$TEST_PREFIX')].{Name:Name}" --output text 2>/dev/null | tr '\t' '\n' | while read -r ap; do
    if [[ -n "$ap" ]]; then
        echo "  Deleting S3 access point: $ap"
        ACCOUNT_ID=$(aws sts get-caller-identity --query Account --output text 2>/dev/null)
        aws s3control delete-access-point --account-id "$ACCOUNT_ID" --name "$ap" --region "$REGION" 2>/dev/null || true
    fi
done

# 8b. Delete S3 buckets with test prefix
echo "Cleaning S3 test buckets..."
aws s3api list-buckets --query "Buckets[?contains(Name, '$FORMAE_PREFIX') || contains(Name, '$SDK_PREFIX') || contains(Name, '$TEST_PREFIX')].Name" --output text 2>/dev/null | tr '\t' '\n' | while read -r bucket; do
    if [[ -n "$bucket" ]]; then
        echo "  Deleting S3 bucket: $bucket"
        # Delete bucket policy first (if any)
        aws s3api delete-bucket-policy --bucket "$bucket" --region "$REGION" 2>/dev/null || true
        aws s3 rm "s3://$bucket" --recursive --region "$REGION" 2>/dev/null || true
        aws s3api delete-bucket --bucket "$bucket" --region "$REGION" 2>/dev/null || true
    fi
done

# 8c. Delete S3 Storage Lens Groups with test prefix
echo "Cleaning S3 test Storage Lens Groups..."
aws s3control list-storage-lens-groups --account-id "$(aws sts get-caller-identity --query Account --output text 2>/dev/null)" --region "$REGION" 2>/dev/null | \
    jq -r ".StorageLensGroupList[]? | select(.Name | test(\"$FORMAE_PREFIX|$SDK_PREFIX|$TEST_PREFIX\")) | .Name" 2>/dev/null | while read -r slg; do
    if [[ -n "$slg" ]]; then
        echo "  Deleting S3 Storage Lens Group: $slg"
        ACCOUNT_ID=$(aws sts get-caller-identity --query Account --output text 2>/dev/null)
        aws s3control delete-storage-lens-group --account-id "$ACCOUNT_ID" --name "$slg" --region "$REGION" 2>/dev/null || true
    fi
done

# ============================================================================
# Route53 Resources (global)
# ============================================================================

# 9. Delete Route53 hosted zones with test prefix (including sdk-test zones)
echo "Cleaning Route53 test hosted zones..."
aws route53 list-hosted-zones --query "HostedZones[].{Id:Id, Name:Name}" --output json 2>/dev/null | \
    jq -r '.[] | select(.Name | test("sdk-.*test-formae\\.io|plugin-sdk-test")) | .Id' 2>/dev/null | while read -r zone; do
    if [[ -n "$zone" ]]; then
        echo "  Deleting Route53 zone: $zone"
        # Delete all record sets except NS and SOA
        aws route53 list-resource-record-sets --hosted-zone-id "$zone" \
            --query "ResourceRecordSets[?Type!='NS' && Type!='SOA']" --output json 2>/dev/null | \
            jq -c '.[]' 2>/dev/null | while read -r record; do
            aws route53 change-resource-record-sets --hosted-zone-id "$zone" \
                --change-batch "{\"Changes\":[{\"Action\":\"DELETE\",\"ResourceRecordSet\":$record}]}" 2>/dev/null || true
        done
        aws route53 delete-hosted-zone --id "$zone" 2>/dev/null || true
    fi
done

# 10. Delete Route53 health checks with test name tag
echo "Cleaning Route53 test health checks..."
aws route53 list-health-checks --query "HealthChecks[].Id" --output text 2>/dev/null | tr '\t' '\n' | while read -r hc_id; do
    if [[ -n "$hc_id" ]]; then
        tags=$(aws route53 list-tags-for-resource --resource-type healthcheck --resource-id "$hc_id" \
            --query "ResourceTagSet.Tags[?Key=='Name'].Value" --output text 2>/dev/null || true)
        if [[ "$tags" == *"$FORMAE_PREFIX"* || "$tags" == *"$SDK_PREFIX"* || "$tags" == *"$TEST_PREFIX"* ]]; then
            echo "  Deleting Route53 health check: $hc_id"
            aws route53 delete-health-check --health-check-id "$hc_id" 2>/dev/null || true
        fi
    fi
done

# 11. Delete Route53 CIDR collections with test prefix
echo "Cleaning Route53 test CIDR collections..."
aws route53 list-cidr-collections --query "CidrCollections[?contains(Name, 'sdk-test')].{Id:Id, Name:Name}" --output json 2>/dev/null | \
    jq -r '.[].Id' 2>/dev/null | while read -r coll_id; do
    if [[ -n "$coll_id" ]]; then
        echo "  Deleting Route53 CIDR collection: $coll_id"
        # Delete all locations first
        aws route53 list-cidr-locations --collection-id "$coll_id" --query "CidrLocations[].LocationName" --output text 2>/dev/null | tr '\t' '\n' | while read -r loc; do
            if [[ -n "$loc" ]]; then
                cidrs=$(aws route53 list-cidr-blocks --collection-id "$coll_id" --location-name "$loc" --query "CidrBlocks[].CidrBlock" --output json 2>/dev/null || echo "[]")
                cidr_list=$(echo "$cidrs" | jq -c '.' 2>/dev/null)
                aws route53 change-cidr-collection --id "$coll_id" --changes "[{\"Action\":\"DELETE_IF_EXISTS\",\"LocationName\":\"$loc\",\"CidrList\":$cidr_list}]" 2>/dev/null || true
            fi
        done
        aws route53 delete-cidr-collection --id "$coll_id" 2>/dev/null || true
    fi
done

# ============================================================================
# KMS Resources (regional)
# ============================================================================

# 12. Schedule KMS keys with test alias for deletion
echo "Scheduling KMS test keys for deletion..."
aws kms list-aliases --query "Aliases[?contains(AliasName, '$TEST_PREFIX')].TargetKeyId" --output text 2>/dev/null | tr '\t' '\n' | while read -r key; do
    if [[ -n "$key" ]]; then
        echo "  Scheduling KMS key for deletion: $key"
        aws kms schedule-key-deletion --key-id "$key" --pending-window-in-days 7 --region "$REGION" 2>/dev/null || true
    fi
done

# ============================================================================
# EC2 Resources (regional) - delete dependents before parents
# ============================================================================

# 13. Delete EC2 key pairs with test prefix
echo "Cleaning EC2 test key pairs..."
aws ec2 describe-key-pairs --region "$REGION" \
    --filters "Name=key-name,Values=*$FORMAE_PREFIX*" \
    --query "KeyPairs[].KeyPairId" --output text 2>/dev/null | tr '\t' '\n' | while read -r kp_id; do
    if [[ -n "$kp_id" ]]; then
        echo "  Deleting EC2 key pair: $kp_id"
        aws ec2 delete-key-pair --key-pair-id "$kp_id" --region "$REGION" 2>/dev/null || true
    fi
done

# 14. Release EC2 Elastic IPs with test prefix (by Name tag)
echo "Cleaning EC2 test Elastic IPs..."
aws ec2 describe-addresses --region "$REGION" \
    --filters "Name=tag:Name,Values=*$FORMAE_PREFIX*" \
    --query "Addresses[].AllocationId" --output text 2>/dev/null | tr '\t' '\n' | while read -r alloc_id; do
    if [[ -n "$alloc_id" ]]; then
        echo "  Releasing EC2 EIP: $alloc_id"
        # Disassociate if attached
        assoc_id=$(aws ec2 describe-addresses --region "$REGION" --allocation-ids "$alloc_id" \
            --query "Addresses[0].AssociationId" --output text 2>/dev/null || true)
        [[ -n "$assoc_id" && "$assoc_id" != "None" ]] && aws ec2 disassociate-address --association-id "$assoc_id" --region "$REGION" 2>/dev/null || true
        aws ec2 release-address --allocation-id "$alloc_id" --region "$REGION" 2>/dev/null || true
    fi
done

# 15. Delete EC2 launch templates with test prefix
echo "Cleaning EC2 test launch templates..."
aws ec2 describe-launch-templates --region "$REGION" \
    --filters "Name=launch-template-name,Values=*$FORMAE_PREFIX*" \
    --query "LaunchTemplates[].LaunchTemplateId" --output text 2>/dev/null | tr '\t' '\n' | while read -r lt_id; do
    if [[ -n "$lt_id" ]]; then
        echo "  Deleting EC2 launch template: $lt_id"
        aws ec2 delete-launch-template --launch-template-id "$lt_id" --region "$REGION" 2>/dev/null || true
    fi
done

# 16. Delete EC2 placement groups with test prefix (by Name tag)
echo "Cleaning EC2 test placement groups..."
aws ec2 describe-placement-groups --region "$REGION" \
    --filters "Name=tag:Name,Values=*$FORMAE_PREFIX*" \
    --query "PlacementGroups[].GroupName" --output text 2>/dev/null | tr '\t' '\n' | while read -r pg; do
    if [[ -n "$pg" ]]; then
        echo "  Deleting EC2 placement group: $pg"
        aws ec2 delete-placement-group --group-name "$pg" --region "$REGION" 2>/dev/null || true
    fi
done

# 17. Delete EC2 DHCP options with test prefix (by Name tag)
echo "Cleaning EC2 test DHCP options..."
aws ec2 describe-dhcp-options --region "$REGION" \
    --filters "Name=tag:Name,Values=*$FORMAE_PREFIX*" \
    --query "DhcpOptions[].DhcpOptionsId" --output text 2>/dev/null | tr '\t' '\n' | while read -r dhcp_id; do
    if [[ -n "$dhcp_id" ]]; then
        echo "  Deleting EC2 DHCP options: $dhcp_id"
        aws ec2 delete-dhcp-options --dhcp-options-id "$dhcp_id" --region "$REGION" 2>/dev/null || true
    fi
done

# 18. Detach and delete EC2 internet gateways with test prefix (by Name tag)
echo "Cleaning EC2 test internet gateways..."
aws ec2 describe-internet-gateways --region "$REGION" \
    --filters "Name=tag:Name,Values=*$FORMAE_PREFIX*" \
    --query "InternetGateways[].{Id:InternetGatewayId, Attachments:Attachments}" --output json 2>/dev/null | \
    jq -c '.[]' 2>/dev/null | while read -r igw_json; do
    igw_id=$(echo "$igw_json" | jq -r '.Id')
    if [[ -n "$igw_id" ]]; then
        echo "  Deleting EC2 internet gateway: $igw_id"
        # Detach from all VPCs
        echo "$igw_json" | jq -r '.Attachments[]?.VpcId // empty' 2>/dev/null | while read -r vpc_id; do
            [[ -n "$vpc_id" ]] && aws ec2 detach-internet-gateway --internet-gateway-id "$igw_id" --vpc-id "$vpc_id" --region "$REGION" 2>/dev/null || true
        done
        aws ec2 delete-internet-gateway --internet-gateway-id "$igw_id" --region "$REGION" 2>/dev/null || true
    fi
done

# 19. Detach and delete EC2 VPN gateways with test prefix (by Name tag)
echo "Cleaning EC2 test VPN gateways..."
aws ec2 describe-vpn-gateways --region "$REGION" \
    --filters "Name=tag:Name,Values=*$FORMAE_PREFIX*" \
    --query "VpnGateways[?State!='deleted'].{Id:VpnGatewayId, Attachments:VpcAttachments}" --output json 2>/dev/null | \
    jq -c '.[]' 2>/dev/null | while read -r vgw_json; do
    vgw_id=$(echo "$vgw_json" | jq -r '.Id')
    if [[ -n "$vgw_id" ]]; then
        echo "  Deleting EC2 VPN gateway: $vgw_id"
        # Detach from all VPCs
        echo "$vgw_json" | jq -r '.Attachments[]? | select(.State != "detached") | .VpcId // empty' 2>/dev/null | while read -r vpc_id; do
            [[ -n "$vpc_id" ]] && aws ec2 detach-vpn-gateway --vpn-gateway-id "$vgw_id" --vpc-id "$vpc_id" --region "$REGION" 2>/dev/null || true
        done
        aws ec2 delete-vpn-gateway --vpn-gateway-id "$vgw_id" --region "$REGION" 2>/dev/null || true
    fi
done

# 20. Delete EC2 customer gateways with test prefix (by Name tag)
echo "Cleaning EC2 test customer gateways..."
aws ec2 describe-customer-gateways --region "$REGION" \
    --filters "Name=tag:Name,Values=*$FORMAE_PREFIX*" \
    --query "CustomerGateways[?State!='deleted'].CustomerGatewayId" --output text 2>/dev/null | tr '\t' '\n' | while read -r cgw_id; do
    if [[ -n "$cgw_id" ]]; then
        echo "  Deleting EC2 customer gateway: $cgw_id"
        aws ec2 delete-customer-gateway --customer-gateway-id "$cgw_id" --region "$REGION" 2>/dev/null || true
    fi
done

# 21a. Delete EC2 transit gateway route tables with test prefix (before TGWs)
echo "Cleaning EC2 test transit gateway route tables..."
aws ec2 describe-transit-gateways --region "$REGION" \
    --filters "Name=tag:Name,Values=*$FORMAE_PREFIX*" "Name=state,Values=available,pending" \
    --query "TransitGateways[].TransitGatewayId" --output text 2>/dev/null | tr '\t' '\n' | while read -r tgw_id; do
    if [[ -n "$tgw_id" ]]; then
        aws ec2 describe-transit-gateway-route-tables --region "$REGION" \
            --filters "Name=transit-gateway-id,Values=$tgw_id" \
            --query "TransitGatewayRouteTables[?!DefaultAssociationRouteTable && !DefaultPropagationRouteTable].TransitGatewayRouteTableId" --output text 2>/dev/null | tr '\t' '\n' | while read -r rt_id; do
            if [[ -n "$rt_id" ]]; then
                echo "  Deleting EC2 transit gateway route table: $rt_id"
                aws ec2 delete-transit-gateway-route-table --transit-gateway-route-table-id "$rt_id" --region "$REGION" 2>/dev/null || true
            fi
        done
    fi
done

# 21b. Delete EC2 transit gateways with test prefix (by Name tag)
echo "Cleaning EC2 test transit gateways..."
aws ec2 describe-transit-gateways --region "$REGION" \
    --filters "Name=tag:Name,Values=*$FORMAE_PREFIX*" "Name=state,Values=available,pending" \
    --query "TransitGateways[].TransitGatewayId" --output text 2>/dev/null | tr '\t' '\n' | while read -r tgw_id; do
    if [[ -n "$tgw_id" ]]; then
        echo "  Deleting EC2 transit gateway: $tgw_id"
        aws ec2 delete-transit-gateway --transit-gateway-id "$tgw_id" --region "$REGION" 2>/dev/null || true
    fi
done

# 22. Delete EC2 IPAMs with test prefix (by Name tag)
echo "Cleaning EC2 test IPAMs..."
aws ec2 describe-ipams --region "$REGION" \
    --filters "Name=tag:Name,Values=*$FORMAE_PREFIX*" \
    --query "Ipams[].IpamId" --output text 2>/dev/null | tr '\t' '\n' | while read -r ipam_id; do
    if [[ -n "$ipam_id" ]]; then
        echo "  Deleting EC2 IPAM: $ipam_id"
        # Delete non-default IPAM scopes first
        aws ec2 describe-ipam-scopes --region "$REGION" \
            --filters "Name=ipam-id,Values=$ipam_id" \
            --query "IpamScopes[?!IsDefault].IpamScopeId" --output text 2>/dev/null | tr '\t' '\n' | while read -r scope_id; do
            [[ -n "$scope_id" ]] && aws ec2 delete-ipam-scope --ipam-scope-id "$scope_id" --region "$REGION" 2>/dev/null || true
        done
        aws ec2 delete-ipam --ipam-id "$ipam_id" --region "$REGION" --cascade 2>/dev/null || true
    fi
done

# --- EKS clusters (very slow to delete, start early, before VPC dependents)
echo "Cleaning EKS test clusters..."
aws eks list-clusters --region "$REGION" --query "clusters[]" --output text 2>/dev/null | tr '\t' '\n' | while read -r cluster; do
    if [[ -n "$cluster" && ("$cluster" == *"$FORMAE_PREFIX"* || "$cluster" == *"$SDK_PREFIX"* || "$cluster" == *"$TEST_PREFIX"*) ]]; then
        echo "  Deleting EKS cluster: $cluster"
        # Delete nodegroups first
        aws eks list-nodegroups --cluster-name "$cluster" --region "$REGION" --query "nodegroups[]" --output text 2>/dev/null | tr '\t' '\n' | while read -r ng; do
            [[ -n "$ng" ]] && aws eks delete-nodegroup --cluster-name "$cluster" --nodegroup-name "$ng" --region "$REGION" 2>/dev/null || true
        done
        aws eks delete-cluster --name "$cluster" --region "$REGION" 2>/dev/null || true
    fi
done

# --- RDS DB instances (slow, before DB subnet groups and VPCs)
echo "Cleaning RDS test DB instances..."
aws rds describe-db-instances --region "$REGION" \
    --query "DBInstances[?contains(DBInstanceIdentifier, '$SDK_PREFIX') || contains(DBInstanceIdentifier, '$FORMAE_PREFIX') || contains(DBInstanceIdentifier, '$TEST_PREFIX')].DBInstanceIdentifier" --output text 2>/dev/null | tr '\t' '\n' | while read -r db; do
    if [[ -n "$db" ]]; then
        echo "  Deleting RDS DB instance: $db"
        aws rds delete-db-instance --db-instance-identifier "$db" --skip-final-snapshot --delete-automated-backups --region "$REGION" 2>/dev/null || true
    fi
done

# --- RDS DB subnet groups (after DB instances, before VPCs)
echo "Cleaning RDS test DB subnet groups..."
aws rds describe-db-subnet-groups --region "$REGION" \
    --query "DBSubnetGroups[?contains(DBSubnetGroupName, '$SDK_PREFIX') || contains(DBSubnetGroupName, '$FORMAE_PREFIX') || contains(DBSubnetGroupName, '$TEST_PREFIX')].DBSubnetGroupName" --output text 2>/dev/null | tr '\t' '\n' | while read -r sg; do
    if [[ -n "$sg" ]]; then
        echo "  Deleting RDS DB subnet group: $sg"
        aws rds delete-db-subnet-group --db-subnet-group-name "$sg" --region "$REGION" 2>/dev/null || true
    fi
done

# --- ELBv2 load balancers (before target groups, subnets, VPCs)
# Legacy LEGACY_LB_PREFIXES: pre-2026-05-24 ecs-service-with-lb fixture used
# `formae-sdk-alb-*` / `formae-sdk-svc-lb-*` names that don't match the test
# prefixes above (because ALB names are capped at 32 chars). Fixture renamed
# to `formae-sdk-test-*` going forward; the legacy patterns are kept here
# so existing account orphans get cleaned up.
echo "Cleaning ELBv2 test load balancers..."
aws elbv2 describe-load-balancers --region "$REGION" \
    --query "LoadBalancers[?contains(LoadBalancerName, '$FORMAE_PREFIX') || contains(LoadBalancerName, '$SDK_PREFIX') || contains(LoadBalancerName, '$TEST_PREFIX') || contains(LoadBalancerName, 'formae-sdk-alb')].LoadBalancerArn" --output text 2>/dev/null | tr '\t' '\n' | while read -r lb_arn; do
    if [[ -n "$lb_arn" ]]; then
        echo "  Deleting ELBv2 load balancer: $lb_arn"
        # Delete listeners first
        aws elbv2 describe-listeners --load-balancer-arn "$lb_arn" --region "$REGION" \
            --query "Listeners[].ListenerArn" --output text 2>/dev/null | tr '\t' '\n' | while read -r listener; do
            [[ -n "$listener" ]] && aws elbv2 delete-listener --listener-arn "$listener" --region "$REGION" 2>/dev/null || true
        done
        aws elbv2 delete-load-balancer --load-balancer-arn "$lb_arn" --region "$REGION" 2>/dev/null || true
    fi
done

# --- ELBv2 target groups (after load balancers)
echo "Cleaning ELBv2 test target groups..."
aws elbv2 describe-target-groups --region "$REGION" \
    --query "TargetGroups[?contains(TargetGroupName, '$FORMAE_PREFIX') || contains(TargetGroupName, '$SDK_PREFIX') || contains(TargetGroupName, '$TEST_PREFIX') || contains(TargetGroupName, 'formae-sdk-tg')].TargetGroupArn" --output text 2>/dev/null | tr '\t' '\n' | while read -r tg_arn; do
    if [[ -n "$tg_arn" ]]; then
        echo "  Deleting ELBv2 target group: $tg_arn"
        aws elbv2 delete-target-group --target-group-arn "$tg_arn" --region "$REGION" 2>/dev/null || true
    fi
done

# --- EC2 NAT gateways (before subnets/VPCs, slow to delete)
echo "Cleaning EC2 test NAT gateways..."
aws ec2 describe-nat-gateways --region "$REGION" \
    --filter "Name=tag:Name,Values=*$FORMAE_PREFIX*" "Name=state,Values=available,pending,failed" \
    --query "NatGateways[].NatGatewayId" --output text 2>/dev/null | tr '\t' '\n' | while read -r nat_id; do
    if [[ -n "$nat_id" ]]; then
        echo "  Deleting EC2 NAT gateway: $nat_id"
        aws ec2 delete-nat-gateway --nat-gateway-id "$nat_id" --region "$REGION" 2>/dev/null || true
    fi
done

# --- EC2 VPC endpoints (before VPCs)
echo "Cleaning EC2 test VPC endpoints..."
aws ec2 describe-vpc-endpoints --region "$REGION" \
    --filters "Name=tag:Name,Values=*$FORMAE_PREFIX*" \
    --query "VpcEndpoints[?State!='deleted'].VpcEndpointId" --output text 2>/dev/null | tr '\t' '\n' | while read -r ep_id; do
    if [[ -n "$ep_id" ]]; then
        echo "  Deleting EC2 VPC endpoint: $ep_id"
        aws ec2 delete-vpc-endpoints --vpc-endpoint-ids "$ep_id" --region "$REGION" 2>/dev/null || true
    fi
done

# --- EC2 egress-only internet gateways (before VPCs)
echo "Cleaning EC2 test egress-only internet gateways..."
aws ec2 describe-egress-only-internet-gateways --region "$REGION" \
    --query "EgressOnlyInternetGateways[].{Id:EgressOnlyInternetGatewayId, Tags:Tags}" --output json 2>/dev/null | \
    jq -r '.[] | select(.Tags[]? | select(.Key == "Name" and (.Value | test("'"$FORMAE_PREFIX"'")))) | .Id' 2>/dev/null | while read -r eigw_id; do
    if [[ -n "$eigw_id" ]]; then
        echo "  Deleting EC2 egress-only internet gateway: $eigw_id"
        aws ec2 delete-egress-only-internet-gateway --egress-only-internet-gateway-id "$eigw_id" --region "$REGION" 2>/dev/null || true
    fi
done

# --- ECS task sets (before services; task sets block service deletion)
# AWS CLI has no `ecs list-task-sets`; task set ARNs come from
# `describe-services` under `.services[].taskSets[].taskSetArn`.
echo "Cleaning ECS test task sets..."
aws ecs list-clusters --region "$REGION" --query "clusterArns[]" --output text 2>/dev/null | tr '\t' '\n' | while read -r cluster_arn; do
    if [[ -n "$cluster_arn" && ("$cluster_arn" == *"$FORMAE_PREFIX"* || "$cluster_arn" == *"$SDK_PREFIX"* || "$cluster_arn" == *"$LEGACY_LB_PREFIX"*) ]]; then
        aws ecs list-services --cluster "$cluster_arn" --region "$REGION" --query "serviceArns[]" --output text 2>/dev/null | tr '\t' '\n' | while read -r svc_arn; do
            if [[ -n "$svc_arn" ]]; then
                aws ecs describe-services --cluster "$cluster_arn" --services "$svc_arn" --region "$REGION" \
                    --query "services[].taskSets[].taskSetArn" --output text 2>/dev/null | tr '\t' '\n' | while read -r ts_arn; do
                    if [[ -n "$ts_arn" ]]; then
                        echo "  Deleting ECS task set: $ts_arn"
                        aws ecs delete-task-set --cluster "$cluster_arn" --service "$svc_arn" --task-set "$ts_arn" --force --region "$REGION" 2>/dev/null || true
                    fi
                done
            fi
        done
    fi
done

# --- ECS services (before clusters)
# `delete-service --force` returns immediately but Fargate tasks take
# 30-60s to drain and their ENIs another ~60s to deregister. Without
# waiting, subnet/VPC cleanup downstream races with task draining and
# leaves orphan ENIs that block VPC delete on subsequent runs.
echo "Cleaning ECS test services..."
ECS_TEST_CLUSTERS=()
while IFS= read -r cluster_arn; do
    [[ -n "$cluster_arn" && ("$cluster_arn" == *"$FORMAE_PREFIX"* || "$cluster_arn" == *"$SDK_PREFIX"* || "$cluster_arn" == *"$LEGACY_LB_PREFIX"*) ]] && ECS_TEST_CLUSTERS+=("$cluster_arn")
done < <(aws ecs list-clusters --region "$REGION" --query "clusterArns[]" --output text 2>/dev/null | tr '\t' '\n')

for cluster_arn in "${ECS_TEST_CLUSTERS[@]}"; do
    while IFS= read -r svc_arn; do
        if [[ -n "$svc_arn" ]]; then
            echo "  Stopping ECS service: $svc_arn"
            aws ecs update-service --cluster "$cluster_arn" --service "$svc_arn" --desired-count 0 --region "$REGION" 2>/dev/null || true
            aws ecs delete-service --cluster "$cluster_arn" --service "$svc_arn" --force --region "$REGION" 2>/dev/null || true
        fi
    done < <(aws ecs list-services --cluster "$cluster_arn" --region "$REGION" --query "serviceArns[]" --output text 2>/dev/null | tr '\t' '\n')
done

# Poll until ACTIVE services drain from test clusters. Budget 120s total
# (Fargate task termination is usually <60s; ALB target deregistration
# adds another ~30s on services with LB attachments).
if [ ${#ECS_TEST_CLUSTERS[@]} -gt 0 ]; then
    echo "  Waiting for ECS services to drain (max 120s)..."
    waited=0
    while [ $waited -lt 120 ]; do
        active=0
        for cluster_arn in "${ECS_TEST_CLUSTERS[@]}"; do
            n=$(aws ecs list-services --cluster "$cluster_arn" --region "$REGION" --query "length(serviceArns)" --output text 2>/dev/null || echo 0)
            [[ -n "$n" && "$n" != "None" ]] && active=$((active + n))
        done
        [ "$active" = "0" ] && break
        sleep 10
        waited=$((waited + 10))
    done
    echo "  ECS services drained after ${waited}s (residual count: ${active:-?})"
fi

# --- EC2 network interfaces (before subnets/security groups)
echo "Cleaning EC2 test network interfaces..."
aws ec2 describe-network-interfaces --region "$REGION" \
    --filters "Name=tag:Name,Values=*$FORMAE_PREFIX*" \
    --query "NetworkInterfaces[].{Id:NetworkInterfaceId, Attachment:Attachment}" --output json 2>/dev/null | \
    jq -c '.[]' 2>/dev/null | while read -r eni_json; do
    eni_id=$(echo "$eni_json" | jq -r '.Id')
    if [[ -n "$eni_id" ]]; then
        echo "  Deleting EC2 network interface: $eni_id"
        # Detach first if attached
        attach_id=$(echo "$eni_json" | jq -r '.Attachment.AttachmentId // empty')
        [[ -n "$attach_id" ]] && aws ec2 detach-network-interface --attachment-id "$attach_id" --force --region "$REGION" 2>/dev/null || true
        sleep 2
        aws ec2 delete-network-interface --network-interface-id "$eni_id" --region "$REGION" 2>/dev/null || true
    fi
done

# --- EC2 subnets (before route tables, VPCs)
echo "Cleaning EC2 test subnets..."
aws ec2 describe-subnets --region "$REGION" \
    --filters "Name=tag:Name,Values=*$FORMAE_PREFIX*" \
    --query "Subnets[].SubnetId" --output text 2>/dev/null | tr '\t' '\n' | while read -r subnet_id; do
    if [[ -n "$subnet_id" ]]; then
        echo "  Deleting EC2 subnet: $subnet_id"
        aws ec2 delete-subnet --subnet-id "$subnet_id" --region "$REGION" 2>/dev/null || true
    fi
done

# --- EC2 route tables (non-main, before VPCs)
echo "Cleaning EC2 test route tables..."
aws ec2 describe-route-tables --region "$REGION" \
    --filters "Name=tag:Name,Values=*$FORMAE_PREFIX*" \
    --query "RouteTables[?Associations[0].Main!=\`true\`].RouteTableId" --output text 2>/dev/null | tr '\t' '\n' | while read -r rt_id; do
    if [[ -n "$rt_id" ]]; then
        echo "  Deleting EC2 route table: $rt_id"
        # Disassociate non-main associations first
        aws ec2 describe-route-tables --route-table-ids "$rt_id" --region "$REGION" \
            --query "RouteTables[0].Associations[?!Main].RouteTableAssociationId" --output text 2>/dev/null | tr '\t' '\n' | while read -r assoc_id; do
            [[ -n "$assoc_id" ]] && aws ec2 disassociate-route-table --association-id "$assoc_id" --region "$REGION" 2>/dev/null || true
        done
        aws ec2 delete-route-table --route-table-id "$rt_id" --region "$REGION" 2>/dev/null || true
    fi
done

# --- EC2 security groups (non-default, before VPCs)
echo "Cleaning EC2 test security groups..."
aws ec2 describe-security-groups --region "$REGION" \
    --filters "Name=tag:Name,Values=*$FORMAE_PREFIX*" \
    --query "SecurityGroups[?GroupName!='default'].GroupId" --output text 2>/dev/null | tr '\t' '\n' | while read -r sg_id; do
    if [[ -n "$sg_id" ]]; then
        echo "  Deleting EC2 security group: $sg_id"
        aws ec2 delete-security-group --group-id "$sg_id" --region "$REGION" 2>/dev/null || true
    fi
done

# --- EC2 network ACLs (non-default, before VPCs)
echo "Cleaning EC2 test network ACLs..."
aws ec2 describe-network-acls --region "$REGION" \
    --filters "Name=tag:Name,Values=*$FORMAE_PREFIX*" \
    --query "NetworkAcls[?!IsDefault].NetworkAclId" --output text 2>/dev/null | tr '\t' '\n' | while read -r nacl_id; do
    if [[ -n "$nacl_id" ]]; then
        echo "  Deleting EC2 network ACL: $nacl_id"
        aws ec2 delete-network-acl --network-acl-id "$nacl_id" --region "$REGION" 2>/dev/null || true
    fi
done

# --- EC2 volumes (detached, with test prefix)
echo "Cleaning EC2 test volumes..."
aws ec2 describe-volumes --region "$REGION" \
    --filters "Name=tag:Name,Values=*$FORMAE_PREFIX*" "Name=status,Values=available" \
    --query "Volumes[].VolumeId" --output text 2>/dev/null | tr '\t' '\n' | while read -r vol_id; do
    if [[ -n "$vol_id" ]]; then
        echo "  Deleting EC2 volume: $vol_id"
        aws ec2 delete-volume --volume-id "$vol_id" --region "$REGION" 2>/dev/null || true
    fi
done

# --- EFS access points (before file systems)
echo "Cleaning EFS test access points..."
aws efs describe-access-points --region "$REGION" 2>/dev/null | \
    jq -r ".AccessPoints[]? | select(.Name // \"\" | test(\"$FORMAE_PREFIX|$SDK_PREFIX|$TEST_PREFIX\")) | .AccessPointId" 2>/dev/null | while read -r ap_id; do
    if [[ -n "$ap_id" ]]; then
        echo "  Deleting EFS access point: $ap_id"
        aws efs delete-access-point --access-point-id "$ap_id" --region "$REGION" 2>/dev/null || true
    fi
done

# 23a. Delete EC2 flow logs with test prefix (before VPCs)
echo "Cleaning EC2 test flow logs..."
aws ec2 describe-flow-logs --region "$REGION" \
    --filter "Name=tag:Name,Values=*$FORMAE_PREFIX*" \
    --query "FlowLogs[].FlowLogId" --output text 2>/dev/null | tr '\t' '\n' | while read -r fl_id; do
    if [[ -n "$fl_id" ]]; then
        echo "  Deleting EC2 flow log: $fl_id"
        aws ec2 delete-flow-logs --flow-log-ids "$fl_id" --region "$REGION" 2>/dev/null || true
    fi
done

# 23b. Delete EC2 VPCs with test prefix (by Name tag) - after all VPC dependents
echo "Cleaning EC2 test VPCs..."
# Delete VPCs matching the test prefix name tag
aws ec2 describe-vpcs --region "$REGION" \
    --filters "Name=tag:Name,Values=*$FORMAE_PREFIX*" \
    --query "Vpcs[].VpcId" --output text 2>/dev/null | tr '\t' '\n' | while read -r vpc_id; do
    if [[ -n "$vpc_id" ]]; then
        echo "  Deleting EC2 VPC: $vpc_id"
        aws ec2 delete-vpc --vpc-id "$vpc_id" --region "$REGION" 2>/dev/null || true
    fi
done
# Delete untagged VPCs (no Name tag) — these are orphaned test VPCs from
# cancelled or failed runs. Conformance test fixtures create VPCs without
# Name tags, so any VPC without a Name tag is safe to clean up.
echo "Cleaning orphaned untagged VPCs..."
DEFAULT_VPC=$(aws ec2 describe-vpcs --region "$REGION" --filters "Name=isDefault,Values=true" --query "Vpcs[0].VpcId" --output text 2>/dev/null)

# clean_orphan_vpc: delete all dependencies of a single untagged VPC, then
# delete the VPC itself. Called via xargs -P so multiple VPCs are processed
# concurrently. All variables it needs (REGION, DEFAULT_VPC) must be exported
# by the caller — see the export block below.
clean_orphan_vpc() {
    local vpc_id="$1"
    [[ -z "$vpc_id" || "$vpc_id" == "$DEFAULT_VPC" ]] && return 0

    echo "  Cleaning orphaned VPC: $vpc_id"
    # Delete any ALBs/NLBs attached to this VPC. The tagged-prefix
    # cleanup at line ~450 catches most by name, but VPC-ID lookup
    # also catches ALBs whose names don't match any known prefix
    # (e.g., legacy fixture names). Initiated here, listeners first.
    aws elbv2 describe-load-balancers --region "$REGION" \
        --query "LoadBalancers[?VpcId=='$vpc_id'].LoadBalancerArn" --output text 2>/dev/null | tr '\t' '\n' | while read -r lb_arn; do
        if [[ -n "$lb_arn" ]]; then
            echo "    Deleting ALB attached to $vpc_id: $lb_arn"
            aws elbv2 describe-listeners --load-balancer-arn "$lb_arn" --region "$REGION" \
                --query "Listeners[].ListenerArn" --output text 2>/dev/null | tr '\t' '\n' | while read -r listener; do
                [[ -n "$listener" ]] && aws elbv2 delete-listener --listener-arn "$listener" --region "$REGION" 2>/dev/null || true
            done
            aws elbv2 delete-load-balancer --load-balancer-arn "$lb_arn" --region "$REGION" 2>/dev/null || true
        fi
    done
    # Release EFS mount targets before attempting ENI force-detach.
    # EFS creates ENIs with interface-type=efs owned by an internal AWS
    # owner ID. These ENIs cannot be force-detached via the EC2 API
    # (returns OperationNotPermitted). They must be released through the
    # EFS API: deleting the mount target causes the EFS service to
    # terminate its own ENI within ~30-60s. The ENI wait loop below then
    # catches those released ENIs naturally without any extra wait code.
    aws ec2 describe-network-interfaces --region "$REGION" \
        --filters "Name=vpc-id,Values=$vpc_id" "Name=interface-type,Values=efs" \
        --query "NetworkInterfaces[].Description" --output text 2>/dev/null | \
        grep -oE 'fsmt-[a-f0-9]+' | sort -u | while read -r mt_id; do
        [[ -n "$mt_id" ]] && {
            echo "    Deleting EFS mount target $mt_id (releases EFS-owned ENI in VPC $vpc_id)"
            aws efs delete-mount-target --mount-target-id "$mt_id" --region "$REGION" 2>/dev/null || true
        }
    done
    # Detach + delete ENIs. delete-network-interface on an attached
    # ENI (the common case for Fargate-managed ENIs lingering from a
    # cancelled run) fails with InvalidParameterValue.InUse; force-
    # detach first, brief sleep for AWS to propagate the detach,
    # then delete. Poll briefly afterwards for residuals so the
    # subnet delete below has a chance to succeed.
    # Skip ENIs owned by amazon-elb — those are released automatically
    # by AWS once the ALB is fully gone; force-detach returns
    # OperationNotPermitted on them.
    aws ec2 describe-network-interfaces --region "$REGION" \
        --filters "Name=vpc-id,Values=$vpc_id" \
        --query "NetworkInterfaces[?RequesterId!='amazon-elb'].{Id:NetworkInterfaceId,Attachment:Attachment}" --output json 2>/dev/null | \
        jq -c '.[]' 2>/dev/null | while read -r eni_json; do
        eni_id=$(echo "$eni_json" | jq -r '.Id')
        if [[ -n "$eni_id" ]]; then
            attach_id=$(echo "$eni_json" | jq -r '.Attachment.AttachmentId // empty')
            [[ -n "$attach_id" ]] && aws ec2 detach-network-interface --attachment-id "$attach_id" --force --region "$REGION" 2>/dev/null || true
        fi
    done
    # Wait up to 5 minutes for ENIs to release. Fargate ENIs take
    # 30-60s; ELB-owned ENIs (amazon-elb requester) released by AWS
    # 1-5 min after the ALB is fully deleted; EFS-owned ENIs released
    # ~30-60s after delete-mount-target (see above).
    waited=0
    while [ $waited -lt 300 ]; do
        remaining=$(aws ec2 describe-network-interfaces --region "$REGION" \
            --filters "Name=vpc-id,Values=$vpc_id" \
            --query "length(NetworkInterfaces)" --output text 2>/dev/null || echo 0)
        [ "$remaining" = "0" ] && break
        sleep 10
        waited=$((waited + 10))
    done
    # Final pass: attempt explicit delete on whatever is left.
    aws ec2 describe-network-interfaces --region "$REGION" \
        --filters "Name=vpc-id,Values=$vpc_id" \
        --query "NetworkInterfaces[].NetworkInterfaceId" --output text 2>/dev/null | tr '\t' '\n' | while read -r eni_id; do
        [[ -n "$eni_id" ]] && aws ec2 delete-network-interface --network-interface-id "$eni_id" --region "$REGION" 2>/dev/null || true
    done
    # Delete NAT gateways (block subnet+VPC deletes; takes ~30-90s).
    # Initiate deletes first, then wait below.
    nat_ids=$(aws ec2 describe-nat-gateways --region "$REGION" \
        --filter "Name=vpc-id,Values=$vpc_id" "Name=state,Values=available,pending,failed" \
        --query "NatGateways[].NatGatewayId" --output text 2>/dev/null || true)
    for nat_id in $nat_ids; do
        [[ -n "$nat_id" ]] && aws ec2 delete-nat-gateway --nat-gateway-id "$nat_id" --region "$REGION" 2>/dev/null || true
    done
    if [[ -n "$nat_ids" ]]; then
        waited=0
        while [ $waited -lt 120 ]; do
            remaining=$(aws ec2 describe-nat-gateways --region "$REGION" \
                --filter "Name=vpc-id,Values=$vpc_id" "Name=state,Values=available,pending,deleting" \
                --query "length(NatGateways)" --output text 2>/dev/null || echo 0)
            [ "$remaining" = "0" ] && break
            sleep 10
            waited=$((waited + 10))
        done
    fi
    # Delete VPC endpoints (block VPC delete)
    aws ec2 describe-vpc-endpoints --region "$REGION" \
        --filters "Name=vpc-id,Values=$vpc_id" \
        --query "VpcEndpoints[?State!='deleted'].VpcEndpointId" --output text 2>/dev/null | tr '\t' '\n' | while read -r ep_id; do
        [[ -n "$ep_id" ]] && aws ec2 delete-vpc-endpoints --vpc-endpoint-ids "$ep_id" --region "$REGION" 2>/dev/null || true
    done
    # Detach VPN gateways attached to this VPC
    aws ec2 describe-vpn-gateways --region "$REGION" \
        --filters "Name=attachment.vpc-id,Values=$vpc_id" "Name=state,Values=available" \
        --query "VpnGateways[].VpnGatewayId" --output text 2>/dev/null | tr '\t' '\n' | while read -r vgw_id; do
        if [[ -n "$vgw_id" ]]; then
            aws ec2 detach-vpn-gateway --vpn-gateway-id "$vgw_id" --vpc-id "$vpc_id" --region "$REGION" 2>/dev/null || true
            aws ec2 delete-vpn-gateway --vpn-gateway-id "$vgw_id" --region "$REGION" 2>/dev/null || true
        fi
    done
    # Delete VPC peering connections (requester or accepter)
    aws ec2 describe-vpc-peering-connections --region "$REGION" \
        --filters "Name=requester-vpc-info.vpc-id,Values=$vpc_id" "Name=status-code,Values=active,pending-acceptance,provisioning" \
        --query "VpcPeeringConnections[].VpcPeeringConnectionId" --output text 2>/dev/null | tr '\t' '\n' | while read -r pcx_id; do
        [[ -n "$pcx_id" ]] && aws ec2 delete-vpc-peering-connection --vpc-peering-connection-id "$pcx_id" --region "$REGION" 2>/dev/null || true
    done
    aws ec2 describe-vpc-peering-connections --region "$REGION" \
        --filters "Name=accepter-vpc-info.vpc-id,Values=$vpc_id" "Name=status-code,Values=active,pending-acceptance,provisioning" \
        --query "VpcPeeringConnections[].VpcPeeringConnectionId" --output text 2>/dev/null | tr '\t' '\n' | while read -r pcx_id; do
        [[ -n "$pcx_id" ]] && aws ec2 delete-vpc-peering-connection --vpc-peering-connection-id "$pcx_id" --region "$REGION" 2>/dev/null || true
    done
    # Delete transit gateway VPC attachments
    aws ec2 describe-transit-gateway-vpc-attachments --region "$REGION" \
        --filters "Name=vpc-id,Values=$vpc_id" "Name=state,Values=available,pending,modifying" \
        --query "TransitGatewayVpcAttachments[].TransitGatewayAttachmentId" --output text 2>/dev/null | tr '\t' '\n' | while read -r tgw_att_id; do
        [[ -n "$tgw_att_id" ]] && aws ec2 delete-transit-gateway-vpc-attachment --transit-gateway-attachment-id "$tgw_att_id" --region "$REGION" 2>/dev/null || true
    done
    # Delete egress-only internet gateways attached to this VPC
    aws ec2 describe-egress-only-internet-gateways --region "$REGION" \
        --query "EgressOnlyInternetGateways[?Attachments[?VpcId=='$vpc_id']].EgressOnlyInternetGatewayId" --output text 2>/dev/null | tr '\t' '\n' | while read -r eigw_id; do
        [[ -n "$eigw_id" ]] && aws ec2 delete-egress-only-internet-gateway --egress-only-internet-gateway-id "$eigw_id" --region "$REGION" 2>/dev/null || true
    done
    # Delete subnets
    aws ec2 describe-subnets --region "$REGION" \
        --filters "Name=vpc-id,Values=$vpc_id" \
        --query "Subnets[].SubnetId" --output text 2>/dev/null | tr '\t' '\n' | while read -r subnet_id; do
        [[ -n "$subnet_id" ]] && aws ec2 delete-subnet --subnet-id "$subnet_id" --region "$REGION" 2>/dev/null || true
    done
    # Delete non-main route tables (each may have explicit associations
    # to disassociate first; disassociating non-main ones is idempotent)
    aws ec2 describe-route-tables --region "$REGION" \
        --filters "Name=vpc-id,Values=$vpc_id" \
        --query "RouteTables[?Associations[0].Main!=\`true\`].RouteTableId" --output text 2>/dev/null | tr '\t' '\n' | while read -r rt_id; do
        if [[ -n "$rt_id" ]]; then
            aws ec2 describe-route-tables --route-table-ids "$rt_id" --region "$REGION" \
                --query "RouteTables[0].Associations[?!Main].RouteTableAssociationId" --output text 2>/dev/null | tr '\t' '\n' | while read -r assoc_id; do
                [[ -n "$assoc_id" ]] && aws ec2 disassociate-route-table --association-id "$assoc_id" --region "$REGION" 2>/dev/null || true
            done
            aws ec2 delete-route-table --route-table-id "$rt_id" --region "$REGION" 2>/dev/null || true
        fi
    done
    # Delete non-default network ACLs
    aws ec2 describe-network-acls --region "$REGION" \
        --filters "Name=vpc-id,Values=$vpc_id" \
        --query "NetworkAcls[?!IsDefault].NetworkAclId" --output text 2>/dev/null | tr '\t' '\n' | while read -r nacl_id; do
        [[ -n "$nacl_id" ]] && aws ec2 delete-network-acl --network-acl-id "$nacl_id" --region "$REGION" 2>/dev/null || true
    done
    # Detach and delete internet gateways
    aws ec2 describe-internet-gateways --region "$REGION" \
        --filters "Name=attachment.vpc-id,Values=$vpc_id" \
        --query "InternetGateways[].InternetGatewayId" --output text 2>/dev/null | tr '\t' '\n' | while read -r igw_id; do
        if [[ -n "$igw_id" ]]; then
            aws ec2 detach-internet-gateway --internet-gateway-id "$igw_id" --vpc-id "$vpc_id" --region "$REGION" 2>/dev/null || true
            aws ec2 delete-internet-gateway --internet-gateway-id "$igw_id" --region "$REGION" 2>/dev/null || true
        fi
    done
    # Delete non-default security groups
    aws ec2 describe-security-groups --region "$REGION" \
        --filters "Name=vpc-id,Values=$vpc_id" \
        --query "SecurityGroups[?GroupName!='default'].GroupId" --output text 2>/dev/null | tr '\t' '\n' | while read -r sg_id; do
        [[ -n "$sg_id" ]] && aws ec2 delete-security-group --group-id "$sg_id" --region "$REGION" 2>/dev/null || true
    done
    # Delete VPC — surface the error if it still fails so we can see
    # what's blocking. The script continues either way.
    if ! aws ec2 delete-vpc --vpc-id "$vpc_id" --region "$REGION" 2>&1; then
        echo "  WARN: delete-vpc $vpc_id failed (see error above)"
        echo "  Enumerating remaining dependencies on $vpc_id:"
        echo "    ENIs:"
        aws ec2 describe-network-interfaces --region "$REGION" \
            --filters "Name=vpc-id,Values=$vpc_id" \
            --query "NetworkInterfaces[].{Id:NetworkInterfaceId,Status:Status,Type:InterfaceType,Desc:Description,Owner:RequesterId}" \
            --output table 2>&1 | sed 's/^/      /' || true
        echo "    Subnets:"
        aws ec2 describe-subnets --region "$REGION" \
            --filters "Name=vpc-id,Values=$vpc_id" \
            --query "Subnets[].{Id:SubnetId,Cidr:CidrBlock,Az:AvailabilityZone}" \
            --output table 2>&1 | sed 's/^/      /' || true
        echo "    NAT gateways:"
        aws ec2 describe-nat-gateways --region "$REGION" \
            --filter "Name=vpc-id,Values=$vpc_id" \
            --query "NatGateways[].{Id:NatGatewayId,State:State}" \
            --output table 2>&1 | sed 's/^/      /' || true
        echo "    Internet gateways:"
        aws ec2 describe-internet-gateways --region "$REGION" \
            --filters "Name=attachment.vpc-id,Values=$vpc_id" \
            --query "InternetGateways[].{Id:InternetGatewayId,State:Attachments[0].State}" \
            --output table 2>&1 | sed 's/^/      /' || true
        echo "    Egress-only IGWs:"
        aws ec2 describe-egress-only-internet-gateways --region "$REGION" \
            --query "EgressOnlyInternetGateways[?Attachments[?VpcId=='$vpc_id']].{Id:EgressOnlyInternetGatewayId}" \
            --output table 2>&1 | sed 's/^/      /' || true
        echo "    VPN gateways:"
        aws ec2 describe-vpn-gateways --region "$REGION" \
            --filters "Name=attachment.vpc-id,Values=$vpc_id" \
            --query "VpnGateways[].{Id:VpnGatewayId,State:State}" \
            --output table 2>&1 | sed 's/^/      /' || true
        echo "    VPC peering connections (as requester):"
        aws ec2 describe-vpc-peering-connections --region "$REGION" \
            --filters "Name=requester-vpc-info.vpc-id,Values=$vpc_id" \
            --query "VpcPeeringConnections[].{Id:VpcPeeringConnectionId,State:Status.Code}" \
            --output table 2>&1 | sed 's/^/      /' || true
        echo "    VPC peering connections (as accepter):"
        aws ec2 describe-vpc-peering-connections --region "$REGION" \
            --filters "Name=accepter-vpc-info.vpc-id,Values=$vpc_id" \
            --query "VpcPeeringConnections[].{Id:VpcPeeringConnectionId,State:Status.Code}" \
            --output table 2>&1 | sed 's/^/      /' || true
        echo "    TGW VPC attachments:"
        aws ec2 describe-transit-gateway-vpc-attachments --region "$REGION" \
            --filters "Name=vpc-id,Values=$vpc_id" \
            --query "TransitGatewayVpcAttachments[].{Id:TransitGatewayAttachmentId,State:State}" \
            --output table 2>&1 | sed 's/^/      /' || true
        echo "    VPC endpoints:"
        aws ec2 describe-vpc-endpoints --region "$REGION" \
            --filters "Name=vpc-id,Values=$vpc_id" \
            --query "VpcEndpoints[].{Id:VpcEndpointId,State:State,Type:VpcEndpointType,Service:ServiceName}" \
            --output table 2>&1 | sed 's/^/      /' || true
        echo "    Route tables:"
        aws ec2 describe-route-tables --region "$REGION" \
            --filters "Name=vpc-id,Values=$vpc_id" \
            --query "RouteTables[].{Id:RouteTableId,Main:Associations[?Main].Main|[0]}" \
            --output table 2>&1 | sed 's/^/      /' || true
        echo "    Network ACLs:"
        aws ec2 describe-network-acls --region "$REGION" \
            --filters "Name=vpc-id,Values=$vpc_id" \
            --query "NetworkAcls[].{Id:NetworkAclId,Default:IsDefault}" \
            --output table 2>&1 | sed 's/^/      /' || true
        echo "    Security groups:"
        aws ec2 describe-security-groups --region "$REGION" \
            --filters "Name=vpc-id,Values=$vpc_id" \
            --query "SecurityGroups[].{Id:GroupId,Name:GroupName}" \
            --output table 2>&1 | sed 's/^/      /' || true
    fi
}

# Export variables and the function so xargs subshells can see them.
export -f clean_orphan_vpc
export REGION DEFAULT_VPC

# Process orphan VPCs in parallel (up to 8 at a time). Each VPC can block
# for up to 5 min on ENI release + 2 min on NAT GW deletion; running them
# serially meant 8-10 orphans took 50+ minutes. With -P 8 that collapses
# to roughly one VPC's worth of wait time. Output from concurrent VPCs will
# interleave, but each VPC logs its own ID so the output remains readable.
aws ec2 describe-vpcs --region "$REGION" \
    --query "Vpcs[?!(Tags[?Key=='Name'])].VpcId" --output text 2>/dev/null | tr '\t' '\n' | \
    grep -v "^$DEFAULT_VPC$" | grep -v "^$" | \
    xargs -P 8 -I {} bash -c 'clean_orphan_vpc "$@"' _ {} || true

# After orphan-VPC sweep, report how many non-default VPCs remain so the
# CI run shows whether the account is near or at the VPC quota.
remaining_vpcs=$(aws ec2 describe-vpcs --region "$REGION" \
    --query "length(Vpcs[?IsDefault==\`false\`])" --output text 2>/dev/null || echo "?")
echo "  Non-default VPCs remaining in $REGION: $remaining_vpcs"

# ============================================================================
# CloudWatch Resources (regional)
# ============================================================================

# 24. Delete CloudWatch Log Groups with test prefix
echo "Cleaning CloudWatch test log groups..."
aws logs describe-log-groups --region "$REGION" \
    --query "logGroups[?contains(logGroupName, '$TEST_PREFIX')].logGroupName" --output text 2>/dev/null | tr '\t' '\n' | while read -r lg; do
    if [[ -n "$lg" ]]; then
        echo "  Deleting log group: $lg"
        aws logs delete-log-group --log-group-name "$lg" --region "$REGION" 2>/dev/null || true
    fi
done

# ============================================================================
# Secrets Manager Resources (regional)
# ============================================================================

# 25. Delete Secrets Manager secrets with test prefix
echo "Cleaning Secrets Manager test secrets..."
aws secretsmanager list-secrets --region "$REGION" \
    --query "SecretList[?contains(Name, '$TEST_PREFIX')].Name" --output text 2>/dev/null | tr '\t' '\n' | while read -r secret; do
    if [[ -n "$secret" ]]; then
        echo "  Deleting secret: $secret"
        aws secretsmanager delete-secret --secret-id "$secret" --force-delete-without-recovery --region "$REGION" 2>/dev/null || true
    fi
done

# ============================================================================
# SQS Resources (regional)
# ============================================================================

# 26. Delete SQS queues with test prefix
echo "Cleaning SQS test queues..."
aws sqs list-queues --region "$REGION" --queue-name-prefix "$TEST_PREFIX" --query "QueueUrls[]" --output text 2>/dev/null | tr '\t' '\n' | while read -r queue; do
    if [[ -n "$queue" ]]; then
        echo "  Deleting SQS queue: $queue"
        aws sqs delete-queue --queue-url "$queue" --region "$REGION" 2>/dev/null || true
    fi
done

# ============================================================================
# DynamoDB Resources (regional)
# ============================================================================

# 27. Delete DynamoDB tables with test prefix
echo "Cleaning DynamoDB test tables..."
aws dynamodb list-tables --region "$REGION" --query "TableNames[?contains(@, '$TEST_PREFIX')]" --output text 2>/dev/null | tr '\t' '\n' | while read -r table; do
    if [[ -n "$table" ]]; then
        echo "  Deleting DynamoDB table: $table"
        aws dynamodb delete-table --table-name "$table" --region "$REGION" 2>/dev/null || true
    fi
done

# ============================================================================
# ECR Resources (regional)
# ============================================================================

# 28. Delete ECR repositories with test prefix
echo "Cleaning ECR test repositories..."
aws ecr describe-repositories --region "$REGION" \
    --query "repositories[?contains(repositoryName, '$TEST_PREFIX')].repositoryName" --output text 2>/dev/null | tr '\t' '\n' | while read -r repo; do
    if [[ -n "$repo" ]]; then
        echo "  Deleting ECR repository: $repo"
        aws ecr delete-repository --repository-name "$repo" --region "$REGION" --force 2>/dev/null || true
    fi
done

# 28a. Delete ECR public repositories with test prefix
echo "Cleaning ECR test public repositories..."
aws ecr-public describe-repositories --region us-east-1 \
    --query "repositories[?contains(repositoryName, '$TEST_PREFIX') || contains(repositoryName, '$SDK_PREFIX') || contains(repositoryName, '$FORMAE_PREFIX')].repositoryName" --output text 2>/dev/null | tr '\t' '\n' | while read -r repo; do
    if [[ -n "$repo" ]]; then
        echo "  Deleting ECR public repository: $repo"
        aws ecr-public delete-repository --repository-name "$repo" --region us-east-1 --force 2>/dev/null || true
    fi
done

# 28b. Delete ECR pull-through cache rules with test prefix
echo "Cleaning ECR test pull-through cache rules..."
aws ecr describe-pull-through-cache-rules --region "$REGION" \
    --query "pullThroughCacheRules[?contains(ecrRepositoryPrefix, '$FORMAE_PREFIX') || contains(ecrRepositoryPrefix, 'formae-sdk-test')].ecrRepositoryPrefix" --output text 2>/dev/null | tr '\t' '\n' | while read -r prefix; do
    if [[ -n "$prefix" ]]; then
        echo "  Deleting ECR pull-through cache rule: $prefix"
        aws ecr delete-pull-through-cache-rule --ecr-repository-prefix "$prefix" --region "$REGION" 2>/dev/null || true
    fi
done

# 28c. Delete ECR repository creation templates with test prefix
echo "Cleaning ECR test repository creation templates..."
aws ecr describe-repository-creation-templates --region "$REGION" \
    --query "registryId" --output text 2>/dev/null > /dev/null  # Just check if accessible
aws ecr describe-repository-creation-templates --region "$REGION" 2>/dev/null | \
    jq -r ".repositoryCreationTemplates[]? | select(.prefix | test(\"$FORMAE_PREFIX|$SDK_PREFIX|$TEST_PREFIX\")) | .prefix" 2>/dev/null | while read -r prefix; do
    if [[ -n "$prefix" ]]; then
        echo "  Deleting ECR repository creation template: $prefix"
        aws ecr delete-repository-creation-template --prefix "$prefix" --region "$REGION" 2>/dev/null || true
    fi
done

# ============================================================================
# ECS Resources (regional)
# ============================================================================

# 29. Delete ECS clusters with test prefix
echo "Cleaning ECS test clusters..."
aws ecs list-clusters --region "$REGION" --query "clusterArns[]" --output text 2>/dev/null | tr '\t' '\n' | while read -r cluster_arn; do
    if [[ -n "$cluster_arn" && ("$cluster_arn" == *"$FORMAE_PREFIX"* || "$cluster_arn" == *"$SDK_PREFIX"* || "$cluster_arn" == *"$LEGACY_LB_PREFIX"*) ]]; then
        echo "  Deleting ECS cluster: $cluster_arn"
        aws ecs delete-cluster --cluster "$cluster_arn" --region "$REGION" 2>/dev/null || true
    fi
done

# 30. Deregister ECS task definitions with test prefix
echo "Cleaning ECS test task definitions..."
for ecs_family_prefix in "$FORMAE_PREFIX" "$SDK_PREFIX" "$LEGACY_LB_PREFIX"; do
    aws ecs list-task-definition-families --region "$REGION" --family-prefix "$ecs_family_prefix" --status ACTIVE \
        --query "families[]" --output text 2>/dev/null | tr '\t' '\n' | while read -r family; do
        if [[ -n "$family" ]]; then
            echo "  Deregistering task definitions for family: $family"
            aws ecs list-task-definitions --region "$REGION" --family-prefix "$family" --status ACTIVE \
                --query "taskDefinitionArns[]" --output text 2>/dev/null | tr '\t' '\n' | while read -r td_arn; do
                [[ -n "$td_arn" ]] && aws ecs deregister-task-definition --task-definition "$td_arn" --region "$REGION" 2>/dev/null || true
            done
            # Also delete inactive task definitions
            aws ecs delete-task-definitions --region "$REGION" --task-definitions \
                $(aws ecs list-task-definitions --region "$REGION" --family-prefix "$family" --status INACTIVE \
                    --query "taskDefinitionArns[]" --output text 2>/dev/null) 2>/dev/null || true
        fi
    done
done

# ============================================================================
# EFS Resources (regional)
# ============================================================================

# 31. Delete EFS file systems with test prefix (by Name tag)
echo "Cleaning EFS test file systems..."
aws efs describe-file-systems --region "$REGION" \
    --query "FileSystems[].{Id:FileSystemId, Tags:Tags}" --output json 2>/dev/null | \
    jq -r '.[] | select(.Tags[]? | select(.Key == "Name" and (.Value | test("'"$FORMAE_PREFIX"'")))) | .Id' 2>/dev/null | while read -r fs_id; do
    if [[ -n "$fs_id" ]]; then
        echo "  Deleting EFS file system: $fs_id"
        # Delete mount targets first
        aws efs describe-mount-targets --file-system-id "$fs_id" --region "$REGION" \
            --query "MountTargets[].MountTargetId" --output text 2>/dev/null | tr '\t' '\n' | while read -r mt_id; do
            [[ -n "$mt_id" ]] && aws efs delete-mount-target --mount-target-id "$mt_id" --region "$REGION" 2>/dev/null || true
        done
        # Wait for mount targets to be deleted
        sleep 5
        aws efs delete-file-system --file-system-id "$fs_id" --region "$REGION" 2>/dev/null || true
    fi
done

# ============================================================================
# Elastic Beanstalk Resources (regional)
# ============================================================================

# 32. Delete Elastic Beanstalk applications with test prefix
echo "Cleaning Elastic Beanstalk test applications..."
aws elasticbeanstalk describe-applications --region "$REGION" \
    --query "Applications[?contains(ApplicationName, '$SDK_PREFIX')].ApplicationName" --output text 2>/dev/null | tr '\t' '\n' | while read -r app; do
    if [[ -n "$app" ]]; then
        echo "  Deleting Elastic Beanstalk application: $app"
        aws elasticbeanstalk delete-application --application-name "$app" --terminate-env-by-force --region "$REGION" 2>/dev/null || true
    fi
done

# ============================================================================
# RDS Resources (regional)
# ============================================================================

# 33. Delete RDS DB parameter groups with test prefix
echo "Cleaning RDS test DB parameter groups..."
aws rds describe-db-parameter-groups --region "$REGION" \
    --query "DBParameterGroups[?contains(DBParameterGroupName, '$SDK_PREFIX')].DBParameterGroupName" --output text 2>/dev/null | tr '\t' '\n' | while read -r pg; do
    if [[ -n "$pg" ]]; then
        echo "  Deleting RDS DB parameter group: $pg"
        aws rds delete-db-parameter-group --db-parameter-group-name "$pg" --region "$REGION" 2>/dev/null || true
    fi
done

# 34. Delete RDS DB cluster parameter groups with test prefix
echo "Cleaning RDS test DB cluster parameter groups..."
aws rds describe-db-cluster-parameter-groups --region "$REGION" \
    --query "DBClusterParameterGroups[?contains(DBClusterParameterGroupName, '$SDK_PREFIX')].DBClusterParameterGroupName" --output text 2>/dev/null | tr '\t' '\n' | while read -r cpg; do
    if [[ -n "$cpg" ]]; then
        echo "  Deleting RDS DB cluster parameter group: $cpg"
        aws rds delete-db-cluster-parameter-group --db-cluster-parameter-group-name "$cpg" --region "$REGION" 2>/dev/null || true
    fi
done

# 35. Delete RDS global clusters with test prefix
echo "Cleaning RDS test global clusters..."
aws rds describe-global-clusters --region "$REGION" \
    --query "GlobalClusters[?contains(GlobalClusterIdentifier, '$SDK_PREFIX') || contains(GlobalClusterIdentifier, '$FORMAE_PREFIX')].GlobalClusterIdentifier" --output text 2>/dev/null | tr '\t' '\n' | while read -r gc; do
    if [[ -n "$gc" ]]; then
        echo "  Deleting RDS global cluster: $gc"
        # Disable deletion protection first
        aws rds modify-global-cluster --global-cluster-identifier "$gc" --no-deletion-protection --region "$REGION" 2>/dev/null || true
        aws rds delete-global-cluster --global-cluster-identifier "$gc" --region "$REGION" 2>/dev/null || true
    fi
done

# 36. Delete RDS option groups with test prefix
echo "Cleaning RDS test option groups..."
aws rds describe-option-groups --region "$REGION" \
    --query "OptionGroupsList[?contains(OptionGroupName, '$SDK_PREFIX') || contains(OptionGroupName, '$FORMAE_PREFIX')].OptionGroupName" --output text 2>/dev/null | tr '\t' '\n' | while read -r og; do
    if [[ -n "$og" ]]; then
        echo "  Deleting RDS option group: $og"
        aws rds delete-option-group --option-group-name "$og" --region "$REGION" 2>/dev/null || true
    fi
done

# --- Lambda event source mappings (before functions and queues)
echo "Cleaning Lambda test event source mappings..."
aws lambda list-event-source-mappings --region "$REGION" --query "EventSourceMappings[]" --output json 2>/dev/null | \
    jq -r '.[] | select(.FunctionArn // "" | test("'"$SDK_PREFIX"'|'"$FORMAE_PREFIX"'|'"$TEST_PREFIX"'")) | .UUID' 2>/dev/null | while read -r uuid; do
    if [[ -n "$uuid" ]]; then
        echo "  Deleting Lambda event source mapping: $uuid"
        aws lambda delete-event-source-mapping --uuid "$uuid" --region "$REGION" 2>/dev/null || true
    fi
done

# --- Lambda functions with test prefix
echo "Cleaning Lambda test functions..."
aws lambda list-functions --region "$REGION" --query "Functions[?contains(FunctionName, '$SDK_PREFIX') || contains(FunctionName, '$FORMAE_PREFIX') || contains(FunctionName, '$TEST_PREFIX')].FunctionName" --output text 2>/dev/null | tr '\t' '\n' | while read -r fn; do
    if [[ -n "$fn" ]]; then
        echo "  Deleting Lambda function: $fn"
        # Delete aliases first
        aws lambda list-aliases --function-name "$fn" --region "$REGION" --query "Aliases[].Name" --output text 2>/dev/null | tr '\t' '\n' | while read -r alias; do
            [[ -n "$alias" ]] && aws lambda delete-alias --function-name "$fn" --name "$alias" --region "$REGION" 2>/dev/null || true
        done
        # Delete function URL config
        aws lambda delete-function-url-config --function-name "$fn" --region "$REGION" 2>/dev/null || true
        # Delete event invoke configs
        aws lambda delete-function-event-invoke-config --function-name "$fn" --region "$REGION" 2>/dev/null || true
        aws lambda delete-function --function-name "$fn" --region "$REGION" 2>/dev/null || true
    fi
done

# --- API Gateway REST APIs with test prefix
echo "Cleaning API Gateway test REST APIs..."
aws apigateway get-rest-apis --region "$REGION" --query "items[?contains(name, '$FORMAE_PREFIX') || contains(name, '$SDK_PREFIX') || contains(name, '$TEST_PREFIX')].id" --output text 2>/dev/null | tr '\t' '\n' | while read -r api_id; do
    if [[ -n "$api_id" ]]; then
        echo "  Deleting API Gateway REST API: $api_id"
        aws apigateway delete-rest-api --rest-api-id "$api_id" --region "$REGION" 2>/dev/null || true
    fi
done

# --- API Gateway API keys with test prefix
echo "Cleaning API Gateway test API keys..."
aws apigateway get-api-keys --region "$REGION" --query "items[?contains(name, '$FORMAE_PREFIX') || contains(name, '$SDK_PREFIX') || contains(name, '$TEST_PREFIX')].id" --output text 2>/dev/null | tr '\t' '\n' | while read -r key_id; do
    if [[ -n "$key_id" ]]; then
        echo "  Deleting API Gateway API key: $key_id"
        aws apigateway delete-api-key --api-key "$key_id" --region "$REGION" 2>/dev/null || true
    fi
done

# --- API Gateway usage plans with test prefix
echo "Cleaning API Gateway test usage plans..."
aws apigateway get-usage-plans --region "$REGION" --query "items[?contains(name, '$FORMAE_PREFIX') || contains(name, '$SDK_PREFIX') || contains(name, '$TEST_PREFIX')].id" --output text 2>/dev/null | tr '\t' '\n' | while read -r plan_id; do
    if [[ -n "$plan_id" ]]; then
        echo "  Deleting API Gateway usage plan: $plan_id"
        aws apigateway delete-usage-plan --usage-plan-id "$plan_id" --region "$REGION" 2>/dev/null || true
    fi
done

# 37. Delete Lambda code signing configs with test prefix
echo "Cleaning Lambda test code signing configs..."
aws lambda list-code-signing-configs --region "$REGION" --max-items 100 2>/dev/null | \
    jq -r ".CodeSigningConfigs[]? | select(.Description // \"\" | test(\"$FORMAE_PREFIX|$SDK_PREFIX|$TEST_PREFIX\")) | .CodeSigningConfigArn" 2>/dev/null | while read -r csc_arn; do
    if [[ -n "$csc_arn" ]]; then
        echo "  Deleting Lambda code signing config: $csc_arn"
        aws lambda delete-code-signing-config --code-signing-config-arn "$csc_arn" --region "$REGION" 2>/dev/null || true
    fi
done

# --- SES email identities (registers domain/email senders)
# Match both FORMAE_PREFIX and the SES-specific 'formae-conformance' prefix used
# by the SES conformance fixtures.
echo "Cleaning SES test email identities..."
aws sesv2 list-email-identities --region "$REGION" --query "EmailIdentities[?starts_with(IdentityName, 'formae-conformance') || starts_with(IdentityName, '$FORMAE_PREFIX') || starts_with(IdentityName, '$SDK_PREFIX') || starts_with(IdentityName, '$TEST_PREFIX')].IdentityName" --output text 2>/dev/null | tr '\t' '\n' | while read -r ident; do
    if [[ -n "$ident" ]]; then
        echo "  Deleting SES email identity: $ident"
        aws sesv2 delete-email-identity --email-identity "$ident" --region "$REGION" 2>/dev/null || true
    fi
done

# --- SES configuration sets (also implicitly removes their event destinations)
echo "Cleaning SES test configuration sets..."
aws sesv2 list-configuration-sets --region "$REGION" --query "ConfigurationSets[?starts_with(@, '$FORMAE_PREFIX') || starts_with(@, '$SDK_PREFIX') || starts_with(@, '$TEST_PREFIX')]" --output text 2>/dev/null | tr '\t' '\n' | while read -r cs; do
    if [[ -n "$cs" ]]; then
        echo "  Deleting SES configuration set: $cs"
        aws sesv2 delete-configuration-set --configuration-set-name "$cs" --region "$REGION" 2>/dev/null || true
    fi
done

# ============================================================================
# CloudFront Resources (global)
# ============================================================================

# CloudFront Functions
echo "Cleaning CloudFront Functions..."
aws cloudfront list-functions --region "$REGION" \
    --query "FunctionList.Items[?starts_with(Name, 'formae-plugin-sdk-test-')].Name" \
    --output text 2>/dev/null | tr '\t' '\n' | while read -r fn_name; do
    [[ -z "$fn_name" || "$fn_name" == "None" ]] && continue
    echo "  Deleting CloudFront Function: $fn_name"
    etag=$(aws cloudfront describe-function --name "$fn_name" --stage DEVELOPMENT \
        --query 'ETag' --output text 2>/dev/null)
    [[ -z "$etag" || "$etag" == "None" ]] && continue
    aws cloudfront delete-function --name "$fn_name" --if-match "$etag" 2>/dev/null || true
done

# CloudFront KeyValueStores
echo "Cleaning CloudFront KeyValueStores..."
aws cloudfront list-key-value-stores --region "$REGION" \
    --query "KeyValueStoreList.Items[?starts_with(Name, 'formae-plugin-sdk-test-')].ARN" \
    --output text 2>/dev/null | tr '\t' '\n' | while read -r kvs_arn; do
    [[ -z "$kvs_arn" || "$kvs_arn" == "None" ]] && continue
    echo "  Deleting CloudFront KeyValueStore: $kvs_arn"
    etag=$(aws cloudfront describe-key-value-store --kvs-arn "$kvs_arn" \
        --query 'ETag' --output text 2>/dev/null)
    [[ -z "$etag" || "$etag" == "None" ]] && continue
    aws cloudfront delete-key-value-store --kvs-arn "$kvs_arn" --if-match "$etag" 2>/dev/null || true
done

# CloudFront Cache Policies
echo "Cleaning CloudFront CachePolicies..."
aws cloudfront list-cache-policies --type custom --region "$REGION" \
    --query "CachePolicyList.Items[?starts_with(CachePolicy.CachePolicyConfig.Name, 'formae-plugin-sdk-test-')].CachePolicy.Id" \
    --output text 2>/dev/null | tr '\t' '\n' | while read -r pol_id; do
    [[ -z "$pol_id" || "$pol_id" == "None" ]] && continue
    echo "  Deleting CloudFront CachePolicy: $pol_id"
    etag=$(aws cloudfront get-cache-policy --id "$pol_id" \
        --query 'ETag' --output text 2>/dev/null)
    [[ -z "$etag" || "$etag" == "None" ]] && continue
    aws cloudfront delete-cache-policy --id "$pol_id" --if-match "$etag" 2>/dev/null || true
done

# CloudFront Origin Request Policies
echo "Cleaning CloudFront OriginRequestPolicies..."
aws cloudfront list-origin-request-policies --type custom --region "$REGION" \
    --query "OriginRequestPolicyList.Items[?starts_with(OriginRequestPolicy.OriginRequestPolicyConfig.Name, 'formae-plugin-sdk-test-')].OriginRequestPolicy.Id" \
    --output text 2>/dev/null | tr '\t' '\n' | while read -r pol_id; do
    [[ -z "$pol_id" || "$pol_id" == "None" ]] && continue
    echo "  Deleting CloudFront OriginRequestPolicy: $pol_id"
    etag=$(aws cloudfront get-origin-request-policy --id "$pol_id" \
        --query 'ETag' --output text 2>/dev/null)
    [[ -z "$etag" || "$etag" == "None" ]] && continue
    aws cloudfront delete-origin-request-policy --id "$pol_id" --if-match "$etag" 2>/dev/null || true
done

# CloudFront Response Headers Policies
echo "Cleaning CloudFront ResponseHeadersPolicies..."
aws cloudfront list-response-headers-policies --type custom --region "$REGION" \
    --query "ResponseHeadersPolicyList.Items[?starts_with(ResponseHeadersPolicy.ResponseHeadersPolicyConfig.Name, 'formae-plugin-sdk-test-')].ResponseHeadersPolicy.Id" \
    --output text 2>/dev/null | tr '\t' '\n' | while read -r pol_id; do
    [[ -z "$pol_id" || "$pol_id" == "None" ]] && continue
    echo "  Deleting CloudFront ResponseHeadersPolicy: $pol_id"
    etag=$(aws cloudfront get-response-headers-policy --id "$pol_id" \
        --query 'ETag' --output text 2>/dev/null)
    [[ -z "$etag" || "$etag" == "None" ]] && continue
    aws cloudfront delete-response-headers-policy --id "$pol_id" --if-match "$etag" 2>/dev/null || true
done

# CloudFront Origin Access Controls
echo "Cleaning CloudFront OriginAccessControls..."
aws cloudfront list-origin-access-controls --region "$REGION" \
    --query "OriginAccessControlList.Items[?starts_with(Name, 'formae-plugin-sdk-test-')].Id" \
    --output text 2>/dev/null | tr '\t' '\n' | while read -r oac_id; do
    [[ -z "$oac_id" || "$oac_id" == "None" ]] && continue
    echo "  Deleting CloudFront OriginAccessControl: $oac_id"
    etag=$(aws cloudfront get-origin-access-control --id "$oac_id" \
        --query 'ETag' --output text 2>/dev/null)
    [[ -z "$etag" || "$etag" == "None" ]] && continue
    aws cloudfront delete-origin-access-control --id "$oac_id" --if-match "$etag" 2>/dev/null || true
done

# ACM Certificates (test prefix only — positive match guarantees prod certs
# like pkl.platform.engineering and hub.platform.engineering cannot match)
echo "Cleaning ACM test certificates..."
aws acm list-certificates --region "$REGION" \
    --certificate-statuses PENDING_VALIDATION ISSUED EXPIRED FAILED INACTIVE REVOKED VALIDATION_TIMED_OUT \
    --query "CertificateSummaryList[?starts_with(DomainName, 'formae-plugin-sdk-test-cert-')].CertificateArn" \
    --output text 2>/dev/null | tr '\t' '\n' | while read -r cert_arn; do
    [[ -z "$cert_arn" || "$cert_arn" == "None" ]] && continue
    echo "  Deleting ACM Certificate: $cert_arn"
    aws acm delete-certificate --certificate-arn "$cert_arn" --region "$REGION" 2>/dev/null || true
done

# Future-proofing: CloudFront Distributions cleanup would go here once
# conformance creates them. Skipped today — Distribution is discoverable=false,
# extractable=false and not in the conformance matrix.

echo ""
echo "=== Cleanup complete ==="
