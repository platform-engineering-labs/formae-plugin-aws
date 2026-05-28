// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package ecs

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"testing"

	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	awselbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbv2types "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
)

// testLogger returns a slog.Logger discarding all output, suitable for use in
// table-driven tests that don't assert on log content.
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func TestComposeEndpoints_EmptyLoadBalancers(t *testing.T) {
	cli := &mockELBv2Client{}
	// Expect zero API calls when there are no LB entries.

	result := composeEndpoints(context.Background(), nil, cli, testLogger())

	assert.NotNil(t, result.Endpoints)
	assert.Empty(t, result.Endpoints)
	assert.NoError(t, result.TransientError)
	cli.AssertExpectations(t)

	// Repeat with empty (non-nil) slice.
	result2 := composeEndpoints(context.Background(), []ecstypes.LoadBalancer{}, cli, testLogger())
	assert.Empty(t, result2.Endpoints)
	assert.NoError(t, result2.TransientError)
}

// ptr returns a pointer to v. Helper for AWS SDK input/output types
// where most fields are *T.
func ptr[T any](v T) *T { return &v }

func TestComposeEndpoints_SingleEntryHTTPS(t *testing.T) {
	ctx := context.Background()
	cli := &mockELBv2Client{}

	tgArn := "arn:aws:elasticloadbalancing:us-east-1:123:targetgroup/grafana-tg/abc"
	albArn := "arn:aws:elasticloadbalancing:us-east-1:123:loadbalancer/app/lgtm-alb/def"
	listenerArn := "arn:aws:elasticloadbalancing:us-east-1:123:listener/app/lgtm-alb/def/g"

	cli.On("DescribeTargetGroups", ctx, &awselbv2.DescribeTargetGroupsInput{
		TargetGroupArns: []string{tgArn},
	}).Return(&awselbv2.DescribeTargetGroupsOutput{
		TargetGroups: []elbv2types.TargetGroup{
			{TargetGroupArn: ptr(tgArn), LoadBalancerArns: []string{albArn}},
		},
	}, nil)

	cli.On("DescribeLoadBalancers", ctx, &awselbv2.DescribeLoadBalancersInput{
		LoadBalancerArns: []string{albArn},
	}).Return(&awselbv2.DescribeLoadBalancersOutput{
		LoadBalancers: []elbv2types.LoadBalancer{
			{
				LoadBalancerArn: ptr(albArn),
				DNSName:         ptr("lgtm-1234.us-east-1.elb.amazonaws.com"),
				Type:            elbv2types.LoadBalancerTypeEnumApplication,
			},
		},
	}, nil)

	cli.On("DescribeListeners", ctx, &awselbv2.DescribeListenersInput{
		LoadBalancerArn: ptr(albArn),
	}).Return(&awselbv2.DescribeListenersOutput{
		Listeners: []elbv2types.Listener{
			{
				ListenerArn: ptr(listenerArn),
				Port:        ptr(int32(443)),
				Protocol:    elbv2types.ProtocolEnumHttps,
				DefaultActions: []elbv2types.Action{
					{Type: elbv2types.ActionTypeEnumForward, TargetGroupArn: ptr(tgArn)},
				},
			},
		},
	}, nil)

	lbs := []ecstypes.LoadBalancer{
		{ContainerName: ptr("grafana"), ContainerPort: ptr(int32(3000)), TargetGroupArn: ptr(tgArn)},
	}

	result := composeEndpoints(ctx, lbs, cli, testLogger())

	assert.NoError(t, result.TransientError)
	assert.Equal(t, map[string]string{
		"grafana:3000": "https://lgtm-1234.us-east-1.elb.amazonaws.com:443",
	}, result.Endpoints)
	cli.AssertExpectations(t)
}

// captureLogs returns a logger that writes to the returned buffer, plus the buffer.
// Used by tests asserting on warn-level skip events.
func captureLogs() (*slog.Logger, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	h := slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	return slog.New(h), buf
}

// awsAPIErr constructs a smithy.APIError with the given code/message for tests
// of error classification. Uses smithy.GenericAPIError directly so we exercise
// the same interface the production code's isTransient/isAccessDenied uses.
func awsAPIErr(code, message string) error {
	return &smithy.GenericAPIError{Code: code, Message: message}
}

