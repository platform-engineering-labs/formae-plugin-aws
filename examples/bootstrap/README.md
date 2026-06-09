# Formae Bootstrap on AWS

Provisions a production formae agent on AWS using Aurora Serverless v2 and ECS
Fargate. Two stages: first the database and networking, then formae itself.

After both stages, your local formae CLI can point at the remote agent. The local
SQLite instance is no longer needed — the remote agent runs discovery and rebuilds
its inventory from what's already deployed.

## Prerequisites

- formae CLI installed
- AWS plugin installed: `formae plugin install aws`
- AWS credentials configured (`~/.aws/credentials` or environment variables)
- Permissions for: VPC, RDS, ECS, IAM, ELB, CloudWatch Logs, Secrets Manager

## Deploy

Stage 1 provisions VPC, subnets, and Aurora PostgreSQL (~15 minutes):

```bash
formae apply --mode reconcile examples/bootstrap/stage1-infra.pkl
```

Stage 2 provisions ECS, ALB, and the formae container (~5 minutes):

```bash
formae apply --mode reconcile examples/bootstrap/stage2-formae.pkl
```

## Connect

Get the ALB DNS name from inventory:

```bash
formae inventory resources --query 'type:AWS::ElasticLoadBalancingV2::LoadBalancer'
```

Update `~/.config/formae/formae.conf.pkl` to point your CLI at the remote agent:

```pkl
cli {
  api {
    url = "http://<alb-dns>"
    port = 49684
  }
}
```

Verify the connection:

```bash
formae status agent
```

## Teardown

Destroy in reverse order:

```bash
formae destroy examples/bootstrap/stage2-formae.pkl
formae destroy examples/bootstrap/stage1-infra.pkl
```

## Configuration

Edit `vars.pkl` to customize:

| Variable | Default | Description |
|----------|---------|-------------|
| projectName | formae-bootstrap | Prefix for all resource names |
| region | us-east-1 | AWS region |
| vpcCidr | 10.100.0.0/16 | VPC CIDR block |
| formaeImage | ghcr.io/.../formae:0.83.2 | Formae container image |
| formaePort | 49684 | Agent API port |
