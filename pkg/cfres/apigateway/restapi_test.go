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

func TestEnrichWithExecuteApiArn_HappyPath(t *testing.T) {
	ctx := context.Background()
	stsClient := &mockStsClient{}
	stsClient.On("GetCallerIdentity", ctx, mock.Anything).Return(&sts.GetCallerIdentityOutput{
		Account: aws.String("000000000000"),
		Arn:     aws.String("arn:aws:iam::000000000000:user/clanker"),
	}, nil)

	r := &RestApi{cfg: &config.Config{Region: "us-east-1"}}
	props := map[string]any{"RestApiId": "abc123"}

	r.enrichWithExecuteApiArn(ctx, stsClient, props)

	assert.Equal(t, "arn:aws:execute-api:us-east-1:000000000000:abc123/*/*", props["ExecuteApiArn"])
	stsClient.AssertExpectations(t)
}

func TestEnrichWithExecuteApiArn_GovPartitionFromCallerArn(t *testing.T) {
	ctx := context.Background()
	stsClient := &mockStsClient{}
	stsClient.On("GetCallerIdentity", ctx, mock.Anything).Return(&sts.GetCallerIdentityOutput{
		Account: aws.String("111122223333"),
		Arn:     aws.String("arn:aws-us-gov:iam::111122223333:role/clanker"),
	}, nil)

	r := &RestApi{cfg: &config.Config{Region: "us-gov-west-1"}}
	props := map[string]any{"RestApiId": "govapi"}

	r.enrichWithExecuteApiArn(ctx, stsClient, props)

	assert.Equal(t, "arn:aws-us-gov:execute-api:us-gov-west-1:111122223333:govapi/*/*", props["ExecuteApiArn"])
}

func TestEnrichWithExecuteApiArn_StsError_LeavesPropsUnenriched(t *testing.T) {
	ctx := context.Background()
	stsClient := &mockStsClient{}
	stsClient.On("GetCallerIdentity", ctx, mock.Anything).Return(nil, errors.New("sts unavailable"))

	r := &RestApi{cfg: &config.Config{Region: "us-east-1"}}
	props := map[string]any{"RestApiId": "abc123"}

	r.enrichWithExecuteApiArn(ctx, stsClient, props)

	_, present := props["ExecuteApiArn"]
	assert.False(t, present)
	stsClient.AssertExpectations(t)
}
