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

echo "=== Cleaning AWS test resources in $REGION ==="
echo "Looking for resources with '$TEST_PREFIX' in name or tags..."
echo ""

# 1. Delete S3 buckets with test prefix
echo "Cleaning S3 test buckets..."
aws s3api list-buckets --query "Buckets[?contains(Name, '$TEST_PREFIX')].Name" --output text 2>/dev/null | tr '\t' '\n' | while read -r bucket; do
    if [[ -n "$bucket" ]]; then
        echo "  Deleting S3 bucket: $bucket"
        aws s3 rm "s3://$bucket" --recursive --region "$REGION" 2>/dev/null || true
        aws s3api delete-bucket --bucket "$bucket" --region "$REGION" 2>/dev/null || true
    fi
done

# 2. Delete IAM roles with test prefix
echo "Cleaning IAM test roles..."
aws iam list-roles --query "Roles[?contains(RoleName, '$TEST_PREFIX')].RoleName" --output text 2>/dev/null | tr '\t' '\n' | while read -r role; do
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
        # Delete instance profiles
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
aws iam list-users --query "Users[?contains(UserName, '$TEST_PREFIX')].UserName" --output text 2>/dev/null | tr '\t' '\n' | while read -r user; do
    if [[ -n "$user" ]]; then
        echo "  Deleting IAM user: $user"
        # Delete access keys
        aws iam list-access-keys --user-name "$user" --query "AccessKeyMetadata[].AccessKeyId" --output text 2>/dev/null | tr '\t' '\n' | while read -r key; do
            [[ -n "$key" ]] && aws iam delete-access-key --user-name "$user" --access-key-id "$key" 2>/dev/null || true
        done
        # Detach policies
        aws iam list-attached-user-policies --user-name "$user" --query "AttachedPolicies[].PolicyArn" --output text 2>/dev/null | tr '\t' '\n' | while read -r policy; do
            [[ -n "$policy" ]] && aws iam detach-user-policy --user-name "$user" --policy-arn "$policy" 2>/dev/null || true
        done
        aws iam delete-user --user-name "$user" 2>/dev/null || true
    fi
done

# 4. Delete Route53 hosted zones with test prefix
echo "Cleaning Route53 test hosted zones..."
aws route53 list-hosted-zones --query "HostedZones[?contains(Name, '$TEST_PREFIX')].Id" --output text 2>/dev/null | tr '\t' '\n' | while read -r zone; do
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

# 5. Schedule KMS keys with test alias for deletion
echo "Scheduling KMS test keys for deletion..."
aws kms list-aliases --query "Aliases[?contains(AliasName, '$TEST_PREFIX')].TargetKeyId" --output text 2>/dev/null | tr '\t' '\n' | while read -r key; do
    if [[ -n "$key" ]]; then
        echo "  Scheduling KMS key for deletion: $key"
        aws kms schedule-key-deletion --key-id "$key" --pending-window-in-days 7 --region "$REGION" 2>/dev/null || true
    fi
done

# 6. Delete CloudWatch Log Groups with test prefix
echo "Cleaning CloudWatch test log groups..."
aws logs describe-log-groups --region "$REGION" \
    --query "logGroups[?contains(logGroupName, '$TEST_PREFIX')].logGroupName" --output text 2>/dev/null | tr '\t' '\n' | while read -r lg; do
    if [[ -n "$lg" ]]; then
        echo "  Deleting log group: $lg"
        aws logs delete-log-group --log-group-name "$lg" --region "$REGION" 2>/dev/null || true
    fi
done

# 7. Delete Secrets Manager secrets with test prefix
echo "Cleaning Secrets Manager test secrets..."
aws secretsmanager list-secrets --region "$REGION" \
    --query "SecretList[?contains(Name, '$TEST_PREFIX')].Name" --output text 2>/dev/null | tr '\t' '\n' | while read -r secret; do
    if [[ -n "$secret" ]]; then
        echo "  Deleting secret: $secret"
        aws secretsmanager delete-secret --secret-id "$secret" --force-delete-without-recovery --region "$REGION" 2>/dev/null || true
    fi
done

# 8. Delete SQS queues with test prefix
echo "Cleaning SQS test queues..."
aws sqs list-queues --region "$REGION" --queue-name-prefix "$TEST_PREFIX" --query "QueueUrls[]" --output text 2>/dev/null | tr '\t' '\n' | while read -r queue; do
    if [[ -n "$queue" ]]; then
        echo "  Deleting SQS queue: $queue"
        aws sqs delete-queue --queue-url "$queue" --region "$REGION" 2>/dev/null || true
    fi
done

# 9. Delete DynamoDB tables with test prefix
echo "Cleaning DynamoDB test tables..."
aws dynamodb list-tables --region "$REGION" --query "TableNames[?contains(@, '$TEST_PREFIX')]" --output text 2>/dev/null | tr '\t' '\n' | while read -r table; do
    if [[ -n "$table" ]]; then
        echo "  Deleting DynamoDB table: $table"
        aws dynamodb delete-table --table-name "$table" --region "$REGION" 2>/dev/null || true
    fi
done

# 10. Delete ECR repositories with test prefix
echo "Cleaning ECR test repositories..."
aws ecr describe-repositories --region "$REGION" \
    --query "repositories[?contains(repositoryName, '$TEST_PREFIX')].repositoryName" --output text 2>/dev/null | tr '\t' '\n' | while read -r repo; do
    if [[ -n "$repo" ]]; then
        echo "  Deleting ECR repository: $repo"
        aws ecr delete-repository --repository-name "$repo" --region "$REGION" --force 2>/dev/null || true
    fi
done

echo ""
echo "=== Cleanup complete ==="