func TestComposeEndpoints_TGNotInResponse(t *testing.T) {
	ctx := context.Background()
	cli := &mockELBv2Client{}
	tgArn := "arn:aws:elasticloadbalancing:us-east-1:123:targetgroup/missing/abc"

	cli.On("DescribeTargetGroups", ctx, &awselbv2.DescribeTargetGroupsInput{TargetGroupArns: []string{tgArn}}).
		Return(&awselbv2.DescribeTargetGroupsOutput{TargetGroups: nil}, nil)

	lbs := []ecstypes.LoadBalancer{
		{ContainerName: ptr("app"), ContainerPort: ptr(int32(80)), TargetGroupArn: ptr(tgArn)},
	}

	logger, logs := captureLogs()
	result := composeEndpoints(ctx, lbs, cli, logger)

	assert.NoError(t, result.TransientError)
	assert.Empty(t, result.Endpoints)
	assert.Contains(t, logs.String(), "TG not found")
}

func TestComposeEndpoints_TGAttachedToZeroLBs(t *testing.T) {
	ctx := context.Background()
	cli := &mockELBv2Client{}
	tgArn := "arn:aws:elasticloadbalancing:us-east-1:123:targetgroup/orphan/abc"

	cli.On("DescribeTargetGroups", ctx, &awselbv2.DescribeTargetGroupsInput{TargetGroupArns: []string{tgArn}}).
		Return(&awselbv2.DescribeTargetGroupsOutput{
			TargetGroups: []elbv2types.TargetGroup{
				{TargetGroupArn: ptr(tgArn), LoadBalancerArns: nil},
			},
		}, nil)

	lbs := []ecstypes.LoadBalancer{
		{ContainerName: ptr("app"), ContainerPort: ptr(int32(80)), TargetGroupArn: ptr(tgArn)},
	}

	logger, logs := captureLogs()
	result := composeEndpoints(ctx, lbs, cli, logger)

	assert.NoError(t, result.TransientError)
	assert.Empty(t, result.Endpoints)
	assert.Contains(t, logs.String(), "attached to zero LBs")
}

func TestComposeEndpoints_TGOnlyOnNLB(t *testing.T) {
	ctx := context.Background()
	cli := &mockELBv2Client{}
	tgArn := "arn:aws:elasticloadbalancing:us-east-1:123:targetgroup/nlb-tg/abc"
	nlbArn := "arn:aws:elasticloadbalancing:us-east-1:123:loadbalancer/net/nlb/def"

	cli.On("DescribeTargetGroups", ctx, &awselbv2.DescribeTargetGroupsInput{TargetGroupArns: []string{tgArn}}).
		Return(&awselbv2.DescribeTargetGroupsOutput{
			TargetGroups: []elbv2types.TargetGroup{
				{TargetGroupArn: ptr(tgArn), LoadBalancerArns: []string{nlbArn}},
			},
		}, nil)
	cli.On("DescribeLoadBalancers", ctx, &awselbv2.DescribeLoadBalancersInput{LoadBalancerArns: []string{nlbArn}}).
		Return(&awselbv2.DescribeLoadBalancersOutput{
			LoadBalancers: []elbv2types.LoadBalancer{
				{LoadBalancerArn: ptr(nlbArn), DNSName: ptr("nlb-dns"), Type: elbv2types.LoadBalancerTypeEnumNetwork},
			},
		}, nil)
	cli.On("DescribeListeners", ctx, &awselbv2.DescribeListenersInput{LoadBalancerArn: ptr(nlbArn)}).
		Return(&awselbv2.DescribeListenersOutput{Listeners: nil}, nil)

	lbs := []ecstypes.LoadBalancer{
		{ContainerName: ptr("app"), ContainerPort: ptr(int32(80)), TargetGroupArn: ptr(tgArn)},
	}

	logger, logs := captureLogs()
	result := composeEndpoints(ctx, lbs, cli, logger)

	assert.NoError(t, result.TransientError)
	assert.Empty(t, result.Endpoints)
	assert.Contains(t, logs.String(), "attached to no ALB")
}

