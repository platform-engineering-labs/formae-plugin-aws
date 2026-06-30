# Event-driven Lambda from a versioned release artifact

A small, end-to-end example that ties together three capabilities of the AWS
plugin:

| Resource | What it shows |
|---|---|
| `AWS::S3::Object` with a structured **`HttpSource`** | The agent fetches the deployment package over HTTPS from a release URL and uploads it to S3 — no bytes shipped through the CLI. |
| `AWS::Lambda::Function` with `Code.s3ObjectVersion` | The S3 object's **VersionId** is the redeploy signal: a new release redeploys; an unchanged re-apply does **not** (no phantom redeploys). |
| `AWS::Events::EventBus` + `AWS::Events::Rule` + `AWS::Lambda::Permission` | An EventBridge rule on a custom bus routes matching events to the function. |

The deployment package is addressed by version: `app-version` is templated into
the release URL, so bumping it points the `HttpSource` at a new release.

```
publish release vX.Y.Z (asset: handler.zip)
   └─> formae apply --properties app-version=X.Y.Z
         └─> HttpSource re-fetches the asset -> new S3 VersionId
               └─> Lambda Code.s3ObjectVersion changes -> redeploy
```

## Prerequisites

1. A running formae agent with AWS credentials for your account/region.
2. The AWS plugin installed (`aws@0.1.13-dev.7` or newer — the release that adds
   `HttpSource`).
3. A reachable **release asset** that is your Lambda deployment zip. Edit
   `main.pkl` and replace the placeholder URL:

   ```pkl
   url = "https://github.com/YOUR_ORG/YOUR_APP/releases/download/v\(appVersion)/handler.zip"
   ```

   Any HTTPS URL that returns the zip works (a public GitHub release asset, an
   S3 pre-signed URL, an artifact server, …). For a **private** GitHub repo,
   uncomment the `headers` block in `main.pkl` and export `GITHUB_TOKEN` (a
   fine-grained token with `contents: read`) in the **agent's** environment.

4. Pick a globally-unique bucket name — edit `bucketName` in `main.pkl`
   (`...-artifacts-changeme`).

## Deploy

Apply, passing the release version you want to deploy:

```bash
formae apply --properties app-version=1.0.0 examples/lambda-http-source/main.pkl
```

What happens: the agent creates the versioned bucket, **fetches the release asset
and uploads it** as `handler-1.0.0.zip` (recording its `VersionId`), creates the
role / log group / function (with `Code` pointing at that exact object version),
then the event bus, rule, and invoke permission.

Verify it landed:

```bash
formae inventory --stack lambda-http-source
```

Fire a test event onto the bus and confirm the function ran:

```bash
aws events put-events --entries '[{
  "EventBusName": "lambda-http-source-bus",
  "Source": "com.example.orders",
  "DetailType": "OrderPlaced",
  "Detail": "{\"orderId\":\"42\"}"
}]'

aws logs tail /aws/lambda/lambda-http-source-handler --since 2m
```

## The redeploy loop

This is the point of `HttpSource` + `s3ObjectVersion`.

**A new release redeploys.** Publish `v1.0.1` (a new `handler.zip` asset), then:

```bash
formae apply --properties app-version=1.0.1 examples/lambda-http-source/main.pkl
```

The URL now resolves to the `v1.0.1` asset → the agent re-fetches → a new S3
`VersionId` → the function's `Code.s3ObjectVersion` changes → a genuine redeploy.

**An unchanged re-apply does not.** Re-run the *same* version:

```bash
formae apply --properties app-version=1.0.1 examples/lambda-http-source/main.pkl
```

The resolved object is unchanged, so the plan shows **no change** to the function
— no phantom `UpdateFunctionCode`, no version churn. (`Code` is write-only but is
not force-resent on every reconcile.)

## Variants

- **Private repo:** uncomment the `headers` block in `main.pkl`; the agent sends
  `Authorization: Bearer <token>` and drops it on the cross-origin redirect to
  the asset's blob host. The header value is write-only and never persisted in
  cleartext.
- **GitHub Actions artifact instead of a release asset:** an Actions artifact's
  download URL is keyed by an opaque artifact id (not derivable from a version)
  and is a zip that *wraps* your file. Fetching it needs the GitHub plugin's
  `WorkflowRun` to resolve the URL, plus `extract = "handler.zip"` on the
  `HttpSource` to pull the member out of the artifact zip.

## Clean up

```bash
formae destroy --stack lambda-http-source
```
