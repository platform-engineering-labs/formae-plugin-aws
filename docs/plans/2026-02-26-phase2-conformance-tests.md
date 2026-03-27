# Phase 2 Conformance Tests Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add conformance tests (base + update + replace) for all Phase 2 resources (~27 resource types with single-parent dependencies).

**Architecture:** Each test follows the established pattern from Phase 1 — a PKL file in `testdata/` with inline parent resources, using `FORMAE_TEST_RUN_ID` for uniqueness and `plugin-sdk-test` prefix for cleanup. Update variants modify non-createOnly fields; replace variants modify createOnly fields. The cleanup script `scripts/ci/clean-environment.sh` must handle all new resource types.

**Tech Stack:** Pkl (schema language), AWS CloudControl API, formae conformance test framework.

**Reference files:**
- Existing parent-child pattern: `testdata/route53-recordset.pkl`
- Existing standalone pattern: `testdata/s3-bucket.pkl`
- Update variant: `testdata/s3-bucket-update.pkl`
- Replace variant: `testdata/s3-bucket-replace.pkl`
- Cleanup script: `scripts/ci/clean-environment.sh`

**Deferred resources (missing schema or complex dependencies):**
- `KMS::Alias` — schema `schema/pkl/kms/alias.pkl` doesn't exist yet
- `Route53::DNSSEC` + `Route53::KeySigningKey` — require KMS key with specific DNSSEC policy, complex chain
- `Route53::RecordSetGroup` — not discoverable, essentially a batch RecordSet
- `DynamoDB::GlobalTable` — requires multi-region replicas
- `RDS::EventSubscription` — requires SNS topic (cross-service)
- `Lambda::LayerVersion` — all fields createOnly, needs S3 deployment package

**Resources to implement: 21 resource types**

---

## Common patterns

### PKL file structure (base)
```pkl
amends "@formae/forma.pkl"
import "@formae/formae.pkl"
import "@aws/aws.pkl"
import "@aws/<service>/<resource>.pkl"

local testRunID = read("env:FORMAE_TEST_RUN_ID")
local stackName = "plugin-sdk-test-<service>-<resource>-\(testRunID)"

forma {
  new formae.Stack { label = stackName; description = "..." }
  new formae.Target { label = "aws-target"; config = new aws.Config { region = "us-east-1" } }
  // parent resource (if needed)
  // resource under test
}
```

### Update variant
Same as base but modifies non-createOnly fields (description, tags, policy document, etc). Keep parent and createOnly fields identical to base.

### Replace variant
Same as base but modifies a createOnly field (name, parent reference, etc). This triggers delete+create. Only possible when a meaningful createOnly field exists.

### Cleanup script pattern
Each resource type gets a cleanup block that finds resources by the `plugin-sdk-test` / `formae-plugin-sdk-test` prefix and deletes them. Child resources must be cleaned before parents.

---

## Task 1: Create feature branch

**Step 1:** Create and switch to feature branch
```bash
git checkout -b feat/phase2-conformance-tests
```

---

## Task 2: IAM RolePolicy (parent: Role)

**Files:**
- Create: `testdata/iam-rolepolicy.pkl`
- Create: `testdata/iam-rolepolicy-update.pkl`
- Create: `testdata/iam-rolepolicy-replace.pkl`

Schema: `schema/pkl/iam/rolepolicy.pkl`
- Fields: `roleName` (createOnly, Resolvable), `policyName` (createOnly), `policyDocument`
- Parent: `AWS::IAM::Role`
- Identifier: `PolicyName`

**Step 1: Write base PKL**
```pkl
amends "@formae/forma.pkl"
import "@formae/formae.pkl"
import "@aws/aws.pkl"
import "@aws/iam/role.pkl"
import "@aws/iam/rolepolicy.pkl"

local testRunID = read("env:FORMAE_TEST_RUN_ID")
local stackName = "plugin-sdk-test-iam-rolepolicy-\(testRunID)"

local testRole = new role.Role {
  label = "test-role"
  roleName = "formae-plugin-sdk-test-rp-role-\(testRunID)"
  assumeRolePolicyDocument {
    ["Version"] = "2012-10-17"
    ["Statement"] {
      new {
        ["Effect"] = "Allow"
        ["Principal"] { ["Service"] = "lambda.amazonaws.com" }
        ["Action"] = "sts:AssumeRole"
      }
    }
  }
}

forma {
  new formae.Stack { label = stackName; description = "Plugin SDK test for IAM RolePolicy" }
  new formae.Target { label = "aws-target"; config = new aws.Config { region = "us-east-1" } }
  testRole
  new rolepolicy.RolePolicy {
    label = "plugin-sdk-test-rolepolicy"
    roleName = testRole.res.roleName
    policyName = "formae-plugin-sdk-test-rp-\(testRunID)"
    policyDocument {
      ["Version"] = "2012-10-17"
      ["Statement"] {
        new {
          ["Effect"] = "Allow"
          ["Action"] = "logs:CreateLogGroup"
          ["Resource"] = "*"
        }
      }
    }
  }
}
```

