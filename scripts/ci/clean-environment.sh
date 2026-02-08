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

echo "=== Cleaning AWS test resources in $REGION ==="
echo "Looking for resources with '$TEST_PREFIX' in name or tags..."
echo ""

# ============================================================================
# IAM Resources (global, no region)
# ============================================================================

# 1. Delete IAM instance profiles with test prefix (before roles)
echo "Cleaning IAM test instance profiles..."
aws iam list-instance-profiles --query "InstanceProfiles[?contains(InstanceProfileName, '$FORMAE_PREFIX') || contains(InstanceProfileName, '$TEST_PREFIX')].InstanceProfileName" --output text 2>/dev/null | tr '\t' '\n' | while read -r profile; do
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
aws iam list-roles --query "Roles[?contains(RoleName, '$FORMAE_PREFIX') || contains(RoleName, '$TEST_PREFIX')].RoleName" --output text 2>/dev/null | tr '\t' '\n' | while read -r role; do
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
aws iam list-users --query "Users[?contains(UserName, '$FORMAE_PREFIX') || contains(UserName, '$TEST_PREFIX')].UserName" --output text 2>/dev/null | tr '\t' '\n' | while read -r user; do
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
aws iam list-groups --query "Groups[?contains(GroupName, '$FORMAE_PREFIX') || contains(GroupName, '$TEST_PREFIX')].GroupName" --output text 2>/dev/null | tr '\t' '\n' | while read -r group; do
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
# S3 Resources (global)
# ============================================================================

# 8. Delete S3 buckets with test prefix
echo "Cleaning S3 test buckets..."
aws s3api list-buckets --query "Buckets[?contains(Name, '$FORMAE_PREFIX') || contains(Name, '$TEST_PREFIX')].Name" --output text 2>/dev/null | tr '\t' '\n' | while read -r bucket; do
    if [[ -n "$bucket" ]]; then
        echo "  Deleting S3 bucket: $bucket"
        aws s3 rm "s3://$bucket" --recursive --region "$REGION" 2>/dev/null || true
        aws s3api delete-bucket --bucket "$bucket" --region "$REGION" 2>/dev/null || true
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
        if [[ "$tags" == *"$FORMAE_PREFIX"* || "$tags" == *"$TEST_PREFIX"* ]]; then
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

# 21. Delete EC2 transit gateways with test prefix (by Name tag)
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

# 23. Delete EC2 VPCs with test prefix (by Name tag) - after all VPC dependents
echo "Cleaning EC2 test VPCs..."
aws ec2 describe-vpcs --region "$REGION" \
    --filters "Name=tag:Name,Values=*$FORMAE_PREFIX*" \
    --query "Vpcs[].VpcId" --output text 2>/dev/null | tr '\t' '\n' | while read -r vpc_id; do
    if [[ -n "$vpc_id" ]]; then
        echo "  Deleting EC2 VPC: $vpc_id"
        aws ec2 delete-vpc --vpc-id "$vpc_id" --region "$REGION" 2>/dev/null || true
    fi
done

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

# ============================================================================
# ECS Resources (regional)
# ============================================================================

# 29. Delete ECS clusters with test prefix
echo "Cleaning ECS test clusters..."
aws ecs list-clusters --region "$REGION" --query "clusterArns[]" --output text 2>/dev/null | tr '\t' '\n' | while read -r cluster_arn; do
    if [[ -n "$cluster_arn" && "$cluster_arn" == *"$FORMAE_PREFIX"* ]]; then
        echo "  Deleting ECS cluster: $cluster_arn"
        aws ecs delete-cluster --cluster "$cluster_arn" --region "$REGION" 2>/dev/null || true
    fi
done

# 30. Deregister ECS task definitions with test prefix
echo "Cleaning ECS test task definitions..."
aws ecs list-task-definition-families --region "$REGION" --family-prefix "$FORMAE_PREFIX" --status ACTIVE \
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

echo ""
echo "=== Cleanup complete ==="
