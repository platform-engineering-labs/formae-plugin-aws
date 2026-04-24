// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build integration

// Integration test exercising the full CRUD lifecycle of
// AWS::Lambda::EventInvokeConfig against live AWS, end-to-end through the
// provisioner registry (the path real plugin callers take).
//
// This test originally answered the diagnostic question "does CloudControl's
// UpdateResource work for this resource type" by hitting ccx.Client
// directly. It answered "no" — CC rejects any Update patch because it
// re-validates the full post-patch state, and server-side state contains
// CC-materialized empty OnSuccess:{}/OnFailure:{} sub-objects that fail
// the schema's "if present, Destination is required" rule even when the
// patch never touches DestinationConfig.
//
// The test now runs through the registered provisioner, which routes
// Update through a native-SDK UpdateFunctionEventInvokeConfig call that
// sidesteps the CC validator. Create / Read / Delete continue on the CC
// path because CC works fine for those. Running this test therefore:
//
//   - Passes when the custom Update provisioner is present and working,
//     giving us a live-AWS regression guard.
//   - Fails loudly if someone ever removes the provisioner or CC's broken
//     Update starts being used again.
//
// It also serves as a tripwire for the day AWS fixes CC's EventInvokeConfig
// Update handler: at that point we can drop the custom provisioner and
// the test will still pass via pure CC.

package lambda

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"archive/zip"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	awslambda "github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/ccx"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/config"
)

const (
	eicResourceType = "AWS::Lambda::EventInvokeConfig"
	// How long we're willing to wait for a CC request to reach a terminal
	// state. CC Create/Delete/Update on EventInvokeConfig should resolve
	// in seconds; we allow 2 min as a generous ceiling before declaring
	// hang — which IS the signal we're looking for here.
	ccStatusTimeout = 2 * time.Minute
)

func uniqueSuffix() string {
	return strings.ReplaceAll(uuid.New().String()[:8], "-", "")
}

// setupLambdaFunction provisions a minimal Lambda function (Python 3.12
// echo handler) plus its execution role, both of which are required
// before an EventInvokeConfig can be created. Registers t.Cleanup
// handlers to tear them down regardless of test outcome. Returns the
// function name, which is what CC Create/Update takes as the parent
// identifier on EventInvokeConfig.
func setupLambdaFunction(t *testing.T) string {
	t.Helper()
	suffix := uniqueSuffix()
	roleName := "formae-itest-eic-role-" + suffix
	functionName := "formae-itest-eic-" + suffix

	awsCfg, err := awsconfig.LoadDefaultConfig(context.Background())
	require.NoError(t, err, "loading AWS config")

	iamClient := iam.NewFromConfig(awsCfg)
	lambdaClient := awslambda.NewFromConfig(awsCfg)

	// --- IAM role ---
	trustPolicy := `{
        "Version": "2012-10-17",
        "Statement": [{
            "Effect": "Allow",
            "Principal": {"Service": "lambda.amazonaws.com"},
            "Action": "sts:AssumeRole"
        }]
    }`
	_, err = iamClient.CreateRole(context.Background(), &iam.CreateRoleInput{
		RoleName:                 aws.String(roleName),
		AssumeRolePolicyDocument: aws.String(trustPolicy),
	})
	require.NoError(t, err, "creating IAM role")
	t.Cleanup(func() {
		t.Logf("Cleaning up IAM role %s", roleName)
		_, err := iamClient.DeleteRole(context.Background(), &iam.DeleteRoleInput{
			RoleName: aws.String(roleName),
		})
		if err != nil {
			t.Logf("Warning: deleting IAM role: %v", err)
		}
	})

	// Fetch the ARN back — CreateRole returns it but we re-get to decouple.
	getRoleOut, err := iamClient.GetRole(context.Background(), &iam.GetRoleInput{
		RoleName: aws.String(roleName),
	})
	require.NoError(t, err, "getting IAM role")
	roleArn := aws.ToString(getRoleOut.Role.Arn)

	// IAM propagation: freshly-created roles aren't immediately usable by
	// Lambda's service-to-service assume-role check. A few seconds of
	// slack here avoids a flaky InvalidParameterValueException on CreateFunction.
	time.Sleep(10 * time.Second)

	// --- Lambda function ---
	// Minimal in-memory zip: handler.py with a no-op lambda_handler.
	zipBytes := buildLambdaZip(t)
	_, err = lambdaClient.CreateFunction(context.Background(), &awslambda.CreateFunctionInput{
		FunctionName: aws.String(functionName),
		Runtime:      "python3.12",
		Role:         aws.String(roleArn),
		Handler:      aws.String("handler.lambda_handler"),
		Code:         &lambdatypes.FunctionCode{ZipFile: zipBytes},
	})
	// CreateFunction can race role-propagation; one retry after another
	// short sleep if the role isn't valid yet.
	if err != nil {
		if strings.Contains(err.Error(), "InvalidParameterValueException") ||
			strings.Contains(err.Error(), "cannot be assumed") {
			time.Sleep(10 * time.Second)
			_, err = lambdaClient.CreateFunction(context.Background(), &awslambda.CreateFunctionInput{
				FunctionName: aws.String(functionName),
				Runtime:      "python3.12",
				Role:         aws.String(roleArn),
				Handler:      aws.String("handler.lambda_handler"),
				Code:         &lambdatypes.FunctionCode{ZipFile: zipBytes},
			})
		}
	}
	require.NoError(t, err, "creating Lambda function")
	t.Cleanup(func() {
		t.Logf("Cleaning up Lambda function %s", functionName)
		_, err := lambdaClient.DeleteFunction(context.Background(), &awslambda.DeleteFunctionInput{
			FunctionName: aws.String(functionName),
		})
		if err != nil {
			t.Logf("Warning: deleting Lambda function: %v", err)
		}
	})

	return functionName
}

