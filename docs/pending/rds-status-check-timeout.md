# RDS DBInstance: CloudControl status check hangs during re-create

## Problem

The PluginOperator's `GetResourceRequestStatus` call to CloudControl for
`AWS::RDS::DBInstance` occasionally takes >20 seconds during CREATE operations.
During this time, the PluginOperator sends no progress to the ResourceUpdater.
The ResourceUpdater's state timeout (2 × statusCheckInterval = 40s) fires and
marks the resource as "missing in action".

## Evidence (run 23577411093)

```
05:46:28 — PluginOperator starts create, IN_PROGRESS
05:46:48 — CheckStatus: still IN_PROGRESS → progress sent → next check in 20s
05:47:08 — CheckStatus fires, calls GetResourceRequestStatus... NO RESPONSE LOGGED
05:47:28 — ResourceUpdater timeout (40s since last progress) → "missing in action"
```

The first create (Steps 1-18) completed successfully — the re-create for OOB
delete test hit a slow CloudControl response.

## Impact

87/88 AWS conformance tests pass. Only `rds-dbinstance` flakes (~50% of runs)
at Step 20 (re-create for OOB delete test).

## Proposed fix (formae)

Two options (not mutually exclusive):
1. **PluginOperator keepalives**: Add a context timeout to the CloudControl
   status check API call. If it exceeds a threshold (e.g., 10s), send a
   keepalive/heartbeat message to the ResourceUpdater to reset the state
   timeout, then continue waiting.
2. **Cancel and retry**: If the status check exceeds a timeout (e.g., 15s),
   cancel the HTTP call and treat it as a retryable error. The PluginOperator
   schedules a new CheckStatus.

Option 2 is simpler and aligns with the existing retry model.