func TestComposeEndpoints_MultiALB(t *testing.T) {
	ctx := context.Background()
	cli := &mockELBv2Client{}
	tgArn := "arn:aws:elasticloadbalancing:us-east-1:123:targetgroup/shared/abc"
	albA := "arn:aws:elasticloadbalancing:us-east-1:123:loadbalancer/app/a/d1"
	albB := "arn:aws:elasticloadbalancing:us-east-1:123:loadbalancer/app/b/d2"

	cli.On("DescribeTargetGroups", ctx, &awselbv2.DescribeTargetGroupsInput{TargetGroupArns: []string{tgArn}}).
		Return(&awselbv2.DescribeTargetGroupsOutput{
			TargetGroups: []elbv2types.TargetGroup{
				{TargetGroupArn: ptr(tgArn), LoadBalancerArns: []string{albA, albB}},
			},
		}, nil)
	cli.On("DescribeLoadBalancers", ctx, &awselbv2.DescribeLoadBalancersInput{LoadBalancerArns: []string{albA, albB}}).
		Return(&awselbv2.DescribeLoadBalancersOutput{
			LoadBalancers: []elbv2types.LoadBalancer{
				{LoadBalancerArn: ptr(albA), DNSName: ptr("a-dns"), Type: elbv2types.LoadBalancerTypeEnumApplication},
				{LoadBalancerArn: ptr(albB), DNSName: ptr("b-dns"), Type: elbv2types.LoadBalancerTypeEnumApplication},
			},
		}, nil)
	cli.On("DescribeListeners", ctx, &awselbv2.DescribeListenersInput{LoadBalancerArn: ptr(albA)}).
		Return(&awselbv2.DescribeListenersOutput{Listeners: nil}, nil)
	cli.On("DescribeListeners", ctx, &awselbv2.DescribeListenersInput{LoadBalancerArn: ptr(albB)}).
		Return(&awselbv2.DescribeListenersOutput{Listeners: nil}, nil)

	lbs := []ecstypes.LoadBalancer{
		{ContainerName: ptr("app"), ContainerPort: ptr(int32(80)), TargetGroupArn: ptr(tgArn)},
	}

	logger, logs := captureLogs()
	result := composeEndpoints(ctx, lbs, cli, logger)

	assert.NoError(t, result.TransientError)
	assert.Empty(t, result.Endpoints)
	assert.Contains(t, logs.String(), "multiple ALBs front the same TG")
}

func TestComposeEndpoints_NoForwardAction(t *testing.T) {
	ctx := context.Background()
	cli := &mockELBv2Client{}
	tgArn := "arn:aws:elasticloadbalancing:us-east-1:123:targetgroup/tg/abc"
	albArn := "arn:aws:elasticloadbalancing:us-east-1:123:loadbalancer/app/a/d1"

	cli.On("DescribeTargetGroups", ctx, &awselbv2.DescribeTargetGroupsInput{TargetGroupArns: []string{tgArn}}).
		Return(&awselbv2.DescribeTargetGroupsOutput{
			TargetGroups: []elbv2types.TargetGroup{
				{TargetGroupArn: ptr(tgArn), LoadBalancerArns: []string{albArn}},
			},
		}, nil)
	cli.On("DescribeLoadBalancers", ctx, &awselbv2.DescribeLoadBalancersInput{LoadBalancerArns: []string{albArn}}).
		Return(&awselbv2.DescribeLoadBalancersOutput{
			LoadBalancers: []elbv2types.LoadBalancer{
				{LoadBalancerArn: ptr(albArn), DNSName: ptr("a-dns"), Type: elbv2types.LoadBalancerTypeEnumApplication},
			},
		}, nil)
	cli.On("DescribeListeners", ctx, &awselbv2.DescribeListenersInput{LoadBalancerArn: ptr(albArn)}).
		Return(&awselbv2.DescribeListenersOutput{
			Listeners: []elbv2types.Listener{
				{
					ListenerArn:    ptr("listener-1"),
					Port:           ptr(int32(80)),
					Protocol:       elbv2types.ProtocolEnumHttp,
					DefaultActions: []elbv2types.Action{{Type: elbv2types.ActionTypeEnumRedirect}},
				},
			},
		}, nil)

	lbs := []ecstypes.LoadBalancer{
		{ContainerName: ptr("app"), ContainerPort: ptr(int32(80)), TargetGroupArn: ptr(tgArn)},
	}

	logger, logs := captureLogs()
	result := composeEndpoints(ctx, lbs, cli, logger)

	assert.NoError(t, result.TransientError)
	assert.Empty(t, result.Endpoints)
	assert.Contains(t, logs.String(), "no HTTP(S) forward-action listener")
}