func buildLambdaZip(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	f, err := w.Create("handler.py")
	require.NoError(t, err, "creating zip entry")
	_, err = f.Write([]byte("def lambda_handler(event, context):\n    return {}\n"))
	require.NoError(t, err, "writing zip entry")
	require.NoError(t, w.Close(), "closing zip")
	return buf.Bytes()
}

// waitForCCStatus polls CloudControl until the given RequestID reaches a
// terminal state (Success or Failed). Fails the test on timeout so a
// hang is surfaced as a test failure rather than a flake.
func waitForCCStatus(t *testing.T, client *ccx.Client, requestID, nativeID string) *resource.StatusResult {
	t.Helper()
	deadline := time.After(ccStatusTimeout)
	readFunc := func(ctx context.Context, req *resource.ReadRequest) (*resource.ReadResult, error) {
		return client.ReadResource(ctx, req)
	}
	for {
		select {
		case <-deadline:
			t.Fatalf("CC request %s did not reach terminal state within %s", requestID, ccStatusTimeout)
			return nil
		default:
		}
		statusRes, err := client.StatusResource(context.Background(), &resource.StatusRequest{
			RequestID:    requestID,
			NativeID:     nativeID,
			ResourceType: eicResourceType,
		}, readFunc)
		require.NoError(t, err, "StatusResource")
		switch statusRes.ProgressResult.OperationStatus {
		case resource.OperationStatusSuccess:
			return statusRes
		case resource.OperationStatusFailure:
			t.Fatalf("CC request %s failed: code=%s msg=%s",
				requestID,
				statusRes.ProgressResult.ErrorCode,
				statusRes.ProgressResult.StatusMessage)
			return nil
		}
		time.Sleep(2 * time.Second)
	}
}

