// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package ecs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	awselbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbv2types "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	"github.com/aws/smithy-go"
)

// ComposeResult is the outcome of composeEndpoints. Endpoints is the
// best-effort map composed from the service's loadBalancers[]; TransientError
// is non-nil ONLY when at least one entry failed due to retryable AWS errors
// AFTER internal retries were exhausted. Permanent failures (unsupported
// topology, IAM, etc.) leave TransientError nil but produce missing keys in
// the Endpoints map.
//
// Callers (Phase B finalSuccess, Service Read) branch on TransientError:
//   - nil  -> publish Endpoints (possibly empty/partial) and proceed to Success
//   - !nil -> Phase B returns InProgress; Read returns a recoverable plugin
//     error so ResolveCache and the sync loop retry the Plugin Read
//     without overwriting persisted state.
//
// Design: docs/superpowers/specs/2026-05-27-ecs-service-endpoints-resolvable-design.md
type ComposeResult struct {
	Endpoints      map[string]string
	TransientError error
}

// composeEndpoints walks each loadBalancers[] entry and composes a URL by
// joining its TG to the fronting ALB's DNS, the forwarding HTTP(S) listener's
// port, and the listener's protocol. Read-only; safe to call repeatedly. The
// helper retries transient AWS errors internally before classifying.
func composeEndpoints(
	ctx context.Context,
	loadBalancers []ecstypes.LoadBalancer,
	elbv2 elbv2Client,
	logger *slog.Logger,
) ComposeResult {
	if len(loadBalancers) == 0 {
		return ComposeResult{Endpoints: map[string]string{}}
	}

	out := ComposeResult{Endpoints: map[string]string{}}

	tgArns := uniqueTGArns(loadBalancers)
	if len(tgArns) == 0 {
		return out
	}

	tgs, err := describeTargetGroupsRetry(ctx, elbv2, tgArns, logger)
	if err != nil {
		if isTransient(err) {
			out.TransientError = err
			return out
		}
		for _, lb := range loadBalancers {
			logSkip(logger, "DescribeTargetGroups failed permanently", aws.ToString(lb.TargetGroupArn), err)
		}
		return out
	}
	tgByArn := indexTGsByArn(tgs)

	albArns := uniqueALBArns(tgs)
	var albs map[string]elbv2types.LoadBalancer
	if len(albArns) > 0 {
		lbsOut, err := describeLoadBalancersRetry(ctx, elbv2, albArns, logger)
		if err != nil {
			if isTransient(err) {
				out.TransientError = err
				return out
			}
			for _, lb := range loadBalancers {
				logSkip(logger, "DescribeLoadBalancers failed permanently", aws.ToString(lb.TargetGroupArn), err)
			}
			return out
		}
		albs = indexLBsByArn(lbsOut)
	}

	listenersByALB := map[string][]elbv2types.Listener{}
	for _, albArn := range albArns {
		l, err := describeListenersRetry(ctx, elbv2, albArn, logger)
		if err != nil {
			if isTransient(err) {
				out.TransientError = err
				continue
			}
			logSkip(logger, "DescribeListeners failed permanently", albArn, err)
			continue
		}
		listenersByALB[albArn] = l
	}

	for _, lb := range loadBalancers {
		key, url, skipped := composeOneEntry(lb, tgByArn, albs, listenersByALB, logger)
		if skipped {
			continue
		}
		if existing, dup := out.Endpoints[key]; dup && existing != url {
			logger.Warn("endpoint composition: duplicate key resolves to different URLs; last wins",
				"op", "composeEndpoints", "key", key, "previous", existing, "chosen", url)
		}
		out.Endpoints[key] = url
	}
	return out
}

func uniqueTGArns(lbs []ecstypes.LoadBalancer) []string {
	seen := map[string]bool{}
	var out []string
	for _, lb := range lbs {
		arn := aws.ToString(lb.TargetGroupArn)
		if arn == "" {
			continue
		}
		if !seen[arn] {
			seen[arn] = true
			out = append(out, arn)
		}
	}
	sort.Strings(out)
	return out
}

func uniqueALBArns(tgs []elbv2types.TargetGroup) []string {
	seen := map[string]bool{}
	var out []string
	for _, tg := range tgs {
		for _, a := range tg.LoadBalancerArns {
			if !seen[a] {
				seen[a] = true
				out = append(out, a)
			}
		}
	}
	sort.Strings(out)
	return out
}

func indexTGsByArn(tgs []elbv2types.TargetGroup) map[string]elbv2types.TargetGroup {
	m := map[string]elbv2types.TargetGroup{}
	for _, tg := range tgs {
		m[aws.ToString(tg.TargetGroupArn)] = tg
	}
	return m
}