func TestComposeEndpoints_NLBProtocolListener(t *testing.T) {
	ctx := context.Background()
	cli := &mockELBv2Client{}
	tgArn := "arn:aws:elasticloadbalancing:us-east-1:123:targetgroup/tg/abc"
	albArn := "arn:aws:elasticloadbalancing:us-east-1:123:loadbalancer/app/a/d1"

	cli.On("DescribeTargetGroups", ctx, &awselbv2.DescribeTargetGroupsInput{TargetGroupArns: []string{tgArn}}).
		Return(&awselbv2.DescribeTargetGroupsOutput{
			TargetGroups: []elbv2types.TargetGroup{
				{TargetGroupArn: ptr(tgArn), LoadBalancerArns: []string{albArn}},
			},
		}, nil)
	cli.On("DescribeLoadBalancers", ctx, &awselbv2.DescribeLoadBalancersInput{LoadBalancerArns: []string{albArn}}).
		Return(&awselbv2.DescribeLoadBalancersOutput{
			LoadBalancers: []elbv2types.LoadBalancer{
				{LoadBalancerArn: ptr(albArn), DNSName: ptr("a-dns"), Type: elbv2types.LoadBalancerTypeEnumApplication},
			},
		}, nil)
	cli.On("DescribeListeners", ctx, &awselbv2.DescribeListenersInput{LoadBalancerArn: ptr(albArn)}).
		Return(&awselbv2.DescribeListenersOutput{
			Listeners: []elbv2types.Listener{
				{
					ListenerArn:    ptr("listener-1"),
					Port:           ptr(int32(443)),
					Protocol:       elbv2types.ProtocolEnumTcp,
					DefaultActions: []elbv2types.Action{{Type: elbv2types.ActionTypeEnumForward, TargetGroupArn: ptr(tgArn)}},
				},
			},
		}, nil)

	lbs := []ecstypes.LoadBalancer{
		{ContainerName: ptr("app"), ContainerPort: ptr(int32(443)), TargetGroupArn: ptr(tgArn)},
	}

	logger, logs := captureLogs()
	result := composeEndpoints(ctx, lbs, cli, logger)

	assert.NoError(t, result.TransientError)
	assert.Empty(t, result.Endpoints)
	assert.Contains(t, logs.String(), "no HTTP(S) forward-action listener")
}

func TestComposeEndpoints_AmbiguousMultiListener(t *testing.T) {
	ctx := context.Background()
	cli := &mockELBv2Client{}
	tgArn := "arn:aws:elasticloadbalancing:us-east-1:123:targetgroup/tg/abc"
	albArn := "arn:aws:elasticloadbalancing:us-east-1:123:loadbalancer/app/a/d1"

	cli.On("DescribeTargetGroups", ctx, &awselbv2.DescribeTargetGroupsInput{TargetGroupArns: []string{tgArn}}).
		Return(&awselbv2.DescribeTargetGroupsOutput{
			TargetGroups: []elbv2types.TargetGroup{
				{TargetGroupArn: ptr(tgArn), LoadBalancerArns: []string{albArn}},
			},
		}, nil)
	cli.On("DescribeLoadBalancers", ctx, &awselbv2.DescribeLoadBalancersInput{LoadBalancerArns: []string{albArn}}).
		Return(&awselbv2.DescribeLoadBalancersOutput{
			LoadBalancers: []elbv2types.LoadBalancer{
				{LoadBalancerArn: ptr(albArn), DNSName: ptr("a-dns"), Type: elbv2types.LoadBalancerTypeEnumApplication},
			},
		}, nil)
	cli.On("DescribeListeners", ctx, &awselbv2.DescribeListenersInput{LoadBalancerArn: ptr(albArn)}).
		Return(&awselbv2.DescribeListenersOutput{
			Listeners: []elbv2types.Listener{
				{
					ListenerArn:    ptr("listener-public"),
					Port:           ptr(int32(443)),
					Protocol:       elbv2types.ProtocolEnumHttps,
					DefaultActions: []elbv2types.Action{{Type: elbv2types.ActionTypeEnumForward, TargetGroupArn: ptr(tgArn)}},
				},
				{
					ListenerArn:    ptr("listener-admin"),
					Port:           ptr(int32(8443)),
					Protocol:       elbv2types.ProtocolEnumHttps,
					DefaultActions: []elbv2types.Action{{Type: elbv2types.ActionTypeEnumForward, TargetGroupArn: ptr(tgArn)}},
				},
			},
		}, nil)

	lbs := []ecstypes.LoadBalancer{
		{ContainerName: ptr("app"), ContainerPort: ptr(int32(443)), TargetGroupArn: ptr(tgArn)},
	}

	logger, logs := captureLogs()
	result := composeEndpoints(ctx, lbs, cli, logger)

	assert.NoError(t, result.TransientError)
	assert.Empty(t, result.Endpoints)
	assert.Contains(t, logs.String(), "ambiguous")
	assert.Contains(t, logs.String(), "listener-public")
	assert.Contains(t, logs.String(), "listener-admin")
}

