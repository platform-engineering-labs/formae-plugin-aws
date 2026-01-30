# Aurora Data API Infrastructure

This example creates an Aurora PostgreSQL Serverless v2 cluster with the **Data API enabled** for testing the `DatastoreAuroraDataAPI` implementation.

## Deploy Infrastructure

```bash
# Apply the Aurora Data API infrastructure
./formae apply --mode reconcile --watch ../formae-plugin-aws/examples/aurora-dataapi/main.pkl
```

## What Gets Created

| Resource | Purpose |
|----------|---------|
| VPC + Subnets | Network for Aurora (required by AWS) |
| Security Group | Allow PostgreSQL within VPC |
| DB Subnet Group | Required for Aurora cluster |
| Aurora PostgreSQL Cluster | Serverless v2 with `enableHttpEndpoint = true` |
| Aurora DB Instance | Serverless instance in the cluster |

## Get ARNs for Testing

After deployment:

```bash
# Cluster ARN
aws rds describe-db-clusters --db-cluster-identifier aurora-dataapi-cluster \
    --query 'DBClusters[0].DBClusterArn' --output text

# Secret ARN (RDS-managed master password)
aws rds describe-db-clusters --db-cluster-identifier aurora-dataapi-cluster \
    --query 'DBClusters[0].MasterUserSecret.SecretArn' --output text
```

Or via formae:

```bash
  ./formae inventory resources --query 'type:AWS::RDS::DBCluster'                                                                                    
  ./formae inventory resources --query 'stack:aurora-dataapi' 
```

## Run Datastore Tests

Once deployed, run the datastore tests against Aurora Data API:

```bash
cd ../formae

go test -v ./internal/metastructure/datastore/... \
    -dbType=auroradataapi \
    -clusterArn="arn:aws:rds:us-east-1:ACCOUNT:cluster:aurora-dataapi-cluster" \
    -secretArn="arn:aws:secretsmanager:us-east-1:ACCOUNT:secret:rds!cluster-..." \
    -database="formae"
```

## IAM Permissions Required

Your AWS credentials need these permissions to call Data API:

```json
{
    "Version": "2012-10-17",
    "Statement": [
        {
            "Effect": "Allow",
            "Action": [
                "rds-data:ExecuteStatement",
                "rds-data:BatchExecuteStatement",
                "rds-data:BeginTransaction",
                "rds-data:CommitTransaction",
                "rds-data:RollbackTransaction"
            ],
            "Resource": "arn:aws:rds:*:*:cluster:aurora-dataapi-cluster"
        },
        {
            "Effect": "Allow",
            "Action": "secretsmanager:GetSecretValue",
            "Resource": "arn:aws:secretsmanager:*:*:secret:rds!cluster-*"
        }
    ]
}
```

## Cleanup

```bash
./formae destroy --watch ../formae-plugin-aws/examples/aurora-dataapi/main.pkl
```
