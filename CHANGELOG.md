# Changelog

All notable changes to the formae AWS plugin are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Install with `sudo formae plugin install aws` on the host that runs the
formae agent.

## [0.1.13]

### Added

- AWS EventBridge support: `AWS::Events::EventBus`, `AWS::Events::Archive`, and `AWS::Events::Rule`. You can now manage custom event buses, their archives, and their rules, including the full rule target tree, declaratively. A rule wires to its bus and its targets through the resource graph, and each bus exposes its ARN as a resolvable (`eventBus.res.arn`), so a producer's `events:PutEvents` permission can reference the bus directly. Rules are modelled as children of their event bus so that rules on a custom bus are discovered correctly.
- DynamoDB `Table` now exposes resolvables, `table.res.arn`, `table.res.streamArn`, and `table.res.tableName`. A table's **stream ARN** can be wired straight into a `Lambda::EventSourceMapping`'s `eventSourceArn`, so a stream-triggered Lambda no longer needs a hand-constructed stream ARN, previously there was no way to reference it at all.
- ApiGateway `RestApi` now exposes an **execute-api ARN** resolvable, `api.res.executeApiArn`. A `Lambda::Permission` that lets API Gateway invoke a function can source its `sourceArn` from the API itself (`api.res.executeApiArn`) instead of a hand-built, account-scoped wildcard ARN. The plugin derives the ARN, which CloudControl doesn't return, and fills in the partition and account.
- `AWS::IAM::ServerCertificate` now exposes resolvables, `serverCertificate.res.arn` and `serverCertificate.res.serverCertificateName`. Other resources (for example an HTTPS load balancer listener) can now reference an uploaded server certificate through the resource graph instead of a hand-written ARN.
- `AWS::S3::Object` support. You can now manage S3 objects declaratively, including shipping a local file as the object's body: the CLI reads the file relative to the apply working directory and uploads it. Object tags round-trip on create and update.
- An `AWS::S3::Object` can now fetch its body from a URL. Instead of inline content, the object's `source` can be a structured remote source, the agent downloads the body over HTTPS at apply time, so the bytes never pass through the CLI. The fetch can send request headers (to pull an **authenticated** artifact; the header value is write-only and is not stored in cleartext) and can **extract a named file from a downloaded zip archive**. A typical use is delivering a Lambda deployment package from a versioned build artifact: point the object at a release URL templated by version and resolve the function's `Code` from the object's version. Publishing a new build then redeploys the function, while re-applying the same version is a no-op, no phantom redeploys. The fetch is restricted to HTTPS and refuses to reach loopback, private-network, or instance-metadata addresses. A full walkthrough, handler, release, deploy, and the redeploy loop, is in the [`lambda-http-source` example](https://github.com/platform-engineering-labs/formae-plugin-aws/tree/main/examples/lambda-http-source).
- A CloudFront `Function`'s `functionCode` can now embed references to other resources. Using formae 0.87.0's `formae.embed`, a function's JavaScript body can splice in another resource's generated value (for example a Key Value Store's Id) at apply time, instead of applying the store, copying the Id by hand, and re-applying the function.
- `AWS::EC2::VPNGatewayRoutePropagation` support. You can now manage VPN gateway route propagation declaratively, having a virtual private gateway automatically propagate its learned routes into a route table, and removing that propagation again. This type can't be provisioned through CloudControl, so it previously couldn't be managed at all; the plugin now drives it directly through the EC2 API.
- `AWS::EC2::NetworkInterfacePermission` support. You can now grant (and revoke) another AWS account permission to attach or associate one of your network interfaces. This type can't be provisioned through CloudControl, so it previously couldn't be managed at all; the plugin now drives it directly through the EC2 API. A network interface can also now be referenced by other resources through the resource graph (`someInterface.res.id`).
- `AWS::Route53::RecordSetGroup` support. You can now manage a group of Route 53 records that are created, updated, and deleted together in a single atomic change, useful when a set of records must always change as a unit. This type can't be provisioned through CloudControl, so it previously couldn't be managed at all; the plugin now applies the whole group through one Route 53 change batch. Scope is simple records (name, type, TTL, values, and alias targets); weighted, latency, geolocation, and other routing-policy records are rejected with a clear error rather than silently dropped.
- `AWS::IAM::UserToGroupAddition` support. You can now manage an IAM group membership, adding a user to a group and removing it again, declaratively. This type can't be provisioned through CloudControl, so it previously couldn't be managed at all; the plugin now drives it directly through the IAM API. Each resource models one user-in-group membership, so to add several users to a group you declare one `UserToGroupAddition` per user.

### Changed

- Requires formae 0.87.0 or newer. This release uses two capabilities added in formae 0.87.0: embedding resolvables inside text fields (used by CloudFront `Function.functionCode`, above), and the new field hint for values a provider drops unless they are re-sent on every update. Secret- and configuration-class fields that need that treatment (an OIDC client secret, several ECS service-configuration fields, EC2 Launch Template write-only fields, and an IAM user's initial console password) are now annotated so they keep applying on update under 0.87.0's revised write-only behaviour. `minFormaeVersion` is bumped to 0.87.0 accordingly.
- Network Firewall enum fields (such as a rule group's rule order and a firewall policy's stateful default actions) are now constrained to their valid values at `pkl eval` time, so an invalid value is caught when the forma is evaluated rather than rejected deep in the apply by AWS.

### Fixed

- An IAM `Role` with inline `policies` no longer shows a phantom update on every reconcile. AWS stores a role's inline policies separately and CloudControl's read doesn't return them, so formae re-proposed adding them on every reconcile even though they were already present. A role's inline policies are now read back, so a role that hasn't changed reconciles as a no-op. Note: manage a role's inline policies through `policies` **or** as standalone `AWS::IAM::RolePolicy` resources, not both on the same role.
- A `Lambda::EventSourceMapping` no longer plans a destructive replace on every reconcile. Its `FunctionName` was incorrectly treated as immutable; because AWS reads the field back as the function's full ARN while a forma typically declares the short name, every reconcile saw a "change" to an immutable field and planned a destroy-and-recreate of the mapping. `FunctionName` is now correctly mutable (AWS updates it in place), so the mapping reconciles without a replace. Reference the target function by its ARN (`someFunction.res.arn`) for a clean no-op.
- An ApiGateway `Method` with a Lambda-proxy integration no longer shows a phantom `Integration` update on every reconcile. The function reference is written into the integration as an invocation URI, but the read didn't translate it back, so the stored integration never matched what the forma declared. The read now restores the function reference from the invocation URI, and the same translation is applied when the integration is updated, so re-pointing a method at a different Lambda applies as an in-place update instead of failing.

## [0.1.12]

### Added

- AWS Network Firewall support: `AWS::NetworkFirewall::Firewall`, `AWS::NetworkFirewall::FirewallPolicy`, `AWS::NetworkFirewall::RuleGroup`, and `AWS::NetworkFirewall::LoggingConfiguration`. You can now manage Network Firewall egress controls declaratively, including FQDN-allowlisting outbound traffic from private subnets. Rule groups, policies, and the firewall wire together through the resource graph (policy and rule-group ARNs, the firewall ARN, VPC and subnet IDs, and the log group are all Resolvables). The firewall exposes its per-AZ endpoints as a resolvable map, `firewall.res.endpointIds.at("<az>")`, so a route table can send `0.0.0.0/0` through the firewall endpoint in its own availability zone; existing EC2 routes accept a VPC-endpoint target unchanged. The firewall withholds create/update success until those per-AZ endpoints have propagated, so routes that depend on them don't resolve against an endpoint that isn't ready yet.

### Changed

- Requires formae 0.86.2 or newer. Updating a Network Firewall `FirewallPolicy`'s default-action lists relies on the whole-list replace behaviour added in formae 0.86.2; on earlier agents the update sends the old and new actions together and AWS rejects them as mutually exclusive. `minFormaeVersion` is bumped to 0.86.2 accordingly.
- Applies are more resilient to AWS CloudControl API throttling. When many resources are created or updated at once, status reads that AWS throttled could previously exhaust their retry budget and fail an otherwise-healthy apply; the budget is widened so they ride out throttling bursts.
- Plugin log lines now appear at their true severity in the agent log and carry per-operation attributes (namespace, operation, resource type, label). Previously, benign `INFO`/`WARN` lines, such as recoverable CloudControl throttling retries, surfaced as `ERROR` and carried none of those attributes, making the agent log noisier and harder to filter by resource.

### Fixed

- Route 53 records whose value is a hostname no longer show a phantom update on every reconcile, and no longer risk blocking the stack. AWS canonicalises domain-name record values with a trailing dot, so a `CNAME`, `DNAME`, `NS`, `PTR`, `MX`, or `SRV` record (or an `ALIAS` target) declared without the trailing dot read back dotted and produced a perpetual no-op diff. Because record-set updates are applied as delete-then-create, that mismatch could also fail with `InvalidChangeBatch` and block the apply. formae now normalises the trailing dot on read for these record types, so a dot-less declaration reconciles as a no-op. `TXT` and `SPF` values (quoted character strings) and `A`/`AAAA` (IP addresses) are left untouched.
- ACM certificate DNS-validation records now reconcile cleanly. The validation `CNAME`'s value is sourced from the certificate via `cert.res.validationRecords`, and AWS returns it with a trailing dot while the record set read it back without one, leaving a phantom update on every reconcile. The certificate-sourced value is now normalised the same way, so the validation record settles as a no-op.
- `secret.res.arn` on `AWS::SecretsManager::Secret` now resolves to the secret's ARN. It previously failed to resolve at all, so any resource referencing a secret's ARN that way errored out before any AWS call was made. `secret.res.arn`, `secret.res.id`, and `secret.res.ref` all now resolve to the ARN.
- A secret's value now writes through. Changing `secretString` on `AWS::SecretsManager::Secret` previously had no effect, the value never reached AWS, so the secret didn't rotate. It's now applied, and `opaque.setOnce` is honoured so an unrelated edit won't re-write a set-once value. Requires formae 0.86.2, this release's floor.
- `AWS::SES::EmailIdentity` updates now apply reliably. CloudControl's asynchronous update handler for this type can fail with `GeneralServiceException` ("The security token included in the request is invalid"), failing an otherwise-valid update. The plugin now applies EmailIdentity updates directly through the SES v2 API, MAIL FROM, DKIM signing, feedback forwarding, configuration set, and tags, instead of routing through CloudControl, mirroring how its Read is already handled.
- `AWS::EKS::Cluster` no longer shows a phantom update on every reconcile. Newer Kubernetes versions (1.32 and later) return a control-plane egress mode that AWS populates itself, which formae previously treated as unexpected drift. It is now recognised as a provider-managed default, so a cluster that hasn't changed reconciles as a no-op.

## [0.1.11]

### Added

- Seven new resource types bring fully-managed CloudFront stacks to formae, distributions, their policies, edge functions, and the ACM certificates they depend on, all wired together through the resource graph. The set is `AWS::CertificateManager::Certificate`, `AWS::CloudFront::Function`, `AWS::CloudFront::KeyValueStore`, `AWS::CloudFront::CachePolicy`, `AWS::CloudFront::OriginRequestPolicy`, `AWS::CloudFront::ResponseHeadersPolicy`, and `AWS::CloudFront::OriginAccessControl`. ACM certs ship with a full custom provisioner that talks to the ACM API directly (the resource type is non-provisionable through CloudControl); `cert.res.validationRecords` exposes the DNS validation CNAMEs ACM publishes, so a `Route53::RecordSet` (or any other DNS-publisher resource) can wire them through the resource graph instead of being filled in by hand. CloudFront Distributions can now reference all of the above through Resolvable links, cache policy ID, origin-request policy ID, response-headers policy ID, origin access control ID, function ARN, lambda ARN, and ACM cert ARN, and formae orders creates and destroys correctly based on those references.
- `AWS::ECS::Service` now exposes an `endpoints` resolvable (`service.res.endpoints.at("containerName:containerPort")`) so downstream resources can wire their config URL through the service itself instead of through the listener. Because the endpoint resolves only once the service is operationally stable (the deployment-stability gating added in 0.1.10), anything consuming it waits until tasks are actually serving traffic. This closes the fresh-apply race where a listener URL resolved seconds before the tasks behind it were healthy, leaving consumers pointed at an endpoint returning 503s. Alongside this, `AWS::ElasticLoadBalancingV2::ListenerRule` now exposes its target group ARN so consumers of rule-routed services get correct ordering instead of racing the rule, and a load balancer `name` longer than the AWS 32-character limit is now rejected at `pkl eval` time rather than deep in apply. Listener-rule path/host routing, weighted target groups, and NLB endpoints are deferred follow-ups.
- When a CloudControl operation fails with an error code formae doesn't already handle, the plugin now logs the full progress event: operation, error code, status message, resource type, request token, and identifier. Previously these failures flowed back with no record of the underlying AWS error code, making resource-type-specific timeout and stabilization failures (such as the occasional ECS Service delete that fails once and succeeds on retry) hard to diagnose. The line is emitted once per genuine failure, so it doesn't add noise to the many in-progress polls during long-running operations.

### Changed

- Requires formae 0.86.0 or newer. This release marks 60 provider-immutable fields across 13 services (AppRunner, EC2, ECR, ECS, EKS, Elastic Beanstalk, ELBv2, KMS, Lambda, RDS, Route 53, S3, SageMaker) as `createOnly`, to line up with the new planning behaviour in formae 0.86.0. Under 0.86.0, fields that aren't explicitly immutable are treated as mutable and updated in place rather than triggering a replace. Any field AWS actually rejects on update needs to be marked immutable, or the apply fails at the provider. With this release every such field is annotated correctly, so the 0.86.0 in-place-update behaviour lands without surprise provider rejections. `minFormaeVersion` is bumped to 0.86.0 accordingly.

### Fixed

- Security group rules are now destroyed *after* the workloads that sit behind the security group, instead of in the first destroy wave. Previously `AWS::EC2::SecurityGroupIngress` and `AWS::EC2::SecurityGroupEgress` had no incoming edges in the destroy graph, so they were torn down first, severing ALB-to-task health checks, task-to-EFS NFS, and task-to-internet connectivity, which then cascade-failed the rest of the teardown. The destroy order is now workloads, then the security group rules, then the security group itself.
- `AWS::Lambda::EventInvokeConfig` no longer fails intermittently on create. CloudControl injects an empty `DestinationConfig` (with empty `OnFailure`/`OnSuccess` sub-objects) into every read of this resource, even when you never set one. formae's required-field validation then walked into the injected empty object and reported a missing `Destination`, surfacing as a flaky apply failure. The empty sub-objects are now stripped on read; genuine user-set destinations are non-empty and pass through untouched.
- An EFS file system's mount targets are now torn down ahead of the file system itself, so destroying a stack that mounts EFS into ECS tasks no longer strands resources or fails on dependency-still-attached errors. This is driven by typed edge annotations (`runtimeDependency`) that pull the mount targets into the destroy ordering ahead of their file system.

## [0.1.10]

### Added

- `AWS::ECS::Service` now reports success only once the deployment is operationally stable: the rollout has completed, the running task count matches the desired count, and at least one healthy target exists behind each attached target group. Previously the service reported success as soon as CloudControl acknowledged the request, so any downstream resource reachable through the load balancer (for example a Grafana target driving its config through the listener URL) frequently hit 503s before the tasks were serving traffic. Non-standard service shapes (`CODE_DEPLOY` and `EXTERNAL` deployment controllers, the `DAEMON` scheduling strategy, classic-ELB attachments without a target group ARN, and `desiredCount = 0`) fall through to safe defaults rather than waiting on target health.

## [0.1.9]

### Fixed

- ELBv2 target groups no longer produce phantom drift that blocks `formae apply`. The target group's `Targets` field is populated at runtime by ECS Services (and anything else calling the register-targets API), and `LoadBalancerArns` is populated when a listener attaches the target group to a load balancer; neither is meaningfully user-settable. Tracking them in formae state meant the periodic synchronizer rewrote the resource on every ECS task placement and every listener attach, after which reconcile rejected the next apply with the stacks-have-been-modified error even though the forma hadn't changed, forcing operators into force mode or extract-and-absorb on every reconcile. Both fields are now dropped from the schema and stripped from the AWS read response before they reach formae state.

## [0.1.8]

### Fixed

- ECS `Service` creation no longer fails with `target group <arn> does not have an associated load balancer` when the Service and its Listener are scheduled together in the same apply. The plugin now treats that specific CloudControl error (`InvalidRequest` + matching message text, on Create operations only) as a transient in-progress state; the PluginOperator's existing status-poll loop absorbs the race until the Listener finishes wiring the target group to the load balancer. No PKL change needed, direct `tg.res.targetGroupArn` references keep working and don't have to be rewritten through `listener.res.targetGroupArn` to avoid the race.
- Inverts the destroy edge between an ECS Service and its target groups via `attachesTo` field hints on `Service.LoadBalancer.targetGroupArn` and `Service.VpcLatticeConfiguration.targetGroupArn`. AWS rejects target-group deletion while a Service is still attached; without the annotation, the Service tore down in parallel with the listener chain, and any plugin-target driving CRUD through the Service's listener URL (Grafana, Loki, Tempo, and similar) wedged mid-tear-down with no URL backing it. With the annotation, the Service is destroyed before its target group. Requires formae 0.85.0 or newer to take effect: 0.84.0 agents silently ignore the annotation (the plugin still installs and every other fix applies, but the destroy-edge inversion no-ops). The plugin's `minFormaeVersion` stays at 0.84.0 deliberately, sibling plugins built against the same SDK family work fine on 0.84.0 and forcing an agent upgrade for one annotation isn't worth the disruption.
- Transient `CloudControl` errors are no longer silently terminal. Synchronous errors from CCAPI on `Create`, `Update`, and `Delete` were previously surfaced to the agent as bare Go errors, which the agent classified as `UnforeseenError`, a non-recoverable code that bypasses the retry pipeline entirely. Even errors that AWS itself flags as recoverable (`Throttling`, `NotStabilized`, `ResourceConflict`, …) ended up as terminal failures. The plugin now translates these into the typed `OperationErrorCode` formae's PluginOperator understands, so the agent retries recoverable conditions instead of failing the whole apply.
- `AWS::RDS::DBSubnetGroup` no longer fails non-deterministically when its subnets are created in the same forma. AWS RDS rejected freshly-created EC2 subnets with `InvalidRequestException: Some input subnets ... are invalid` until RDS's internal subnet cache caught up, a classic cross-service eventual-consistency window between EC2 and RDS. The plugin recognises this specific class of `InvalidRequest` as a recoverable race, so the agent retries through the propagation gap and the subnet group lands on the first apply.
- `AWS::SES::ConfigurationSetEventDestination` updates now succeed. Previously, any update to an event destination (toggling `enabled`, changing `matchingEventTypes`, switching destination targets) failed within milliseconds with no AWS API round-trip. CCAPI rejects this resource's composite `<csName>|<edName>` identifier on Update with `ValidationException: not valid for identifier [/properties/Id]`, the same limitation that affected Read in 0.1.7. Updates now route through `sesv2.UpdateConfigurationSetEventDestination` directly.
- `AWS::SES::ConfigurationSetEventDestination` deletes now succeed. Same CCAPI composite-identifier limitation as Update. The most user-visible symptom was that `formae destroy` failed on any stack containing an event destination, and replace flows (when the parent `ConfigurationSet.name` changes) failed at the destroy step. Deletes now go through `sesv2.DeleteConfigurationSetEventDestination`, with a missing-destination error treated as a successful no-op so retried destroys are idempotent.
- Discovery of `AWS::SES::ConfigurationSetEventDestination` now correctly surfaces existing event destinations as unmanaged resources. CCAPI's `ListResources` returns bare `EventDestinationName`s (`"bounces"`) instead of the composite `<csName>|<edName>` the resource's Read path requires, so every discovered destination failed its per-resource Read and never made it into the inventory. The plugin's List now walks `ListConfigurationSets` → `GetConfigurationSetEventDestinations` and emits properly-formed composite identifiers.

## [0.1.7]

### Added

- Amazon SES support for outbound transactional email. `AWS::SES::EmailIdentity` covers verified sending domains or addresses (with bundled MAIL FROM, feedback, and Easy DKIM attributes). `AWS::SES::ConfigurationSet` and `AWS::SES::ConfigurationSetEventDestination` route bounce/complaint/delivery events to SNS, Kinesis Firehose, EventBridge, or CloudWatch, exactly one of the four destination types is enforced at PKL evaluation time, so bad shapes fail at `pkl eval` rather than at apply time. `AWS::SES::EmailIdentityVerification` is a polling gate downstream consumers depend on for send-readiness; it sits between the identity (and the DNS records that verify it) and any resource that needs to send mail, breaking the apply-time deadlock where verification needs DNS, DNS depends on the identity, and the identity can't wait on either.
- `EmailIdentity.res.requiredDnsRecords` is a typed listing resolvable that exposes the DNS records SES expects, 3 DKIM CNAMEs, plus an MX and SPF TXT for MAIL FROM when configured. A forma drives Route53 (or any DNS plugin) directly off `id.res.requiredDnsRecords.at(N).name` and `.values`, with no manual token extraction. See `examples/ses-basic/main.pkl` in the plugin repo for the full pattern. Terraform, Pulumi, and Crossplane all force users to extract `verification_token`/`dkim_tokens[]` strings and hand-author the records; this is the first IaC tool to wire them automatically.

### Fixed

- `AWS::EC2::PlacementGroup`'s `spreadLevel` is now marked `hasProviderDefault`. AWS auto-populates `SpreadLevel` after a `partition`-strategy create even when not specified, which previously caused replace flows (where the user switches `strategy` from `spread` to `partition`) to fail with `Property SpreadLevel is not expected and not a provider default`. The field stays `createOnly`.
- Container-level changes on `AWS::ECS::TaskDefinition` are no longer silently dropped from the diff. 19 user-canonical sub-fields on `ContainerDefinition`, including `environment`, `portMappings`, `mountPoints`, `secrets`, `command`, `entryPoint`, `dependsOn`, `extraHosts`, and `dockerLabels`, were previously annotated `hasProviderDefault`, which symmetrically stripped them from both the desired and actual sides before comparison. Any real change to those fields produced an empty plan on `formae apply`, forcing operators into out-of-band `aws ecs register-task-definition` workarounds (which then showed up as drift on the next reconcile). The annotation is now scoped to the genuinely cloud-defaulted scalars (`cpu`, `essential`, `versionConsistency`).
- `AWS::Lambda::LayerVersion`'s `compatibleArchitectures`, `compatibleRuntimes`, and `description` are now marked `hasProviderDefault`. AWS Read returns empty values for these when the user omits them in PKL, and because every `LayerVersion` field is `createOnly`, the resulting phantom drift would schedule a destroy + create on every reapply.

## [0.1.6]

### Fixed

- Updating an `AWS::Lambda::EventInvokeConfig` no longer fails with `Model validation failed: required key [Destination] not found`. CloudControl's update handler for this resource type re-validates the full server-side state on every patch, even fields the patch doesn't touch, and AWS materialises empty `DestinationConfig.OnFailure` / `OnSuccess` sub-objects into the response on Read whether or not the caller ever set them, which then fail the schema's "if present, `Destination` is required" rule. Updates that only changed `MaximumRetryAttempts` or `MaximumEventAgeInSeconds` would still get rejected on the server side. Updates now route through the Lambda `UpdateFunctionEventInvokeConfig` API directly, which has no such cross-field re-validation.
- ELBv2 `Listener.defaultActions[*].forwardConfig` and ECS `Cluster.clusterSettings` are now marked `hasProviderDefault`. ELBv2 derives `ForwardConfig` (target groups + stickiness defaults) on Read from the action's `TargetGroupArn` when the user specifies a simple forward target, and ECS populates `clusterSettings` with default entries like `containerInsights: disabled` when none are configured. Without the annotations, every reapply emitted a no-op patch on these fields that surfaced as a spurious update.

## [0.1.5]

### Added

- Complete EKS resource coverage. `Addon`, `AccessEntry`, `FargateProfile`, `PodIdentityAssociation`, and `IdentityProviderConfig` are now first-class resources alongside `Cluster` and `Nodegroup`, enabling end-to-end EKS cluster management (including discovery of cluster-child resources) from a single forma.
- ECS `ExpressGatewayService` support. Express Gateway services can now be managed through formae. Uses the native ECS SDK because the CloudControl handler for this type is broken server-side.
- ALB Listener URL resolvable, Listeners now expose a computed `url` property that combines the parent ALB's DNS name with the Listener's protocol and port. Use `listener.res.url` to wire load balancer endpoints into target configs without manual URL construction.
- ~225 reference-bearing properties (IDs, ARNs, names) across 77 schema files now accept `formae.Resolvable`, and all resources with Resolvable classes now have `hidden res` wired up.

### Changed

- The `profile` field is now mutable, changing it updates the target in place without recreating resources. The `region` field remains immutable; changing it triggers a full target replace as before. See Per-field config mutability for details.

### Fixed

- ECS `Service` and `TaskSet` discovery no longer spam `InvalidRequestException: Missing Or Invalid ResourceModel property` errors on every cycle. Service is now discovered as a child of Cluster (its list handler requires a `Cluster` filter), and TaskSet is enumerated via the ECS SDK's `DescribeServices` because its CloudControl list handler demands an `Id` and is effectively a Read.
- TaskDefinition was missing its resolvable wiring, `taskDef.res.taskDefinitionArn` now works as expected, enabling ECS Services to reference task definitions via resolvables.
- `EFSVolumeConfiguration.filesystemId` now accepts resolvable references, so ECS tasks can reference EFS filesystems created in the same forma.
- VPCGatewayAttachment now exposes resolvable references (`igwAttach.res.internetGatewayId`). Routes should reference the gateway ID through the attachment, not the gateway directly, to ensure correct destroy ordering. Without this, destroying a stack with Routes and an IGW attachment could hang for hours because formae tried to detach the IGW before deleting the routes that use it.
- Listener now exposes `listener.res.targetGroupArn`, resolving to the target-group ARN attached via the listener's first default action. ECS Services (and any other consumer that needs the target group already wired to the load balancer) should reference this instead of `tg.res.targetGroupArn`. Without it, AWS rejects ECS service creation with "target group does not have an associated load balancer" when formae schedules the service before the listener attaches the TG. Same pattern as `igwAttach.res.internetGatewayId` for routes/IGW.
- Patch-mode updates to resources with optional nested objects (for example, Lambda `EventInvokeConfig.DestinationConfig`) could fail with CloudControl errors like `required key [Destination] not found` when the nested object was empty. The plugin now strips empty sub-objects from `replace` operations the same way it already did for `add` operations, so these updates succeed.
- Reapplying an unchanged forma containing an ECS Service no longer spuriously replaces the service. AWS fills in default port-mapping values on `awsvpc` tasks that the user didn't set, which previously made the planner think the task definition had changed. Those fields are now recognised as provider-populated and ignored during comparison. Requires formae 0.84.0.
- `EFSVolumeConfiguration.rootDirectory` no longer causes phantom replacements. CloudControl returns `RootDirectory` as `"/"` even when it was never set, which made the planner see a change on every reapply.
- ECS Service and TaskSet no longer produce spurious replacements when the Cluster field is referenced via ARN. CloudControl normalises the Cluster to a short name on Read, which flipped the `createOnly` field and triggered a full replace. ARN-vs-short-name differences are now normalised before comparison.
- TaskSet updates no longer hang indefinitely. CloudControl's update handler for TaskSet Scale never returns; a custom ECS SDK update provisioner is used instead.

## [0.1.4]

### Fixed

- Route53 RecordSets with the same name but different types (e.g., SOA and NS for the same domain) were discovered with identical labels, causing duplicate entries that churned on every sync cycle. Labels now include the record type, producing unique entries like `example.com.-SOA` and `example.com.-NS`.
- Resources with composite CloudControl identifiers (e.g., ECS Services, Lambda EventInvokeConfigs) could show up as duplicates in inventory, one from the initial create and another from discovery. This happened because AWS CloudControl returns full ARNs during create but short names during list. Identifiers are now normalized so the resource created by apply and the resource found by discovery are correctly recognized as the same thing.
- Discovery of subnet route table associations could cause apply commands to get permanently stuck. AWS returns VPC-level (main) route table associations in discovery results that cannot be read, which triggered a cascade of internal failures. These associations are now filtered out during discovery.
- Updates to resources with provider-default nested objects (e.g. Lambda EventInvokeConfig destination config) could fail with CloudControl validation errors. Empty nested objects left behind after stripping unused fields were not being removed, causing required-field violations.

## [0.1.3]

### Added

- Conformance test coverage for 88 resource types across EC2, ECS, EKS, ELBv2, Elastic Beanstalk, Lambda, API Gateway, RDS, Route53, S3, SQS, IAM, KMS, EFS, ECR, CloudWatch Logs, Secrets Manager, and DynamoDB, validating the full create, read, update, delete, sync, and discovery lifecycle.
- Cross-resource references: TransitGateway, FlowLog, ResourcePolicy, ConfigurationTemplate, NetworkAclEntry, and TargetGroupTuple fields now support resolvable references, enabling correct dependency ordering during apply.

### Fixed

- Several resource types had broken operations through AWS CloudControl that are now fixed:
  - S3 BucketPolicy: reads were broken, now works correctly
  - S3 StorageLensGroup: updates were missing resource properties in the response
  - SQS QueuePolicy: was not provisionable through CloudControl, now works via direct SQS API
  - IAM Policy: was not provisionable through CloudControl, now works via direct IAM API
  - IAM AccessKey: was not provisionable through CloudControl, now works via direct IAM API
  - IAM InstanceProfile: suffered from a 60-second propagation delay through CloudControl, now works via direct IAM API
  - EC2 NetworkAclEntry: was not supported through CloudControl, now works via direct EC2 API
  - Elastic Beanstalk ConfigurationTemplate: updates through CloudControl injected CloudFormation references, now works via direct EB API
- Spurious diffs during updates and synchronization for resources where AWS populates default values (e.g. LoadBalancer attributes, ECS container defaults, Lambda runtime settings). Over 130 fields across 54 resource schemas now correctly distinguish user-specified values from provider defaults.
- Newly created resources could appear with missing identifiers or properties in the inventory until the next sync.
- Empty optional fields could cause apply failures with CloudControl validation errors (e.g. Lambda Architectures, ECS container definitions). Optional fields that are not set are now correctly omitted.
- Elastic Beanstalk ConfigurationTemplates deleted outside of formae were not correctly detected during synchronization.
- CloudControl status polling now correctly handles ELBv2 update semantics and prevents extract crashes on resources with complex nested properties.

## [0.1.2]

### Added

- Phase 1 conformance tests covering 32 standalone resources, validating the full CRUD and discovery lifecycle.
- Expanded Route53 HealthCheck schema with fully typed `HealthCheckConfig` and `AlarmIdentifier` sub-resources, enabling richer health check definitions in Pkl.
- Added `ResourceLifecycleConfig` fields to ElasticBeanstalk environments and configuration templates.
- Added an AppRunner example demonstrating a simple web service deployment.

### Changed

- Renamed `apprunner/service.pkl` to `apprunner/apprunnerservice.pkl` for consistency with the naming convention used across other resource schemas.

### Fixed

- Extract now correctly filters `ListResults`, preventing unrelated resources from appearing in extracted Pkl output.

## [0.1.1]

### Added

- Added support for AppRunner resources (`AWS::AppRunner::Service`), enabling management of AppRunner web services through formae.

### Fixed

- Added missing `af-south-1` availability zone pattern. The `Region` typealias already included `af-south-1`, but the `AvailabilityZone` constraint was missing it, causing Pkl evaluation failures for resources in that region.

## [0.1.0]

### Added

- Initial release of the AWS plugin as a standalone package built on the formae Plugin SDK.