func TestComposeEndpoints_MissingContainerNamePort(t *testing.T) {
	ctx := context.Background()
	cli := &mockELBv2Client{}
	tgArn := "arn:aws:elasticloadbalancing:us-east-1:123:targetgroup/tg/abc"
	albArn := "arn:aws:elasticloadbalancing:us-east-1:123:loadbalancer/app/a/d1"

	cli.On("DescribeTargetGroups", ctx, &awselbv2.DescribeTargetGroupsInput{TargetGroupArns: []string{tgArn}}).
		Return(&awselbv2.DescribeTargetGroupsOutput{
			TargetGroups: []elbv2types.TargetGroup{
				{TargetGroupArn: ptr(tgArn), LoadBalancerArns: []string{albArn}},
			},
		}, nil)
	cli.On("DescribeLoadBalancers", ctx, &awselbv2.DescribeLoadBalancersInput{LoadBalancerArns: []string{albArn}}).
		Return(&awselbv2.DescribeLoadBalancersOutput{
			LoadBalancers: []elbv2types.LoadBalancer{
				{LoadBalancerArn: ptr(albArn), DNSName: ptr("a-dns"), Type: elbv2types.LoadBalancerTypeEnumApplication},
			},
		}, nil)
	cli.On("DescribeListeners", ctx, &awselbv2.DescribeListenersInput{LoadBalancerArn: ptr(albArn)}).
		Return(&awselbv2.DescribeListenersOutput{
			Listeners: []elbv2types.Listener{
				{
					ListenerArn:    ptr("listener-1"),
					Port:           ptr(int32(443)),
					Protocol:       elbv2types.ProtocolEnumHttps,
					DefaultActions: []elbv2types.Action{{Type: elbv2types.ActionTypeEnumForward, TargetGroupArn: ptr(tgArn)}},
				},
			},
		}, nil)

	lbs := []ecstypes.LoadBalancer{
		{ContainerName: nil, ContainerPort: ptr(int32(443)), TargetGroupArn: ptr(tgArn)},
	}

	logger, logs := captureLogs()
	result := composeEndpoints(ctx, lbs, cli, logger)

	assert.NoError(t, result.TransientError)
	assert.Empty(t, result.Endpoints)
	assert.Contains(t, logs.String(), "missing required fields")
}

