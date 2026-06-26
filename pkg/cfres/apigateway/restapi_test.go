// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package apigateway

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/config"
)

// synthesizeExecuteApiArn derives the execute-api ARN
// arn:<partition>:execute-api:<region>:<account>:<restApiId>/*/* and writes it
// to props as ExecuteApiArn. CloudControl's read model for
// AWS::ApiGateway::RestApi returns only RestApiId/RootResourceId, so the ARN a
// Lambda invoke Permission needs for sourceArn has to be synthesized.

func TestSynthesizeExecuteApiArn_HappyPath(t *testing.T) {
	props := map[string]any{"RestApiId": "abc123"}

	set := synthesizeExecuteApiArn(props, "aws", "us-east-1", "000000000000")

	assert.True(t, set)
	assert.Equal(t, "arn:aws:execute-api:us-east-1:000000000000:abc123/*/*", props["ExecuteApiArn"])
}

func TestSynthesizeExecuteApiArn_GovPartition(t *testing.T) {
	props := map[string]any{"RestApiId": "govapi"}

	set := synthesizeExecuteApiArn(props, "aws-us-gov", "us-gov-west-1", "111122223333")

	assert.True(t, set)
	assert.Equal(t, "arn:aws-us-gov:execute-api:us-gov-west-1:111122223333:govapi/*/*", props["ExecuteApiArn"])
}

func TestSynthesizeExecuteApiArn_MissingRestApiId_NoKey(t *testing.T) {
	props := map[string]any{"RootResourceId": "root"}

	set := synthesizeExecuteApiArn(props, "aws", "us-east-1", "000000000000")

	assert.False(t, set)
	_, present := props["ExecuteApiArn"]
	assert.False(t, present)
}

func TestSynthesizeExecuteApiArn_EmptyRegion_NoKey(t *testing.T) {
	props := map[string]any{"RestApiId": "abc123"}

	set := synthesizeExecuteApiArn(props, "aws", "", "000000000000")

	assert.False(t, set)
	_, present := props["ExecuteApiArn"]
	assert.False(t, present)
}

func TestSynthesizeExecuteApiArn_EmptyAccount_NoKey(t *testing.T) {
	props := map[string]any{"RestApiId": "abc123"}

	set := synthesizeExecuteApiArn(props, "aws", "us-east-1", "")

	assert.False(t, set)
	_, present := props["ExecuteApiArn"]
	assert.False(t, present)
}

func TestSynthesizeExecuteApiArn_EmptyPartition_NoKey(t *testing.T) {
	props := map[string]any{"RestApiId": "abc123"}

	set := synthesizeExecuteApiArn(props, "", "us-east-1", "000000000000")

	assert.False(t, set)
	_, present := props["ExecuteApiArn"]
	assert.False(t, present)
}

func TestSynthesizeExecuteApiArn_ExistingKeyNotOverwritten(t *testing.T) {
	props := map[string]any{
		"RestApiId":     "abc123",
		"ExecuteApiArn": "arn:aws:execute-api:us-east-1:000000000000:preexisting/*/*",
	}

	set := synthesizeExecuteApiArn(props, "aws", "us-east-1", "999999999999")

	assert.False(t, set)
	assert.Equal(t, "arn:aws:execute-api:us-east-1:000000000000:preexisting/*/*", props["ExecuteApiArn"])
}

func TestPartitionFromArn(t *testing.T) {
	assert.Equal(t, "aws", partitionFromArn("arn:aws:iam::000000000000:user/clanker"))
	assert.Equal(t, "aws-us-gov", partitionFromArn("arn:aws-us-gov:iam::000000000000:user/clanker"))
	assert.Equal(t, "aws-cn", partitionFromArn("arn:aws-cn:iam::000000000000:user/clanker"))
	// Unparseable values default to the standard partition rather than dropping the ARN.
	assert.Equal(t, "aws", partitionFromArn("not-an-arn"))
	assert.Equal(t, "aws", partitionFromArn(""))
}

// staticFactory returns an stsClientFactory that always yields the given client.
func staticFactory(c stsClientInterface) func(*config.Config) (stsClientInterface, error) {
	return func(*config.Config) (stsClientInterface, error) { return c, nil }
}

func TestEnrichWithExecuteApiArn_HappyPath(t *testing.T) {
	resetIdentityCache()
	ctx := context.Background()
	stsClient := &mockStsClient{}
	stsClient.On("GetCallerIdentity", ctx, mock.Anything).Return(&sts.GetCallerIdentityOutput{
		Account: aws.String("000000000000"),
		Arn:     aws.String("arn:aws:iam::000000000000:user/clanker"),
	}, nil)

	r := &RestApi{cfg: &config.Config{Region: "us-east-1"}, stsClientFactory: staticFactory(stsClient)}
	props := map[string]any{"RestApiId": "abc123"}

	r.enrichWithExecuteApiArn(ctx, props)

	assert.Equal(t, "arn:aws:execute-api:us-east-1:000000000000:abc123/*/*", props["ExecuteApiArn"])
	stsClient.AssertExpectations(t)
}

