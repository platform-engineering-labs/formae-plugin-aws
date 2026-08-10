// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build integration

// Integration test for the one property unit tests cannot establish: that Aurora
// accepts the SCRAM verifier this package composes, and that the role it creates
// can actually authenticate with the plaintext the verifier was derived from.
//
// The catalog accepting a verifier is not the same as a login succeeding — a
// verifier composed from a differently-normalized password is stored happily and
// then fails every authentication attempt. The only way to close that gap is to
// connect as the role, which is what this test does, through the Data API with a
// role-scoped secret. It then rotates the password through Update and repeats,
// asserting the new password works and the old one no longer does.

package rds

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/rdsdata"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/config"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
)

// The cluster and its admin secret are supplied by the environment: this test
// needs a live Aurora PostgreSQL cluster with the Data API enabled, which the
// unit suite deliberately does not.
const (
	envClusterArn = "FORMAE_INTEGRATION_TEST_CLUSTER_ARN"
	envSecretArn  = "FORMAE_INTEGRATION_TEST_CLUSTER_SECRET_ARN"
)

func TestDatabaseRole_Integration_CreatedRoleCanAuthenticateAndRotate(t *testing.T) {
	region := os.Getenv("AWS_REGION")
	if region == "" {
		t.Skip("AWS_REGION not set; skipping integration test")
	}
	clusterArn := os.Getenv(envClusterArn)
	if clusterArn == "" {
		t.Skipf("%s not set; skipping", envClusterArn)
	}
	adminSecretArn := os.Getenv(envSecretArn)
	if adminSecretArn == "" {
		t.Skipf("%s not set; skipping", envSecretArn)
	}

	ctx := context.Background()
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	require.NoError(t, err)

	dataAPI := rdsdata.NewFromConfig(awsCfg)
	secrets := secretsmanager.NewFromConfig(awsCfg)

	provisioner := &DatabaseRole{cfg: &config.Config{Region: region}}

	roleName := fmt.Sprintf("formae_it_%s", uuid.New().String()[:8])
	firstPassword := "Fx7$kQ2mPz9!aW4t"
	secondPassword := "Rj5#nB8vLc3%hT6y"

	properties := func(password string) json.RawMessage {
		raw, marshalErr := json.Marshal(map[string]any{
			"ClusterArn":     clusterArn,
			"AdminSecretArn": adminSecretArn,
			"RoleName":       roleName,
			"Password":       password,
			"CanLogin":       true,
		})
		require.NoError(t, marshalErr)
		return raw
	}

	createResult, err := provisioner.Create(ctx, &resource.CreateRequest{Properties: properties(firstPassword)})
	require.NoError(t, err)
	require.Equal(t, resource.OperationStatusSuccess, createResult.ProgressResult.OperationStatus)
	nativeID := createResult.ProgressResult.NativeID

	t.Cleanup(func() {
		if _, cleanupErr := provisioner.Delete(context.Background(), &resource.DeleteRequest{NativeID: nativeID}); cleanupErr != nil {
			t.Logf("warning: failed to drop role %s: %v", roleName, cleanupErr)
		}
	})

	// Authenticating needs a secret in the RDS JSON format naming this role, so
	// the Data API can connect AS the role rather than as the admin.
	roleSecretArn := putRoleSecret(t, ctx, secrets, "role", roleName, firstPassword)

	assertAuthenticates(t, ctx, dataAPI, clusterArn, roleSecretArn,
		"the role must be able to log in with the password its verifier was derived from")

	// Rotate, then prove the new password works and the old one does not.
	_, err = provisioner.Update(ctx, &resource.UpdateRequest{
		NativeID:          nativeID,
		PriorProperties:   properties(firstPassword),
		DesiredProperties: properties(secondPassword),
	})
	require.NoError(t, err)

	staleSecretArn := roleSecretArn
	rotatedSecretArn := putRoleSecret(t, ctx, secrets, "role-rotated", roleName, secondPassword)

	assertAuthenticates(t, ctx, dataAPI, clusterArn, rotatedSecretArn,
		"the role must be able to log in with the rotated password")

	_, err = execute(ctx, dataAPI, clusterArn, staleSecretArn, "SELECT 1", nil)
	assert.Error(t, err, "the superseded password must no longer authenticate")
}

// putRoleSecret stores a role-scoped credential in the RDS JSON format and
// returns its ARN, so the Data API can connect as that role.
func putRoleSecret(t *testing.T, ctx context.Context, secrets *secretsmanager.Client, secretName, roleName, password string) string {
	t.Helper()

	payload, err := json.Marshal(map[string]string{"username": roleName, "password": password})
	require.NoError(t, err)

	name := fmt.Sprintf("formae-plugin-sdk-test-%s-%s", secretName, uuid.New().String()[:8])
	created, err := secrets.CreateSecret(ctx, &secretsmanager.CreateSecretInput{
		Name:         aws.String(name),
		SecretString: aws.String(string(payload)),
	})
	require.NoError(t, err)

	t.Cleanup(func() {
		_, deleteErr := secrets.DeleteSecret(context.Background(), &secretsmanager.DeleteSecretInput{
			SecretId:                   created.ARN,
			ForceDeleteWithoutRecovery: aws.Bool(true),
		})
		if deleteErr != nil {
			t.Logf("warning: failed to delete secret %s: %v", name, deleteErr)
		}
	})

	return aws.ToString(created.ARN)
}

// assertAuthenticates issues a trivial statement through the Data API using the
// given secret, retrying briefly: a freshly written secret is not always
// immediately readable, and a serverless cluster may need to resume.
func assertAuthenticates(t *testing.T, ctx context.Context, client dataAPIClient, clusterArn, secretArn, msg string) {
	t.Helper()

	var lastErr error
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		if _, err := execute(ctx, client, clusterArn, secretArn, "SELECT 1", nil); err == nil {
			return
		} else {
			lastErr = err
		}
		time.Sleep(5 * time.Second)
	}

	require.NoError(t, lastErr, msg)
}