func TestComposeEndpoints_TransientRetrySuccess(t *testing.T) {
	ctx := context.Background()
	cli := &mockELBv2Client{}
	tgArn := "arn:aws:elasticloadbalancing:us-east-1:123:targetgroup/tg/abc"
	albArn := "arn:aws:elasticloadbalancing:us-east-1:123:loadbalancer/app/a/d1"

	cli.On("DescribeTargetGroups", ctx, &awselbv2.DescribeTargetGroupsInput{TargetGroupArns: []string{tgArn}}).
		Return((*awselbv2.DescribeTargetGroupsOutput)(nil), awsAPIErr("Throttling", "rate exceeded")).Once()
	cli.On("DescribeTargetGroups", ctx, &awselbv2.DescribeTargetGroupsInput{TargetGroupArns: []string{tgArn}}).
		Return(&awselbv2.DescribeTargetGroupsOutput{
			TargetGroups: []elbv2types.TargetGroup{
				{TargetGroupArn: ptr(tgArn), LoadBalancerArns: []string{albArn}},
			},
		}, nil).Once()
	cli.On("DescribeLoadBalancers", ctx, &awselbv2.DescribeLoadBalancersInput{LoadBalancerArns: []string{albArn}}).
		Return(&awselbv2.DescribeLoadBalancersOutput{
			LoadBalancers: []elbv2types.LoadBalancer{
				{LoadBalancerArn: ptr(albArn), DNSName: ptr("a-dns"), Type: elbv2types.LoadBalancerTypeEnumApplication},
			},
		}, nil)
	cli.On("DescribeListeners", ctx, &awselbv2.DescribeListenersInput{LoadBalancerArn: ptr(albArn)}).
		Return(&awselbv2.DescribeListenersOutput{
			Listeners: []elbv2types.Listener{
				{
					ListenerArn:    ptr("l1"),
					Port:           ptr(int32(443)),
					Protocol:       elbv2types.ProtocolEnumHttps,
					DefaultActions: []elbv2types.Action{{Type: elbv2types.ActionTypeEnumForward, TargetGroupArn: ptr(tgArn)}},
				},
			},
		}, nil)

	lbs := []ecstypes.LoadBalancer{
		{ContainerName: ptr("app"), ContainerPort: ptr(int32(443)), TargetGroupArn: ptr(tgArn)},
	}

	result := composeEndpoints(ctx, lbs, cli, testLogger())

	assert.NoError(t, result.TransientError)
	assert.Equal(t, map[string]string{"app:443": "https://a-dns:443"}, result.Endpoints)
	cli.AssertExpectations(t)
}

func TestComposeEndpoints_TransientRetryExhausted(t *testing.T) {
	ctx := context.Background()
	cli := &mockELBv2Client{}
	tgArn := "arn:aws:elasticloadbalancing:us-east-1:123:targetgroup/tg/abc"

	cli.On("DescribeTargetGroups", ctx, &awselbv2.DescribeTargetGroupsInput{TargetGroupArns: []string{tgArn}}).
		Return((*awselbv2.DescribeTargetGroupsOutput)(nil), awsAPIErr("Throttling", "rate exceeded")).Times(3)

	lbs := []ecstypes.LoadBalancer{
		{ContainerName: ptr("app"), ContainerPort: ptr(int32(443)), TargetGroupArn: ptr(tgArn)},
	}

	result := composeEndpoints(ctx, lbs, cli, testLogger())

	assert.Error(t, result.TransientError)
	assert.Empty(t, result.Endpoints)
	cli.AssertExpectations(t)
}

func TestComposeEndpoints_AccessDeniedIsPermanentWithIAMHint(t *testing.T) {
	ctx := context.Background()
	cli := &mockELBv2Client{}
	tgArn := "arn:aws:elasticloadbalancing:us-east-1:123:targetgroup/tg/abc"

	cli.On("DescribeTargetGroups", ctx, &awselbv2.DescribeTargetGroupsInput{TargetGroupArns: []string{tgArn}}).
		Return((*awselbv2.DescribeTargetGroupsOutput)(nil), awsAPIErr("AccessDenied", "missing permission")).Once()

	lbs := []ecstypes.LoadBalancer{
		{ContainerName: ptr("app"), ContainerPort: ptr(int32(443)), TargetGroupArn: ptr(tgArn)},
	}

	logger, logs := captureLogs()
	result := composeEndpoints(ctx, lbs, cli, logger)

	assert.NoError(t, result.TransientError)
	assert.Empty(t, result.Endpoints)
	assert.Contains(t, logs.String(), "iam_permission")
	assert.Contains(t, logs.String(), "elbv2:Describe")
	cli.AssertExpectations(t)
}

