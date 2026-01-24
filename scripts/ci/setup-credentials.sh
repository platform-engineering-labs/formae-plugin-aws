#!/bin/bash
# © 2025 Platform Engineering Labs Inc.
# SPDX-License-Identifier: FSL-1.1-ALv2
#
# Setup AWS Credentials Hook
# ==========================
# This script verifies that AWS credentials are properly configured
# before running conformance tests.
#
# For local development:
#   - Set AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY environment variables
#   - Or configure AWS_PROFILE to use a named profile
#
# For CI (GitHub Actions):
#   - Use OIDC with aws-actions/configure-aws-credentials
#   - Or set AWS_ACCESS_KEY_ID/AWS_SECRET_ACCESS_KEY secrets

set -euo pipefail

echo "Verifying AWS credentials..."
echo ""

# Check for credentials - one of these must be set
if [[ -z "${AWS_ACCESS_KEY_ID:-}" && -z "${AWS_SECRET_ACCESS_KEY:-}" && -z "${AWS_PROFILE:-}" && -z "${AWS_ROLE_ARN:-}" ]]; then
    echo "ERROR: No AWS credentials configured"
    echo ""
    echo "Set one of the following:"
    echo "  - AWS_ACCESS_KEY_ID + AWS_SECRET_ACCESS_KEY (+ AWS_SESSION_TOKEN for temp creds)"
    echo "  - AWS_PROFILE (for named profile)"
    echo "  - AWS_ROLE_ARN (for OIDC/AssumeRole in CI)"
    echo ""
    echo "For local development, you can also run:"
    echo "  aws configure"
    exit 1
fi

# Check for region
if [[ -z "${AWS_REGION:-}" && -z "${AWS_DEFAULT_REGION:-}" ]]; then
    echo "ERROR: AWS_REGION or AWS_DEFAULT_REGION must be set"
    echo ""
    echo "Set the region:"
    echo "  export AWS_REGION=us-east-1"
    exit 1
fi

# Display configured credentials (without secrets)
echo "AWS credentials configured:"
echo "  Region: ${AWS_REGION:-${AWS_DEFAULT_REGION:-not set}}"

if [[ -n "${AWS_PROFILE:-}" ]]; then
    echo "  Profile: $AWS_PROFILE"
elif [[ -n "${AWS_ROLE_ARN:-}" ]]; then
    echo "  Role ARN: $AWS_ROLE_ARN"
elif [[ -n "${AWS_ACCESS_KEY_ID:-}" ]]; then
    echo "  Access Key ID: ${AWS_ACCESS_KEY_ID:0:4}..."
fi

# Verify credentials work by calling STS
echo ""
echo "Verifying credentials with STS..."
if ! aws sts get-caller-identity > /dev/null 2>&1; then
    echo "ERROR: AWS credentials are invalid or expired"
    echo "Please check your credentials and try again."
    exit 1
fi

IDENTITY=$(aws sts get-caller-identity --query 'Arn' --output text)
echo "Authenticated as: $IDENTITY"
echo ""
echo "AWS credentials verified successfully!"