**Step 2: Write update PKL** — change policyDocument (add another statement)
```pkl
// Same structure, but policyDocument has an additional statement:
policyDocument {
  ["Version"] = "2012-10-17"
  ["Statement"] {
    new {
      ["Effect"] = "Allow"
      ["Action"] = "logs:CreateLogGroup"
      ["Resource"] = "*"
    }
    new {
      ["Effect"] = "Allow"
      ["Action"] = "logs:PutLogEvents"
      ["Resource"] = "*"
    }
  }
}
```

**Step 3: Write replace PKL** — change policyName (createOnly)
```pkl
// Same structure, but:
policyName = "formae-plugin-sdk-test-rp-replaced-\(testRunID)"
```

---

## Task 3: IAM GroupPolicy (parent: Group)

**Files:**
- Create: `testdata/iam-grouppolicy.pkl`
- Create: `testdata/iam-grouppolicy-update.pkl`
- Create: `testdata/iam-grouppolicy-replace.pkl`

Schema: `schema/pkl/iam/grouppolicy.pkl`
- Fields: `groupName` (createOnly), `policyName` (createOnly), `policyDocument`

**Base:** Create group inline, attach policy with simple allow statement.
**Update:** Modify policyDocument.
**Replace:** Change policyName (createOnly).

---

## Task 4: IAM UserPolicy (parent: User)

**Files:**
- Create: `testdata/iam-userpolicy.pkl`
- Create: `testdata/iam-userpolicy-update.pkl`
- Create: `testdata/iam-userpolicy-replace.pkl`

Schema: `schema/pkl/iam/userpolicy.pkl`
- Fields: `userName` (createOnly), `policyName` (createOnly), `policyDocument`

**Base:** Create user inline, attach policy.
**Update:** Modify policyDocument.
**Replace:** Change policyName (createOnly).

---

## Task 5: IAM AccessKey (parent: User)