func TestComposeEndpoints_PartialMixedResult(t *testing.T) {
	ctx := context.Background()
	cli := &mockELBv2Client{}
	tgGood := "arn:aws:elasticloadbalancing:us-east-1:123:targetgroup/good/abc"
	tgRule := "arn:aws:elasticloadbalancing:us-east-1:123:targetgroup/rule-routed/abc"
	tgTransient := "arn:aws:elasticloadbalancing:us-east-1:123:targetgroup/transient/abc"
	albGood := "arn:aws:elasticloadbalancing:us-east-1:123:loadbalancer/app/a/g"
	albRule := "arn:aws:elasticloadbalancing:us-east-1:123:loadbalancer/app/a/r"
	albTransient := "arn:aws:elasticloadbalancing:us-east-1:123:loadbalancer/app/a/t"

	cli.On("DescribeTargetGroups", ctx, &awselbv2.DescribeTargetGroupsInput{
		TargetGroupArns: []string{tgGood, tgRule, tgTransient},
	}).Return(&awselbv2.DescribeTargetGroupsOutput{
		TargetGroups: []elbv2types.TargetGroup{
			{TargetGroupArn: ptr(tgGood), LoadBalancerArns: []string{albGood}},
			{TargetGroupArn: ptr(tgRule), LoadBalancerArns: []string{albRule}},
			{TargetGroupArn: ptr(tgTransient), LoadBalancerArns: []string{albTransient}},
		},
	}, nil)

	cli.On("DescribeLoadBalancers", ctx, &awselbv2.DescribeLoadBalancersInput{
		LoadBalancerArns: []string{albGood, albRule, albTransient},
	}).Return(&awselbv2.DescribeLoadBalancersOutput{
		LoadBalancers: []elbv2types.LoadBalancer{
			{LoadBalancerArn: ptr(albGood), DNSName: ptr("good-dns"), Type: elbv2types.LoadBalancerTypeEnumApplication},
			{LoadBalancerArn: ptr(albRule), DNSName: ptr("rule-dns"), Type: elbv2types.LoadBalancerTypeEnumApplication},
			{LoadBalancerArn: ptr(albTransient), DNSName: ptr("transient-dns"), Type: elbv2types.LoadBalancerTypeEnumApplication},
		},
	}, nil)

	cli.On("DescribeListeners", ctx, &awselbv2.DescribeListenersInput{LoadBalancerArn: ptr(albGood)}).
		Return(&awselbv2.DescribeListenersOutput{
			Listeners: []elbv2types.Listener{{
				ListenerArn:    ptr("l1"),
				Port:           ptr(int32(443)),
				Protocol:       elbv2types.ProtocolEnumHttps,
				DefaultActions: []elbv2types.Action{{Type: elbv2types.ActionTypeEnumForward, TargetGroupArn: ptr(tgGood)}},
			}},
		}, nil)
	cli.On("DescribeListeners", ctx, &awselbv2.DescribeListenersInput{LoadBalancerArn: ptr(albRule)}).
		Return(&awselbv2.DescribeListenersOutput{
			Listeners: []elbv2types.Listener{{
				ListenerArn:    ptr("l-rule"),
				Port:           ptr(int32(80)),
				Protocol:       elbv2types.ProtocolEnumHttp,
				DefaultActions: []elbv2types.Action{{Type: elbv2types.ActionTypeEnumRedirect}},
			}},
		}, nil)
	cli.On("DescribeListeners", ctx, &awselbv2.DescribeListenersInput{LoadBalancerArn: ptr(albTransient)}).
		Return((*awselbv2.DescribeListenersOutput)(nil), awsAPIErr("Throttling", "rate exceeded")).Times(3)

	lbs := []ecstypes.LoadBalancer{
		{ContainerName: ptr("good"), ContainerPort: ptr(int32(443)), TargetGroupArn: ptr(tgGood)},
		{ContainerName: ptr("rule"), ContainerPort: ptr(int32(80)), TargetGroupArn: ptr(tgRule)},
		{ContainerName: ptr("trans"), ContainerPort: ptr(int32(8080)), TargetGroupArn: ptr(tgTransient)},
	}

	logger, logs := captureLogs()
	result := composeEndpoints(ctx, lbs, cli, logger)

	assert.Error(t, result.TransientError, "expected TransientError to be set due to throttling on albTransient")
	assert.Equal(t, map[string]string{"good:443": "https://good-dns:443"}, result.Endpoints)
	assert.Contains(t, logs.String(), "no HTTP(S) forward-action listener")
	cli.AssertExpectations(t)
}

