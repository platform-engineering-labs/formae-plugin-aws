# Production-Grade VPC Infrastructure

This example provisions a complete AWS VPC with public and private subnets across multiple availability zones, ready for production workloads.

## Architecture

- **VPC** with DNS support and hostnames enabled
- **Internet Gateway** attached to the VPC for public internet access
- **NAT Gateway** with Elastic IP for private subnet outbound traffic
- **3 Public Subnets** across AZs (a, b, c) with auto-assigned public IPs
- **3 Private Subnets** across AZs (a, b, c) for internal workloads
- **Route Tables** — public routes through IGW, private routes through NAT
- **Security Groups** — public SG defaults, private SG allows all intra-VPC traffic

## Files

| File | Description |
|------|-------------|
| `main.pkl` | Entry point — wires variables into VPC class and spreads resources |
| `vars.pkl` | Configuration variables, CLI-overridable props, stack and target |
| `infrastructure/vpc.pkl` | Reusable VPC class that produces all resources |

## Usage

Ensure the formae agent is running, then:

```bash
formae apply --mode reconcile --watch examples/vpc/main.pkl
```

### CLI Overrides

Override defaults via flags:

```bash
formae apply --mode reconcile --watch examples/vpc/main.pkl \
  --name my-vpc \
  --region us-east-1 \
  --vpc-cidr 10.0.0.0/16
```

## Default Configuration

| Variable | Default |
|----------|---------|
| `name` | `vpc-example` |
| `region` | `us-west-2` |
| `vpc-cidr` | `10.1.0.0/16` |
| Public subnets | `10.1.0.0/19`, `10.1.64.0/19`, `10.1.128.0/19` |
| Private subnets | `10.1.32.0/19`, `10.1.96.0/19`, `10.1.160.0/19` |
