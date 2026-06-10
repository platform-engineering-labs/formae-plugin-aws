# Formae Bootstrap on AWS

Stands up a production formae agent on AWS — VPC, **RDS PostgreSQL**, ECS Fargate
running the agent, and an Application Load Balancer for the API. The agent connects
to Postgres over TCP (port 5432); the DB password is generated and kept in Secrets
Manager, injected into the container at task start.

Once it's up, point your local formae CLI at the remote agent. The local SQLite
instance is no longer needed — the remote agent runs discovery and rebuilds its
inventory from what's already deployed.

Two entry files, pick the one that fits:

| File | Use when | Provisions |
|------|----------|------------|
| [`main.pkl`](main.pkl) | You have **no** database — bootstrap from scratch | VPC + **RDS PostgreSQL** + ECS + ALB + agent |
| [`existing-db.pkl`](existing-db.pkl) | You **already have** a Postgres database | VPC + ECS + ALB + agent (no DB), pointed at your connection |

## Prerequisites

- formae CLI installed
- AWS plugin installed: `formae plugin install aws`
- AWS credentials configured (`~/.aws/credentials` or environment variables)
- Permissions for: VPC, RDS, ECS, IAM, ELB, CloudWatch Logs, Secrets Manager

## Deploy — from scratch (`main.pkl`)

Provisions everything, including a `db.t4g.micro` RDS PostgreSQL instance
(cluster spin-up dominates the wall-clock, ~10 min):

```bash
formae apply --mode reconcile examples/bootstrap/main.pkl
```

## Deploy — bring your own database (`existing-db.pkl`)

Skips the database; you supply the connection. Put the password in a Secrets
Manager secret and pass its ARN — it's injected at runtime, never written into Pkl
or the task definition:

```bash
formae apply --mode reconcile examples/bootstrap/existing-db.pkl \
  --db-host mydb.abc123.us-east-2.rds.amazonaws.com \
  --db-name formae --db-user formae \
  --db-password-secret-arn arn:aws:secretsmanager:us-east-2:<acct>:secret:my-db-pw
```

- `--db-host` and `--db-password-secret-arn` are required; `--db-port` (5432),
  `--db-user` (`formae`), and `--db-name` (`formae`) default sensibly.
- If your secret stores **JSON** (e.g. an RDS-managed secret), append the key
  selector to the ARN: `...:secret:my-db-pw:password::`.
- **Networking:** the Fargate task runs in the VPC this file creates, so the
  database must be reachable from it — a publicly-accessible endpoint, or peer/share
  this VPC with your database's VPC (or adapt the file to deploy into existing subnets).

## Connect

Get the ALB DNS name from inventory:

```bash
formae inventory resources --query 'type:AWS::ElasticLoadBalancingV2::LoadBalancer'
```

Point your CLI at the remote agent in `~/.config/formae/formae.conf.pkl`:

```pkl
cli {
  api {
    url = "http://<alb-dns>"
    port = 49684
  }
}
```

Verify:

```bash
formae status agent
```

## Teardown

```bash
formae destroy --mode reconcile examples/bootstrap/main.pkl          # from-scratch
# or
formae destroy --mode reconcile examples/bootstrap/existing-db.pkl   # BYO (leaves your DB alone)
```

## Configuration

Edit [`vars.pkl`](vars.pkl) to customize:

| Variable | Default | Description |
|----------|---------|-------------|
| projectName | formae-bootstrap | Prefix for all resource names |
| region | us-east-2 | AWS region |
| vpcCidr | 10.100.0.0/16 | VPC CIDR block |
| dbName | formae | Database name |
| dbUser | formae | Database master/user name |
| dbInstanceClass | db.t4g.micro | RDS instance class (`main.pkl` only) |
| dbEngineVersion | 16.4 | PostgreSQL engine version (`main.pkl` only) |
| formaeImage | ghcr.io/.../formae:0.83.2 | Formae container image |
| formaePort | 49684 | Agent API port |