func TestComposeEndpoints_DuplicateKeyLastWins(t *testing.T) {
	ctx := context.Background()
	cli := &mockELBv2Client{}
	tg1 := "arn:aws:elasticloadbalancing:us-east-1:123:targetgroup/tg1/abc"
	tg2 := "arn:aws:elasticloadbalancing:us-east-1:123:targetgroup/tg2/abc"
	alb1 := "arn:aws:elasticloadbalancing:us-east-1:123:loadbalancer/app/a/1"
	alb2 := "arn:aws:elasticloadbalancing:us-east-1:123:loadbalancer/app/a/2"

	cli.On("DescribeTargetGroups", ctx, &awselbv2.DescribeTargetGroupsInput{TargetGroupArns: []string{tg1, tg2}}).
		Return(&awselbv2.DescribeTargetGroupsOutput{
			TargetGroups: []elbv2types.TargetGroup{
				{TargetGroupArn: ptr(tg1), LoadBalancerArns: []string{alb1}},
				{TargetGroupArn: ptr(tg2), LoadBalancerArns: []string{alb2}},
			},
		}, nil)
	cli.On("DescribeLoadBalancers", ctx, &awselbv2.DescribeLoadBalancersInput{LoadBalancerArns: []string{alb1, alb2}}).
		Return(&awselbv2.DescribeLoadBalancersOutput{
			LoadBalancers: []elbv2types.LoadBalancer{
				{LoadBalancerArn: ptr(alb1), DNSName: ptr("dns-1"), Type: elbv2types.LoadBalancerTypeEnumApplication},
				{LoadBalancerArn: ptr(alb2), DNSName: ptr("dns-2"), Type: elbv2types.LoadBalancerTypeEnumApplication},
			},
		}, nil)
	cli.On("DescribeListeners", ctx, &awselbv2.DescribeListenersInput{LoadBalancerArn: ptr(alb1)}).
		Return(&awselbv2.DescribeListenersOutput{
			Listeners: []elbv2types.Listener{{
				ListenerArn:    ptr("l1"),
				Port:           ptr(int32(443)),
				Protocol:       elbv2types.ProtocolEnumHttps,
				DefaultActions: []elbv2types.Action{{Type: elbv2types.ActionTypeEnumForward, TargetGroupArn: ptr(tg1)}},
			}},
		}, nil)
	cli.On("DescribeListeners", ctx, &awselbv2.DescribeListenersInput{LoadBalancerArn: ptr(alb2)}).
		Return(&awselbv2.DescribeListenersOutput{
			Listeners: []elbv2types.Listener{{
				ListenerArn:    ptr("l2"),
				Port:           ptr(int32(80)),
				Protocol:       elbv2types.ProtocolEnumHttp,
				DefaultActions: []elbv2types.Action{{Type: elbv2types.ActionTypeEnumForward, TargetGroupArn: ptr(tg2)}},
			}},
		}, nil)

	lbs := []ecstypes.LoadBalancer{
		{ContainerName: ptr("app"), ContainerPort: ptr(int32(80)), TargetGroupArn: ptr(tg1)},
		{ContainerName: ptr("app"), ContainerPort: ptr(int32(80)), TargetGroupArn: ptr(tg2)},
	}

	logger, logs := captureLogs()
	result := composeEndpoints(ctx, lbs, cli, logger)

	assert.NoError(t, result.TransientError)
	// Last-write-wins. Note: TG ARN map iteration order may vary; either
	// "http://dns-2:80" (tg2 last) or "http://dns-1:443" (tg1 last) is
	// acceptable so long as exactly one key/value pair is present and the
	// warning is logged.
	assert.Len(t, result.Endpoints, 1)
	assert.Contains(t, result.Endpoints, "app:80")
	assert.Contains(t, logs.String(), "duplicate key resolves to different URLs")
	cli.AssertExpectations(t)
}