// TestEventInvokeConfig_CRUD_Lifecycle runs the full Create / Read /
// Update / Delete lifecycle against live AWS, using CloudControl for
// Create / Read / Delete and the custom provisioner for Update (the
// same routing the plugin uses at runtime). Passing proves the custom
// Update provisioner works end-to-end; regressing to the CC Update
// path would trip the test on the known "DestinationConfig/OnSuccess:
// required key [Destination] not found" ValidationException.
func TestEventInvokeConfig_CRUD_Lifecycle(t *testing.T) {
	functionName := setupLambdaFunction(t)

	client, err := ccx.NewClient(&config.Config{})
	require.NoError(t, err, "creating ccx client")

	// --- CREATE ---
	// Matches the conformance fixture: $LATEST qualifier, 2 retries, 3600s age.
	createProps := map[string]any{
		"FunctionName":             functionName,
		"Qualifier":                "$LATEST",
		"MaximumRetryAttempts":     2,
		"MaximumEventAgeInSeconds": 3600,
	}
	createPropsBytes, err := json.Marshal(createProps)
	require.NoError(t, err, "marshaling create props")

	t.Logf("CREATE: CC create %s with props=%s", eicResourceType, string(createPropsBytes))
	createRes, err := client.CreateResource(context.Background(), &resource.CreateRequest{
		ResourceType: eicResourceType,
		Label:        "formae-itest-eic",
		Properties:   createPropsBytes,
	})
	require.NoError(t, err, "CC CreateResource")
	require.NotNil(t, createRes, "CreateResult nil")
	require.NotNil(t, createRes.ProgressResult, "CreateResult.ProgressResult nil")
	nativeID := createRes.ProgressResult.NativeID
	if createRes.ProgressResult.OperationStatus != resource.OperationStatusSuccess {
		require.NotEmpty(t, createRes.ProgressResult.RequestID, "CC Create returned no RequestID for async polling")
		t.Logf("CREATE: async, RequestID=%s, polling", createRes.ProgressResult.RequestID)
		waitForCCStatus(t, client, createRes.ProgressResult.RequestID, nativeID)
	}
	require.NotEmpty(t, nativeID, "no NativeID after create")
	t.Logf("CREATE: nativeID=%s", nativeID)

	// Register a belt-and-suspenders cleanup so a mid-test failure still
	// removes the EIC. The final delete step below duplicates this if
	// everything goes well.
	deletedOnce := false
	t.Cleanup(func() {
		if deletedOnce {
			return
		}
		t.Logf("Cleaning up EIC %s (final delete not reached)", nativeID)
		_, _ = client.DeleteResource(context.Background(), &resource.DeleteRequest{
			NativeID:     nativeID,
			ResourceType: eicResourceType,
		})
	})

	// --- READ after create ---
	readRes, err := client.ReadResource(context.Background(), &resource.ReadRequest{
		NativeID:     nativeID,
		ResourceType: eicResourceType,
	})
	require.NoError(t, err, "CC ReadResource after create")
	require.NotNil(t, readRes, "ReadResult nil after create")
	var readProps map[string]any
	require.NoError(t, json.Unmarshal([]byte(readRes.Properties), &readProps), "parsing read props")
	assert.EqualValues(t, 2, readProps["MaximumRetryAttempts"], "create: MaximumRetryAttempts mismatch")
	assert.EqualValues(t, 3600, readProps["MaximumEventAgeInSeconds"], "create: MaximumEventAgeInSeconds mismatch")
	t.Logf("READ post-create: %s", readRes.Properties)

	// --- UPDATE ---
	// Route Update through the custom provisioner — this is what
	// AWS::Lambda::EventInvokeConfig gets in a real apply, because the
	// type is registered for OperationUpdate. Intentionally does NOT go
	// through ccx.Client.UpdateResource: CC's Update handler rejects
	// with "DestinationConfig/OnSuccess: required key [Destination] not
	// found" whenever server-side state has the (CC-materialized) empty
	// sub-objects, regardless of whether the patch touches them.
	desiredProps := map[string]any{
		"FunctionName":             functionName,
		"Qualifier":                "$LATEST",
		"MaximumRetryAttempts":     0,
		"MaximumEventAgeInSeconds": 60,
	}
	desiredPropsBytes, err := json.Marshal(desiredProps)
	require.NoError(t, err, "marshaling desired props")

	t.Logf("UPDATE: provisioner update %s", nativeID)
	eic := &EventInvokeConfig{cfg: &config.Config{}}
	updateRes, err := eic.Update(context.Background(), &resource.UpdateRequest{
		NativeID:          nativeID,
		ResourceType:      eicResourceType,
		Label:             "formae-itest-eic",
		PriorProperties:   createPropsBytes,
		DesiredProperties: desiredPropsBytes,
	})
	require.NoError(t, err, "Update via custom provisioner")
	require.NotNil(t, updateRes, "UpdateResult nil")
	require.NotNil(t, updateRes.ProgressResult, "UpdateResult.ProgressResult nil")
	assert.Equal(t, resource.OperationStatusSuccess, updateRes.ProgressResult.OperationStatus,
		"Update should return synchronous success — the native Lambda SDK call is not async")
	t.Logf("UPDATE: done")

	// --- READ after update ---
	readRes, err = client.ReadResource(context.Background(), &resource.ReadRequest{
		NativeID:     nativeID,
		ResourceType: eicResourceType,
	})
	require.NoError(t, err, "CC ReadResource after update")
	require.NoError(t, json.Unmarshal([]byte(readRes.Properties), &readProps), "parsing read props after update")
	assert.EqualValues(t, 0, readProps["MaximumRetryAttempts"], "update: MaximumRetryAttempts not applied")
	assert.EqualValues(t, 60, readProps["MaximumEventAgeInSeconds"], "update: MaximumEventAgeInSeconds not applied")
	t.Logf("READ post-update: %s", readRes.Properties)

	// --- DELETE ---
	t.Logf("DELETE: CC delete %s", nativeID)
	deleteRes, err := client.DeleteResource(context.Background(), &resource.DeleteRequest{
		NativeID:     nativeID,
		ResourceType: eicResourceType,
	})
	require.NoError(t, err, "CC DeleteResource")
	require.NotNil(t, deleteRes, "DeleteResult nil")
	require.NotNil(t, deleteRes.ProgressResult, "DeleteResult.ProgressResult nil")
	if deleteRes.ProgressResult.OperationStatus != resource.OperationStatusSuccess {
		require.NotEmpty(t, deleteRes.ProgressResult.RequestID, "CC Delete returned no RequestID for async polling")
		waitForCCStatus(t, client, deleteRes.ProgressResult.RequestID, nativeID)
	}
	deletedOnce = true
	t.Logf("DELETE: done")

	// NativeID format sanity: should be FunctionName|Qualifier per
	// cfn-schema. Fail loudly if it ever changes shape.
	assert.Equal(t,
		fmt.Sprintf("%s|$LATEST", functionName),
		nativeID,
		"unexpected NativeID shape — update this test if cfn-schema changed")
}
