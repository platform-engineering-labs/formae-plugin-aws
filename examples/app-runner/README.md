# app-runner Example
This example deploys a simple AWS App Runner service from a public container image. App Runner is the simplest way to run a container on AWS — no VPC, IAM roles, or load balancer configuration required.

## What Gets Provisioned
A single App Runner service running `public.ecr.aws/aws-containers/hello-app-runner:latest` on port 8000. App Runner automatically provisions a load balancer, TLS certificate, and auto-scaling.

## Provisioning the resources
Ensure the **formae** node is up and running, then run the following command:

```bash
formae apply --mode reconcile --watch examples/app-runner/service.pkl
```

## Verifying the deployment
Check that the service appears in the inventory:

```bash
formae inventory resources --query 'stack:apprunner-stack'
```

Grab the service ARN from the `NativeID` column and retrieve the service URL:

```bash
aws apprunner describe-service --service-arn <ServiceArn> --region us-west-2 --query 'Service.ServiceUrl' --output text
```

Then test it:

```bash
curl https://<ServiceUrl>
```

## Destroying the resources
Ensure the **formae** node is up and running, then run the following command:

```bash
formae destroy --query 'stack:apprunner-stack'
```
