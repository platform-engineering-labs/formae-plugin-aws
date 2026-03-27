# GCP Inventory Regression Analysis (March 23, 2026)

## Problem

5 GCP conformance tests fail with "Inventory returned 0 resources" after a successful create:
- cloudrun-service, cloudrun-job, cloudrun-worker-pool, firewall, table

Last green GCP run: March 20 (formae 0.82.3). Started failing March 21 (formae 0.83.0).

## Root Cause

`validateRequiredFields` in `resource_persister.go` (line 126) silently drops the resource
when required schema fields are missing from the properties:

```go
if err := validateRequiredFields(resourceUpdate.DesiredState); err != nil {
    slog.Debug("Validation of required fields failed", "error", err)
    return "", nil  // Silent drop — resource never persisted
}
```

The command still reports Success because the ResourceUpdater doesn't know the persist was skipped.

Example error from CI logs:
```
Validation of required fields failed error="resource plugin-sdk-test-cloudrun-service
of type GCP::CloudRun::Service is missing required fields: [traffic.percent template.containers.image]"
```

## What Changed

Commit `6d82c4d9` (fix(schema): support hasProviderDefault on nested sub-resource fields)
changed `isSubResourceType` in `formae.pkl` to also handle `Listing<SubResource>` types.

Before: `subHints` only traversed direct SubResource fields. Nested fields inside
`Listing<SubResource>` types (like `traffic: Listing<TrafficTarget>`) were not processed.

After: `subHints` now traverses into `Listing<SubResource>`, propagating hints including:
```pkl
required = !(v.type is reflect.NullableType)
```

This is the **same rule** used for all other properties — non-nullable = required. The fix
correctly propagated `hasProviderDefault` (the goal), but also propagated `Required` to
nested fields that the GCP API doesn't always return in Read responses.

## Why Only Some GCP Resources Fail

Failing resources have deeply nested non-nullable fields inside Listing sub-resources
(e.g., `traffic.percent: Int`, `template.containers.image: String`) that the GCP API
doesn't include in every response.

Passing resources (bucket, disk, network, subnetwork) either:
- Are synchronous (complete properties returned)
- Have no deeply nested required fields inside Listing sub-resources

## Possible Fixes (Discussion Points)

1. **GCP schema fix**: Make these fields nullable (`Int?`, `String?`) if the API doesn't
   guarantee they're present in every response. This is consistent with the codebase rule:
   non-nullable = required = must be present.

2. **GCP plugin fix**: Ensure async Status/Read handlers return complete properties
   including all nested fields.

3. **Core safety net**: The silent `return "", nil` in `validateRequiredFields` is dangerous
   regardless — a successful command that silently drops the resource is a data loss bug.
   At minimum this should be a warning log, or the validation should be softened for
   Read/Status responses (vs user-provided properties).

## Recommendation

The `required = !(v.type is reflect.NullableType)` rule is correct and consistent.
The issue is either in the GCP schemas (fields should be nullable) or the GCP plugin
(Read responses should be complete). Discuss with the GCP plugin author which applies.

The silent drop in `resource_persister.go` should be fixed separately as a core issue.

## BigQuery Table Extract Failure

The `table` conformance test fails at Step 6 (`formae extract`) with exit status 1.
This passed on March 20 (formae 0.82.3) but fails now. The table schema has a
self-referencing type: `SchemaField.fields: Listing<SchemaField>?`. This may hit
the same recursive schema issue that `subHints` had — the cycle detection fix in
#349 was for hint propagation, but extract likely uses a different code path that
also needs cycle detection.
