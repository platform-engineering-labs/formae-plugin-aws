# AWS Plugin for Formae

AWS CloudControl resource plugin for [Formae](https://github.com/platform-engineering-labs/formae). This plugin enables Formae to manage AWS resources using the [AWS Cloud Control API](https://docs.aws.amazon.com/cloudcontrolapi/latest/userguide/what-is-cloudcontrolapi.html).

## Installation

```bash
# Install the plugin (when published)
formae plugin install aws

# Or build from source
make install
```

## Supported Resources

This plugin supports **209 AWS resource types** across 21 services via the CloudControl API:

| Service | Resources | Examples |
|---------|-----------|----------|
| EC2 | 96 | VPC, Subnet, SecurityGroup, Instance, NATGateway, InternetGateway |
| IAM | 16 | Role, Policy, User, Group, InstanceProfile, OIDCProvider |
| RDS | 16 | DBInstance, DBCluster, DBSubnetGroup, OptionGroup |
| Lambda | 10 | Function, LayerVersion, Permission, EventSourceMapping |
| ECS | 7 | Cluster, Service, TaskDefinition, CapacityProvider |
| S3 | 11 | Bucket, BucketPolicy, AccessPoint |
| EKS | 2 | Cluster, NodeGroup |
| Route53 | 7 | HostedZone, RecordSet, HealthCheck |
| DynamoDB | 2 | Table, GlobalTable |
| KMS | 2 | Key, Alias |
| Secrets Manager | 4 | Secret, ResourcePolicy, RotationSchedule |
| CloudFront | 1 | Distribution |
| ELBv2 | 7 | LoadBalancer, TargetGroup, Listener, ListenerRule |
| ECR | 6 | Repository, RegistryPolicy, ReplicationConfiguration |
| EFS | 3 | FileSystem, MountTarget, AccessPoint |
| SQS | 3 | Queue, QueuePolicy |
| API Gateway | 8 | RestApi, Resource, Method, Deployment, Stage |
| SageMaker | 4 | Domain, UserProfile, Endpoint |
| Elastic Beanstalk | 4 | Application, Environment, ConfigurationTemplate |
| Logs | 1 | LogGroup |

See [`schema/pkl/`](schema/pkl/) for the complete list of supported resource types.

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

The plugin uses the standard AWS credential chain. Configure credentials using one of:

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

**IAM Instance Profile / ECS Task Role:**
When running on EC2 or ECS, credentials are automatically retrieved from the instance metadata service.

**OIDC (for CI/CD):**
See `.github/workflows/ci.yml` for an example using GitHub Actions OIDC with `aws-actions/configure-aws-credentials`.

## Example

Here's a basic infrastructure example creating a VPC with subnets and security groups:

```pkl
amends "@formae/forma.pkl"
import "@formae/formae.pkl"
import "@aws/aws.pkl"
import "@aws/ec2/vpc.pkl"
import "@aws/ec2/subnet.pkl"
import "@aws/ec2/internetgateway.pkl"
import "@aws/ec2/securitygroup.pkl"

local region = "us-east-1"

stack: formae.Stack = new {
    label = "my-infrastructure"
    description = "Basic AWS infrastructure"
}

target: formae.Target = new formae.Target {
    label = "aws-target"
    config = new aws.Config {
        region = region
    }
}

local myVpc: vpc.VPC = new {
    label = "main-vpc"
    CidrBlock = "10.0.0.0/16"
    EnableDnsHostnames = true
    EnableDnsSupport = true
    tags = new Listing {
        new formae.Tag { key = "Name"; value = "main-vpc" }
    }
}

local publicSubnet: subnet.Subnet = new {
    label = "public-subnet-1"
    VpcId = myVpc.ref("VpcId")
    CidrBlock = "10.0.1.0/24"
    AvailabilityZone = "\(region)a"
    MapPublicIpOnLaunch = true
    tags = new Listing {
        new formae.Tag { key = "Name"; value = "public-subnet-1" }
    }
}

local igw: internetgateway.InternetGateway = new {
    label = "main-igw"
    tags = new Listing {
        new formae.Tag { key = "Name"; value = "main-igw" }
    }
}

local webSg: securitygroup.SecurityGroup = new {
    label = "web-sg"
    GroupDescription = "Allow HTTP/HTTPS traffic"
    VpcId = myVpc.ref("VpcId")
    SecurityGroupIngress = new Listing {
        new securitygroup.Ingress {
            IpProtocol = "tcp"
            FromPort = 80
            ToPort = 80
            CidrIp = "0.0.0.0/0"
        }
        new securitygroup.Ingress {
            IpProtocol = "tcp"
            FromPort = 443
            ToPort = 443
            CidrIp = "0.0.0.0/0"
        }
    }
    tags = new Listing {
        new formae.Tag { key = "Name"; value = "web-sg" }
    }
}

forma {
    stack
    target
    myVpc
    publicSubnet
    igw
    webSg
}
```

Apply with:
```bash
formae apply --mode reconcile --watch my-infrastructure.pkl
```

See the [examples/](examples/) directory for more examples.

## Development

### Prerequisites

- Go 1.25+
- [Pkl CLI](https://pkl-lang.org/main/current/pkl-cli/index.html) 0.30+
- AWS credentials (for integration/conformance testing)

### Building

```bash
make build      # Build plugin binary
make test-unit  # Run unit tests
make lint       # Run linter
make install    # Build + install locally
```

### Local Testing

```bash
# Install plugin locally
make install

# Start formae agent
formae agent start

# Apply example resources
formae apply --mode reconcile --watch examples/basic/main.pkl
```

### Credentials Setup for Testing

The `scripts/ci/setup-credentials.sh` script verifies AWS credentials before running conformance tests:

```bash
# Verify credentials are configured
./scripts/ci/setup-credentials.sh

# Run conformance tests (calls setup-credentials automatically)
make conformance-test
```

### Conformance Testing

Run the full CRUD lifecycle + discovery tests:

```bash
make conformance-test                  # Latest formae version
make conformance-test VERSION=0.77.15  # Specific version
```

The `scripts/ci/clean-environment.sh` script cleans up test resources. It runs before and after conformance tests and is idempotent.

## License

This plugin is licensed under the [Functional Source License, Version 1.1, ALv2 Future License (FSL-1.1-ALv2)](LICENSE).

Copyright 2026 Platform Engineering Labs Inc.