**Files:**
- Create: `testdata/iam-accesskey.pkl`
- Create: `testdata/iam-accesskey-update.pkl`
- No replace variant (userName is only createOnly field, changing it doesn't make sense as a "replace")

Schema: `schema/pkl/iam/accesskey.pkl`
- Fields: `userName` (createOnly), `status`, `serial` (createOnly)

**Base:** Create user inline, create access key.
**Update:** Change `status` from default to "Inactive".

---

## Task 6: IAM Policy (standalone)

**Files:**
- Create: `testdata/iam-policy.pkl`
- Create: `testdata/iam-policy-update.pkl`
- Create: `testdata/iam-policy-replace.pkl`

Schema: `schema/pkl/iam/policy.pkl`
- Fields: `policyName`, `policyDocument`, `groups`, `roles`, `users` — none are createOnly

**Base:** Create standalone managed policy with simple statement.
**Update:** Modify policyDocument (add another action).
**Replace:** Change policyName. Note: policyName is NOT createOnly in the schema, so replace may not trigger a new NativeID. If all fields are updatable, skip replace variant.

---

## Task 7: IAM InstanceProfile (needs Role)

**Files:**
- Create: `testdata/iam-instanceprofile.pkl`
- Create: `testdata/iam-instanceprofile-update.pkl`
- Create: `testdata/iam-instanceprofile-replace.pkl`

Schema: `schema/pkl/iam/instanceprofile.pkl`
- Fields: `instanceProfileName` (createOnly), `path` (createOnly), `roles` (Listing, Resolvable)

**Base:** Create role inline, create instance profile referencing it.
**Update:** Not possible — only `roles` is updatable but changing it requires another role. Include a second role and swap.
**Replace:** Change instanceProfileName (createOnly).

---

## Task 8: IAM SAMLProvider

**Files:**
- Create: `testdata/iam-samlprovider.pkl`
- Create: `testdata/iam-samlprovider-update.pkl`
- Create: `testdata/iam-samlprovider-replace.pkl`

Schema: `schema/pkl/iam/samlprovider.pkl`
- Fields: `name` (createOnly), `samlMetadataDocument`, `tags`

**Base:** Create with minimal SAML metadata XML.
**Update:** Change samlMetadataDocument or add tags.
**Replace:** Change name (createOnly).

Note: needs valid SAML metadata XML. Use a minimal self-signed metadata document.

---

## Task 9: SQS QueuePolicy (parent: Queue)

**Files:**
- Create: `testdata/sqs-queuepolicy.pkl`
- Create: `testdata/sqs-queuepolicy-update.pkl`
- No replace variant (no createOnly fields)

Schema: `schema/pkl/sqs/queuepolicy.pkl`
- Fields: `queues` (Listing, Resolvable), `policyDocument` — no createOnly fields

**Base:** Create queue inline, attach policy allowing SendMessage.
**Update:** Modify policyDocument (add ReceiveMessage action).

---

## Task 10: SQS QueueInlinePolicy (parent: Queue)

**Files:**
- Create: `testdata/sqs-queueinlinepolicy.pkl`
- Create: `testdata/sqs-queueinlinepolicy-update.pkl`
- Create: `testdata/sqs-queueinlinepolicy-replace.pkl`

Schema: `schema/pkl/sqs/queueinlinepolicy.pkl`
- Fields: `queue` (createOnly, Resolvable), `policyDocument`

**Base:** Create queue inline, attach inline policy.
**Update:** Modify policyDocument.
**Replace:** Create a second queue and change the `queue` reference (createOnly).

---

## Task 11: S3 BucketPolicy (parent: Bucket)

**Files:**
- Create: `testdata/s3-bucketpolicy.pkl`
- Create: `testdata/s3-bucketpolicy-update.pkl`
- Create: `testdata/s3-bucketpolicy-replace.pkl`

Schema: `schema/pkl/s3/bucketpolicy.pkl`
- Fields: `bucket` (createOnly, Resolvable), `policyDocument`

**Base:** Create bucket inline, attach bucket policy allowing GetObject.
**Update:** Modify policyDocument (add PutObject).
**Replace:** Create a second bucket and change the `bucket` reference.

---

## Task 12: S3 AccessPoint (parent: Bucket)

**Files:**
- Create: `testdata/s3-accesspoint.pkl`
- Create: `testdata/s3-accesspoint-update.pkl`
- Create: `testdata/s3-accesspoint-replace.pkl`

Schema: `schema/pkl/s3/s3accesspoint.pkl`
- Fields: `bucket` (createOnly), `name` (createOnly), `policy`, `publicAccessBlockConfiguration`

**Base:** Create bucket inline, create access point.
**Update:** Modify `policy` or `publicAccessBlockConfiguration`.
**Replace:** Change `name` (createOnly).

---

## Task 13: S3 StorageLensGroup (standalone)

**Files:**
- Create: `testdata/s3-storagelensgroup.pkl`
- Create: `testdata/s3-storagelensgroup-update.pkl`
- Create: `testdata/s3-storagelensgroup-replace.pkl`

Schema: `schema/pkl/s3/storagelensgroup.pkl`
- Fields: `name` (createOnly), `filter` (required), `tags`

**Base:** Create with simple prefix filter.
**Update:** Modify filter (change prefix) or add tags.
**Replace:** Change name (createOnly).

---

## Task 14: SecretsManager ResourcePolicy (parent: Secret)

**Files:**
- Create: `testdata/secretsmanager-resourcepolicy.pkl`
- Create: `testdata/secretsmanager-resourcepolicy-update.pkl`
- Create: `testdata/secretsmanager-resourcepolicy-replace.pkl`

Schema: `schema/pkl/secretsmanager/resourcepolicy.pkl`
- Fields: `secretId` (createOnly), `resourcePolicy`, `blockPublicPolicy` (writeOnly)

**Base:** Create secret inline, attach resource policy.
**Update:** Modify resourcePolicy.
**Replace:** Create second secret and change secretId.

---

## Task 15: ECR PublicRepository (standalone)

**Files:**
- Create: `testdata/ecr-publicrepository.pkl`
- Create: `testdata/ecr-publicrepository-update.pkl`
- No replace variant (no createOnly fields)

Schema: `schema/pkl/ecr/publicrepository.pkl`
- Fields: `repositoryName`, `repositoryCatalogData`, `tags` — no createOnly fields

**Base:** Create public repository.
**Update:** Add/modify repositoryCatalogData or tags.

Note: ECR Public Repository requires opt-in to the public registry. May fail in some accounts.

---

## Task 16: ECR PullThroughCacheRule (standalone)

**Files:**
- Create: `testdata/ecr-pullthroughcacherule.pkl`
- No update variant (only createOnly and writeOnly fields)
- Create: `testdata/ecr-pullthroughcacherule-replace.pkl`

Schema: `schema/pkl/ecr/pullthroughcacherule.pkl`
- Fields: `ecrRepositoryPrefix` (createOnly), `upstreamRegistry` (createOnly+writeOnly), `upstreamRegistryUrl` (createOnly), `credentialArn` (writeOnly)

**Base:** Create rule for ECR Public upstream.
**Replace:** Change ecrRepositoryPrefix.

Note: No update variant — all non-writeOnly fields are createOnly.

---

## Task 17: ECR RepositoryCreationTemplate (standalone)

**Files:**
- Create: `testdata/ecr-repositorycreationtemplate.pkl`
- Create: `testdata/ecr-repositorycreationtemplate-update.pkl`
- Create: `testdata/ecr-repositorycreationtemplate-replace.pkl`

Schema: `schema/pkl/ecr/repositorycreationtemplate.pkl`
- Fields: `prefix` (createOnly), `appliedFor`, `description`, `imageTagMutability`, etc.

**Base:** Create template with prefix and REPLICATION applied.
**Update:** Change description or imageTagMutability.
**Replace:** Change prefix (createOnly).

---

## Task 18: Lambda CodeSigningConfig (standalone)

**Files:**
- Create: `testdata/lambda-codesigningconfig.pkl`
- Create: `testdata/lambda-codesigningconfig-update.pkl`
- No replace variant (no createOnly fields)

Schema: `schema/pkl/lambda/codesigningconfig.pkl`
- Fields: `allowedPublishers` (required), `codeSigningPolicies`, `description`, `tags`

**Base:** Create with allowedPublishers pointing to a signing profile ARN.
**Update:** Change description or codeSigningPolicies.

Note: Requires an AWS Signer signing profile. May need to create one or use a known ARN. If not feasible, defer this resource.

---

## Task 19: ElasticBeanstalk ApplicationVersion (parent: Application)

**Files:**
- Create: `testdata/elasticbeanstalk-applicationversion.pkl`
- Create: `testdata/elasticbeanstalk-applicationversion-update.pkl`
- No replace variant (applicationName + sourceBundle are createOnly, but changing them creates a fundamentally different resource)

Schema: `schema/pkl/elasticbeanstalk/applicationversion.pkl`
- Fields: `applicationName` (createOnly, Resolvable), `description`, `sourceBundle` (createOnly)

**Base:** Create EB application inline, create version with S3 source bundle.
**Update:** Change description.

Note: Needs an S3 bucket with a valid EB source bundle (zip file). Create bucket + upload in the same forma, or use a pre-existing bundle.

---

## Task 20: ElasticBeanstalk ConfigurationTemplate (parent: Application)

**Files:**
- Create: `testdata/elasticbeanstalk-configurationtemplate.pkl`
- Create: `testdata/elasticbeanstalk-configurationtemplate-update.pkl`
- No replace variant (complex createOnly fields, not practical)

Schema: `schema/pkl/elasticbeanstalk/configurationtemplate.pkl`
- Fields: `applicationName` (createOnly), `description`, `solutionStackName` (createOnly), `optionSettings`, `platformArn` (createOnly)

**Base:** Create EB application inline, create config template with a solutionStackName.
**Update:** Change description or optionSettings.

---

## Task 21: RDS GlobalCluster (standalone)

**Files:**
- Create: `testdata/rds-globalcluster.pkl`
- Create: `testdata/rds-globalcluster-update.pkl`
- Create: `testdata/rds-globalcluster-replace.pkl`

Schema: `schema/pkl/rds/globalcluster.pkl`
- Fields: `globalClusterIdentifier` (createOnly), `engine` (createOnly), `engineVersion`, `deletionProtection`, `storageEncrypted` (createOnly), `tags`

**Base:** Create Aurora MySQL global cluster.
**Update:** Change deletionProtection or engineVersion.
**Replace:** Change globalClusterIdentifier (createOnly).

---

## Task 22: RDS OptionGroup (standalone)

**Files:**
- Create: `testdata/rds-optiongroup.pkl`
- Create: `testdata/rds-optiongroup-update.pkl`
- Create: `testdata/rds-optiongroup-replace.pkl`

Schema: `schema/pkl/rds/optiongroup.pkl`
- Fields: `optionGroupName` (createOnly), `optionGroupDescription` (createOnly), `engineName` (createOnly), `majorEngineVersion` (createOnly), `optionConfigurations`, `tags`

**Base:** Create option group for mysql 8.0.
**Update:** Add/modify tags.
**Replace:** Change optionGroupName (createOnly).

---

## Task 23: EC2 TransitGatewayRouteTable (parent: TransitGateway)

**Files:**
- Create: `testdata/ec2-transitgatewayroutetable.pkl`
- Create: `testdata/ec2-transitgatewayroutetable-update.pkl`
- No replace variant (only transitGatewayId is createOnly, changing parent doesn't make sense)

Schema: `schema/pkl/ec2/transitgatewayroutetable.pkl`
- Fields: `transitGatewayId` (createOnly, Resolvable), `tags`

**Base:** Create transit gateway inline, create route table.
**Update:** Add/modify tags.

Note: Transit gateway creation takes 1-2 minutes.

---

## Task 24: EC2 IPAMPool (parent: IPAM)

**Files:**
- Create: `testdata/ec2-ipampool.pkl`
- Create: `testdata/ec2-ipampool-update.pkl`
- No replace variant (createOnly fields define the pool's fundamental nature)

Schema: `schema/pkl/ec2/ipampool.pkl`
- Fields: `ipamScopeId` (createOnly, Resolvable), `addressFamily` (createOnly), `description`, `tags`, etc.

**Base:** Create IPAM inline, create pool using IPAM's private scope.
**Update:** Change description or add tags.

Note: Need to reference the IPAM's default private scope. The IPAM resource returns `PrivateDefaultScopeId` as a read-only property. May need to use a resolvable.

---

## Task 25: EC2 FlowLog (parent: VPC)

**Files:**
- Create: `testdata/ec2-flowlog.pkl`
- No update variant (all fields except tags are createOnly)
- No replace variant (would need to change the VPC which is the parent)

Schema: `schema/pkl/ec2/flowlog.pkl`
- Fields: all createOnly except `tags`. `resourceId` (Resolvable), `resourceType`, `trafficType`, `logDestinationType`, etc.

**Base:** Create VPC inline, create flow log to CloudWatch Logs.
**Update:** Add/modify tags only.

Note: Logging to CloudWatch requires a log group and IAM role. May be simpler to log to S3 (create bucket inline) or use the `cloud-watch-logs` destination with a log group.

---

## Task 26: Update cleanup script

**File:** Modify: `scripts/ci/clean-environment.sh`

Add cleanup entries for all new resource types. Order matters — clean children before parents.

New cleanup sections needed:
- IAM inline policies (role/group/user) — already handled by existing role/group/user cleanup
- IAM access keys — already handled by existing user cleanup
- IAM standalone policies — already handled by existing managed policy cleanup (item 5)
- SQS queue policies — no separate cleanup needed (deleted with queue)
- S3 bucket policies — no separate cleanup needed (deleted with bucket)
- S3 access points — NEW: delete access points before buckets
- S3 StorageLensGroups — NEW
- SecretsManager resource policies — no separate cleanup (deleted with secret)
- ECR public repositories — NEW
- ECR pull-through cache rules — NEW
- ECR repository creation templates — NEW
- Lambda code signing configs — NEW (if included)
- ElasticBeanstalk application versions — already handled by app delete (cascade)
- ElasticBeanstalk configuration templates — already handled by app delete (cascade)
- RDS global clusters — NEW
- RDS option groups — NEW
- EC2 transit gateway route tables — NEW: delete before transit gateways
- EC2 IPAM pools — already handled by IPAM --cascade delete
- EC2 flow logs — NEW: delete before VPCs

---

## Task 27: Validate PKL files locally

**Step 1:** Run `pkl eval` on all new testdata files to check syntax
```bash
for f in testdata/*.pkl; do
  echo "Evaluating $f..."
  pkl eval "$f" --project-dir testdata/ 2>&1 | head -5 || echo "FAILED: $f"
done
```

Note: eval will fail for env:FORMAE_TEST_RUN_ID but should not fail on syntax.

---

## Task 28: Push and trigger CI

**Step 1:** Commit all new testdata files and cleanup script changes
```bash
git add testdata/ scripts/ci/clean-environment.sh
git commit -m "feat: add Phase 2 conformance tests (~21 resource types)"
```

**Step 2:** Push feature branch
```bash
git push -u origin feat/phase2-conformance-tests
```

**Step 3:** Monitor CI results — expect some failures on first run

---

## Execution strategy

Tasks 2-25 (writing PKL files) are independent and can be parallelized across agents grouped by service:
- **Agent A:** IAM resources (Tasks 2-8)
- **Agent B:** SQS + S3 + SecretsManager (Tasks 9-14)
- **Agent C:** ECR + Lambda + EB (Tasks 15-20)
- **Agent D:** RDS + EC2 (Tasks 21-25)

Task 26 (cleanup script) depends on knowing all resource types but can be done in parallel with the above.
Task 27 (validation) depends on all PKL files being written.
Task 28 (push) depends on everything.