func indexLBsByArn(lbs []elbv2types.LoadBalancer) map[string]elbv2types.LoadBalancer {
	m := map[string]elbv2types.LoadBalancer{}
	for _, lb := range lbs {
		m[aws.ToString(lb.LoadBalancerArn)] = lb
	}
	return m
}

// describeTargetGroupsRetry / describeLoadBalancersRetry / describeListenersRetry
// wrap their AWS SDK calls with a 3-attempt retry loop (500ms / 1s / 2s).
func describeTargetGroupsRetry(ctx context.Context, cli elbv2Client, arns []string, logger *slog.Logger,
) ([]elbv2types.TargetGroup, error) {
	var out *awselbv2.DescribeTargetGroupsOutput
	err := withRetries(ctx, "DescribeTargetGroups", logger, func() error {
		var cerr error
		out, cerr = cli.DescribeTargetGroups(ctx, &awselbv2.DescribeTargetGroupsInput{TargetGroupArns: arns})
		return cerr
	})
	if err != nil {
		return nil, err
	}
	return out.TargetGroups, nil
}

func describeLoadBalancersRetry(ctx context.Context, cli elbv2Client, arns []string, logger *slog.Logger,
) ([]elbv2types.LoadBalancer, error) {
	var out *awselbv2.DescribeLoadBalancersOutput
	err := withRetries(ctx, "DescribeLoadBalancers", logger, func() error {
		var cerr error
		out, cerr = cli.DescribeLoadBalancers(ctx, &awselbv2.DescribeLoadBalancersInput{LoadBalancerArns: arns})
		return cerr
	})
	if err != nil {
		return nil, err
	}
	return out.LoadBalancers, nil
}

func describeListenersRetry(ctx context.Context, cli elbv2Client, albArn string, logger *slog.Logger,
) ([]elbv2types.Listener, error) {
	var out *awselbv2.DescribeListenersOutput
	err := withRetries(ctx, "DescribeListeners", logger, func() error {
		var cerr error
		out, cerr = cli.DescribeListeners(ctx, &awselbv2.DescribeListenersInput{LoadBalancerArn: &albArn})
		return cerr
	})
	if err != nil {
		return nil, err
	}
	return out.Listeners, nil
}

// withRetries runs fn up to 3 times with 500ms/1s/2s backoff. Stops early on
// non-transient errors.
func withRetries(ctx context.Context, op string, logger *slog.Logger, fn func() error) error {
	backoffs := []time.Duration{500 * time.Millisecond, 1 * time.Second, 2 * time.Second}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		err := fn()
		if err == nil {
			return nil
		}
		lastErr = err
		if !isTransient(err) {
			return err
		}
		logger.Info("endpoint composition: transient AWS error; retrying",
			"op", "composeEndpoints", "call", op, "attempt", attempt+1, "err", err)
		if attempt < 2 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoffs[attempt]):
			}
		}
	}
	return lastErr
}

