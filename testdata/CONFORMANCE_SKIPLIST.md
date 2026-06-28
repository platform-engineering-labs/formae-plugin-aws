# Conformance coverage skip / defer list

Tracks AWS resource types that intentionally do **not** have a conformance
fixture (resource-under-test), with the reason. Part of PLA-112 ("100% AWS
conformance coverage"): the definition of done is `covered + skipped == all
declared types`, every skip carrying a one-line reason.

A type is "covered" only when it is the resource-under-test (the last
`new <module>.<Class>` in a `testdata/*.pkl` `forma {}` block). Appearing only
as a dependency does not count.

## Categories

- **Tier S - un-CI-able:** cannot run the CRUD lifecycle in standard CI
  (physical hardware, retired platforms, human-in-the-loop).
- **Tier D - deferred:** possible but deferred for cost, runtime, or heavy
  multi-service prerequisites.
- **Harness limitation:** the type can be created but cannot satisfy a
  conformance step (e.g. the OOB-delete-then-gone check).
- **NON_PROVISIONABLE (needs custom provisioner):** the AWS Cloud Control API
  has no handler for the type (`describe-type` -> `ProvisioningType =
  NON_PROVISIONABLE`), so the generic CRUD path fails instantly. Each needs a
  custom provisioner before it can be covered. Surfaced by a sweep of all
  declared types' `ProvisioningType` (run via `aws cloudformation describe-type`).

> Note: NON_PROVISIONABLE alone does not mean unsupported. Several
> NON_PROVISIONABLE types already have custom provisioners and ARE covered
> (CertificateManager::Certificate, EC2::NetworkAclEntry, EC2::Route,
> IAM::AccessKey, IAM::Policy, Route53::RecordSet, SQS::QueuePolicy, ...). Only
> the ones below lack a provisioner.

## Tier S - un-CI-able

| Type | Reason |
| -- | -- |
| EC2::LocalGatewayRouteTable | Local Gateways exist only on AWS Outpost hardware; none in CI. |
| EC2::LocalGatewayRoute | Same (Outpost). |
| EC2::LocalGatewayRouteTableVPCAssociation | Same (Outpost). |
| EC2::LocalGatewayRouteTableVirtualInterfaceGroupAssociation | Same (Outpost). |
| RDS::DBSecurityGroup | EC2-Classic-only; EC2-Classic retired Aug 2022. Also NON_PROVISIONABLE. |
| RDS::DBSecurityGroupIngress | Same (EC2-Classic). Also NON_PROVISIONABLE. |
| EC2::EnclaveCertificateIamRoleAssociation | Requires a Nitro Enclaves context + enclave-specific ACM cert. |
| SES::EmailIdentityVerification | Verifying an email-address identity needs a human to click a link in the mailbox. (Has a custom provisioner, but cannot be conformance-tested.) |

## Tier D - deferred (cost / time / multi-service prereqs)

| Type | Reason |
| -- | -- |
| EC2::ClientVpnEndpoint | Needs an ACM server cert (mTLS); slow create/delete. Also NON_PROVISIONABLE. |
| EC2::ClientVpnAuthorizationRule | Depends on ClientVpnEndpoint. Also NON_PROVISIONABLE. |
| EC2::ClientVpnRoute | Depends on ClientVpnEndpoint. Also NON_PROVISIONABLE. |
| EC2::ClientVpnTargetNetworkAssociation | Depends on ClientVpnEndpoint. Also NON_PROVISIONABLE. |
| SecretsManager::RotationSchedule | Needs a deployed Lambda rotation function. |
| SecretsManager::SecretTargetAttachment | Links a secret to a live RDS/Redshift/DocDB instance. |
| EC2::Host | Dedicated Host allocates single-tenant physical hardware, billed per hour. |
| EC2::CapacityReservation | Reserves (and bills for) real instance capacity. |
| EC2::CapacityReservationFleet | Same. |
| SageMaker::Endpoint | Spins up a real inference instance ($/hr). |
| EC2::TransitGatewayConnect | Needs GRE/BGP Connect peers (live networking participants). |
| EC2::TransitGatewayMulticastDomain | Needs multicast-enabled ENIs subscribed to the domain. |
| EC2::TransitGatewayMulticastDomainAssociation | Same. |
| EC2::TransitGatewayMulticastGroupMember | Same. |
| EC2::TransitGatewayMulticastGroupSource | Same. |
| EC2::TrafficMirrorSession | Source ENI must be on a Nitro instance with live traffic. |
| EC2::VerifiedAccessEndpoint | Needs a load balancer/ENI + ACM cert + trust provider. |
| EC2::TransitGatewayPeeringAttachment | Needs a 2nd-region target, peerAccountId, and an accept handshake. |
| EC2::CarrierGateway | Needs Wavelength zones opted into the account. |
| EC2::VPCEndpointConnectionNotification | Requires an SNS topic, but the plugin has no SNS schema yet. |
| RDS::CustomDBEngineVersion | Custom Oracle/SQL-Server media + licensing. |
| RDS::DBShardGroup | Aurora Limitless (preview). |
| RDS::Integration | Zero-ETL multi-service (Aurora -> Redshift). |
| IAM::VirtualMFADevice | CloudControl create returns a seed but clean enable/delete needs an association flow. |

## Harness limitation

| Type | Reason | Tracking |
| -- | -- | -- |
| EC2::SubnetCidrBlock | IPv6 /64 cannot be derived statically; needs plugin-side auto-derivation or a slow GUA IPAM-pool fixture. | PLA-121 |
| EC2::VPCDHCPOptionsAssociation | Full CRUD passes, but OOB-delete resets the VPC to the default DHCP option set rather than removing an object; Cloud Control still returns an association for the VpcId, so sync sees drift (changed dhcpOptionsId), never a deletion. The harness "gone after OOB delete" step cannot pass. | - |

## NON_PROVISIONABLE - needs custom provisioner

| Type | AWS API for the provisioner | Tracking |
| -- | -- | -- |
| EC2::VPNGatewayRoutePropagation | ec2:EnableVgwRoutePropagation / DisableVgwRoutePropagation / DescribeRouteTables | PLA-145 (in implementation) |
| EC2::NetworkInterfacePermission | ec2:CreateNetworkInterfacePermission / DescribeNetworkInterfacePermissions / DeleteNetworkInterfacePermission | PLA-146 |
| ElasticLoadBalancingV2::ListenerCertificate | elbv2:AddListenerCertificates / DescribeListenerCertificates / RemoveListenerCertificates | PLA-147 |
| IAM::UserToGroupAddition | iam:AddUserToGroup / GetGroup / RemoveUserFromGroup | PLA-148 |
| Route53::RecordSetGroup | route53:ChangeResourceRecordSets / ListResourceRecordSets | PLA-149 |