func TestEnrichWithExecuteApiArn_GovPartitionFromCallerArn(t *testing.T) {
	resetIdentityCache()
	ctx := context.Background()
	stsClient := &mockStsClient{}
	stsClient.On("GetCallerIdentity", ctx, mock.Anything).Return(&sts.GetCallerIdentityOutput{
		Account: aws.String("111122223333"),
		Arn:     aws.String("arn:aws-us-gov:iam::111122223333:role/clanker"),
	}, nil)

	r := &RestApi{cfg: &config.Config{Region: "us-gov-west-1"}, stsClientFactory: staticFactory(stsClient)}
	props := map[string]any{"RestApiId": "govapi"}

	r.enrichWithExecuteApiArn(ctx, props)

	assert.Equal(t, "arn:aws-us-gov:execute-api:us-gov-west-1:111122223333:govapi/*/*", props["ExecuteApiArn"])
}

func TestEnrichWithExecuteApiArn_StsError_LeavesPropsUnenriched(t *testing.T) {
	resetIdentityCache()
	ctx := context.Background()
	stsClient := &mockStsClient{}
	stsClient.On("GetCallerIdentity", ctx, mock.Anything).Return(nil, errors.New("sts unavailable"))

	r := &RestApi{cfg: &config.Config{Region: "us-east-1"}, stsClientFactory: staticFactory(stsClient)}
	props := map[string]any{"RestApiId": "abc123"}

	r.enrichWithExecuteApiArn(ctx, props)

	_, present := props["ExecuteApiArn"]
	assert.False(t, present)
	stsClient.AssertExpectations(t)
}

// Account and partition are invariant for a credential set, so the derived ARN
// must come from a memoized GetCallerIdentity rather than one STS call per Read.
// Each Read dispatch builds a fresh provisioner (registry.Get), so independent
// instances sharing the same credentials must still resolve the identity once.
func TestResolveCallerIdentity_MemoizedAcrossReads(t *testing.T) {
	resetIdentityCache()
	ctx := context.Background()
	stsClient := &mockStsClient{}
	stsClient.On("GetCallerIdentity", ctx, mock.Anything).Return(&sts.GetCallerIdentityOutput{
		Account: aws.String("000000000000"),
		Arn:     aws.String("arn:aws:iam::000000000000:user/clanker"),
	}, nil).Once()

	factory := staticFactory(stsClient)
	first := &RestApi{cfg: &config.Config{Region: "eu-west-1"}, stsClientFactory: factory}
	second := &RestApi{cfg: &config.Config{Region: "eu-west-1"}, stsClientFactory: factory}

	props1 := map[string]any{"RestApiId": "abc123"}
	props2 := map[string]any{"RestApiId": "def456"}
	first.enrichWithExecuteApiArn(ctx, props1)
	second.enrichWithExecuteApiArn(ctx, props2)

	assert.Equal(t, "arn:aws:execute-api:eu-west-1:000000000000:abc123/*/*", props1["ExecuteApiArn"])
	assert.Equal(t, "arn:aws:execute-api:eu-west-1:000000000000:def456/*/*", props2["ExecuteApiArn"])
	stsClient.AssertNumberOfCalls(t, "GetCallerIdentity", 1)
}

// A different credential set (distinct profile/region) is a distinct cache key,
// so it resolves its own identity rather than reusing another set's account.
func TestResolveCallerIdentity_DistinctCredentialsNotShared(t *testing.T) {
	resetIdentityCache()
	ctx := context.Background()

	clientA := &mockStsClient{}
	clientA.On("GetCallerIdentity", ctx, mock.Anything).Return(&sts.GetCallerIdentityOutput{
		Account: aws.String("000000000000"),
		Arn:     aws.String("arn:aws:iam::000000000000:user/a"),
	}, nil).Once()
	clientB := &mockStsClient{}
	clientB.On("GetCallerIdentity", ctx, mock.Anything).Return(&sts.GetCallerIdentityOutput{
		Account: aws.String("111122223333"),
		Arn:     aws.String("arn:aws:iam::111122223333:user/b"),
	}, nil).Once()

	a := &RestApi{cfg: &config.Config{Profile: "a", Region: "us-east-1"}, stsClientFactory: staticFactory(clientA)}
	b := &RestApi{cfg: &config.Config{Profile: "b", Region: "us-east-1"}, stsClientFactory: staticFactory(clientB)}

	propsA := map[string]any{"RestApiId": "apiA"}
	propsB := map[string]any{"RestApiId": "apiB"}
	a.enrichWithExecuteApiArn(ctx, propsA)
	b.enrichWithExecuteApiArn(ctx, propsB)

	assert.Equal(t, "arn:aws:execute-api:us-east-1:000000000000:apiA/*/*", propsA["ExecuteApiArn"])
	assert.Equal(t, "arn:aws:execute-api:us-east-1:111122223333:apiB/*/*", propsB["ExecuteApiArn"])
	clientA.AssertExpectations(t)
	clientB.AssertExpectations(t)
}