// composeOneEntry resolves one loadBalancers[] entry to (key, url). Returns
// skipped=true if the entry should be omitted from the map.
func composeOneEntry(
	lb ecstypes.LoadBalancer,
	tgByArn map[string]elbv2types.TargetGroup,
	albs map[string]elbv2types.LoadBalancer,
	listenersByALB map[string][]elbv2types.Listener,
	logger *slog.Logger,
) (key, url string, skipped bool) {
	tgArn := aws.ToString(lb.TargetGroupArn)
	containerName := aws.ToString(lb.ContainerName)
	containerPort := int32(0)
	if lb.ContainerPort != nil {
		containerPort = *lb.ContainerPort
	}

	if tgArn == "" || containerName == "" || containerPort == 0 {
		logSkip(logger, "loadBalancers[] entry missing required fields", tgArn, nil,
			"containerName", containerName, "containerPort", containerPort)
		return "", "", true
	}

	tg, ok := tgByArn[tgArn]
	if !ok {
		logSkip(logger, "TG not found in DescribeTargetGroups response", tgArn, nil)
		return "", "", true
	}
	if len(tg.LoadBalancerArns) == 0 {
		logSkip(logger, "TG attached to zero LBs", tgArn, nil)
		return "", "", true
	}

	// Identify the unique ALB that fronts this TG (filter to ALB type).
	var albArn string
	albCount := 0
	for _, a := range tg.LoadBalancerArns {
		lbObj, present := albs[a]
		if !present {
			continue
		}
		if lbObj.Type != elbv2types.LoadBalancerTypeEnumApplication {
			continue
		}
		albCount++
		albArn = a
	}
	if albCount == 0 {
		logSkip(logger, "TG attached to no ALB (NLB-only or unresolved)", tgArn, nil)
		return "", "", true
	}
	if albCount > 1 {
		logSkip(logger, "multiple ALBs front the same TG", tgArn, nil, "albCount", albCount)
		return "", "", true
	}

	albObj := albs[albArn]

	// Find HTTP(S) forward-action listeners referencing this TG.
	var matches []elbv2types.Listener
	for _, lst := range listenersByALB[albArn] {
		if !listenerForwardsTo(lst, tgArn) {
			continue
		}
		if lst.Protocol != elbv2types.ProtocolEnumHttp && lst.Protocol != elbv2types.ProtocolEnumHttps {
			continue
		}
		matches = append(matches, lst)
	}
	if len(matches) == 0 {
		logSkip(logger, "no HTTP(S) forward-action listener references this TG (rule-routing or NLB protocols)", tgArn, nil)
		return "", "", true
	}
	if len(matches) > 1 {
		arns := make([]string, 0, len(matches))
		for _, l := range matches {
			arns = append(arns, aws.ToString(l.ListenerArn))
		}
		logSkip(logger, "ambiguous: multiple HTTP(S) forward-action listeners reference this TG; cannot determine primary URL", tgArn, nil,
			"listenerCount", len(matches), "listenerArns", arns)
		return "", "", true
	}

	lst := matches[0]
	port := int32(0)
	if lst.Port != nil {
		port = *lst.Port
	}
	scheme := "http"
	if lst.Protocol == elbv2types.ProtocolEnumHttps {
		scheme = "https"
	}
	dns := aws.ToString(albObj.DNSName)

	return fmt.Sprintf("%s:%d", containerName, containerPort),
		fmt.Sprintf("%s://%s:%d", scheme, dns, port),
		false
}

// listenerForwardsTo returns true if any DefaultActions[] entry has
// Type=forward referencing tgArn, either directly or via ForwardConfig.
func listenerForwardsTo(lst elbv2types.Listener, tgArn string) bool {
	for _, a := range lst.DefaultActions {
		if a.Type != elbv2types.ActionTypeEnumForward {
			continue
		}
		if aws.ToString(a.TargetGroupArn) == tgArn {
			return true
		}
		if a.ForwardConfig != nil {
			for _, tg := range a.ForwardConfig.TargetGroups {
				if aws.ToString(tg.TargetGroupArn) == tgArn {
					return true
				}
			}
		}
	}
	return false
}

// isTransient classifies an AWS API error as retryable (transient) vs not.
// Transient: SDK marks as retryable, 5xx, throttling. Permanent: AccessDenied,
// validation errors, 4xx (except throttling).
//
// Uses smithy.APIError (the interface real AWS SDK v2 errors satisfy via
// smithy.GenericAPIError). The interface returns ErrorFault as a typed enum,
// so we compare against smithy.FaultServer rather than a string.
func isTransient(err error) bool {
	if err == nil {
		return false
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		code := apiErr.ErrorCode()
		switch code {
		case "Throttling", "ThrottlingException", "RequestLimitExceeded",
			"TooManyRequests", "InternalFailure", "ServiceUnavailable":
			return true
		case "AccessDenied", "AccessDeniedException", "UnauthorizedOperation",
			"InvalidParameter", "InvalidParameterValue", "ValidationException",
			"TargetGroupNotFound", "LoadBalancerNotFound", "ListenerNotFound":
			return false
		}
		if apiErr.ErrorFault() == smithy.FaultServer {
			return true
		}
	}
	// Unknown errors (network, EOF): treat as transient. The retry budget bounds
	// runaway loops.
	return true
}

// logSkip emits a structured warn-level slog event for permanent per-entry
// skips. Special-cases AccessDenied with an IAM remediation hint.
func logSkip(logger *slog.Logger, reason, tgArn string, err error, extras ...any) {
	attrs := []any{"op", "composeEndpoints", "reason", reason, "tgArn", tgArn}
	attrs = append(attrs, extras...)
	if err != nil {
		attrs = append(attrs, "err", err.Error())
		if isAccessDenied(err) {
			attrs = append(attrs,
				"errorClass", "iam_permission",
				"remediation", "grant elbv2:Describe* on the agent's IAM role")
		}
	}
	logger.Warn("endpoint composition: skipping entry", attrs...)
}

func isAccessDenied(err error) bool {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "AccessDenied", "AccessDeniedException", "UnauthorizedOperation":
			return true
		}
	}
	return false
}
