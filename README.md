# AWS Plugin for Formae

[![CI](https://github.com/platform-engineering-labs/formae-plugin-aws/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/platform-engineering-labs/formae-plugin-aws/actions/workflows/ci.yml)
[![Nightly](https://github.com/platform-engineering-labs/formae-plugin-aws/actions/workflows/nightly.yml/badge.svg?branch=main)](https://github.com/platform-engineering-labs/formae-plugin-aws/actions/workflows/nightly.yml)

AWS resource plugin for
[formae](https://github.com/platform-engineering-labs/formae). This plugin
enables Formae to manage AWS resources using the [AWS Cloud Control
API](https://docs.aws.amazon.com/cloudcontrolapi/latest/userguide/what-is-cloudcontrolapi.html).

## Supported Resources

This plugin supports **246 AWS resource types** across 28 services via the
CloudControl API:

| Service | Resources | Examples |
|---------|-----------|----------|
| EC2 | 96 | VPC, Subnet, SecurityGroup, Instance, NATGateway, InternetGateway |
| RDS | 18 | DBInstance, DBCluster, DBSubnetGroup, OptionGroup |
| IAM | 16 | Role, Policy, User, Group, InstanceProfile, OIDCProvider |
| S3 | 11 | Bucket, BucketPolicy, AccessPoint |
| Lambda | 10 | Function, LayerVersion, Permission, EventSourceMapping |
| API Gateway | 8 | RestApi, Resource, Method, Deployment, Stage |
| ECS | 8 | Cluster, Service, TaskDefinition, CapacityProvider |
| CloudFront | 7 | Distribution |
| EKS | 7 | Cluster, NodeGroup |
| ELBv2 | 7 | LoadBalancer, TargetGroup, Listener, ListenerRule |
| Route53 | 7 | HostedZone, RecordSet, HealthCheck |
| ECR | 6 | Repository, RegistryPolicy, ReplicationConfiguration |
| App Runner | 5 | Service, VpcConnector, AutoScalingConfiguration |
| Elastic Beanstalk | 4 | Application, Environment, ConfigurationTemplate |
| Network Firewall | 4 | Firewall, FirewallPolicy, RuleGroup |
| SES | 4 | EmailIdentity, ConfigurationSet, ReceiptRule |
| SageMaker | 4 | Domain, UserProfile, Endpoint |
| Secrets Manager | 4 | Secret, ResourcePolicy, RotationSchedule |
| EFS | 3 | FileSystem, MountTarget, AccessPoint |
| EventBridge | 3 | Rule, EventBus, Connection |
| SQS | 3 | Queue, QueuePolicy |
| CodeBuild | 2 | Project, ImageBuild |
| DynamoDB | 2 | Table, GlobalTable |
| KMS | 2 | Key, Alias |
| Service Discovery | 2 | PrivateDnsNamespace, Service |
| Certificate Manager | 1 | Certificate |
| CloudTrail | 1 | Trail |
| Logs | 1 | LogGroup |

See [`schema/pkl/`](schema/pkl/) for the complete list of supported resource
types.

## Configuration

### Target Configuration

Configure an AWS target in your Forma file:

```pkl
import "@formae/formae.pkl"
import "@aws/aws.pkl"

target: formae.Target = new formae.Target {
  label = "aws-target"
  config = new aws.Config {
    region = "us-east-1"
    // Optional: specify a named profile
    // profile = "my-profile"
  }
}
```

### Credentials

The plugin uses the standard AWS credential chain. Configure credentials using
one of:

**Environment Variables:**

```bash
export AWS_ACCESS_KEY_ID="your-access-key"
export AWS_SECRET_ACCESS_KEY="your-secret-key"
export AWS_REGION="us-east-1"

# For temporary credentials (e.g., from STS AssumeRole)
export AWS_SESSION_TOKEN="your-session-token"
```

**Named Profile:**

```bash
# Use a profile from ~/.aws/credentials
export AWS_PROFILE="my-profile"
```

**IAM Instance Profile / ECS Task Role:** When running on EC2 or ECS,
credentials are automatically retrieved from the instance metadata service.

**OIDC (for CI/CD):** See `.github/workflows/ci.yml` for an example using GitHub
Actions OIDC with `aws-actions/configure-aws-credentials`.

## Examples

See the [examples/](examples/) directory for usage examples.

```bash
# Evaluate an example
formae eval examples/complete/lifeline/basic_infrastructure.pkl

# Apply resources
formae apply --mode reconcile --watch examples/complete/lifeline/basic_infrastructure.pkl
```

## License

This plugin is licensed under the [Functional Source License, Version 1.1, ALv2
Future License (FSL-1.1-ALv2)](LICENSE).

Copyright 2026 Platform Engineering Labs Inc.
