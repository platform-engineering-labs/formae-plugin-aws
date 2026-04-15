// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package elasticloadbalancingv2

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/config"
)

func listenerPropsJSON(protocol string, port int, lbArn string) string {
	props := map[string]any{
		"ListenerArn":     "arn:aws:elasticloadbalancing:us-west-2:123456789012:listener/app/my-alb/50dc6c495c0c9188/f2f7dc8efc522ab2",
		"LoadBalancerArn": lbArn,
		"Protocol":        protocol,
		"Port":            port,
		"DefaultActions": []map[string]any{
			{"Type": "forward", "TargetGroupArn": "arn:aws:elasticloadbalancing:us-west-2:123456789012:targetgroup/my-tg/73e2d6bc24d8a067"},
		},
	}
	b, _ := json.Marshal(props)
	return string(b)
}

func albPropsJSON(dnsName string) string {
	props := map[string]any{
		"LoadBalancerArn":  "arn:aws:elasticloadbalancing:us-west-2:123456789012:loadbalancer/app/my-alb/50dc6c495c0c9188",
		"DNSName":          dnsName,
		"LoadBalancerName": "my-alb",
		"Type":             "application",
	}
	b, _ := json.Marshal(props)
	return string(b)
}

const (
	listenerArn = "arn:aws:elasticloadbalancing:us-west-2:123456789012:listener/app/my-alb/50dc6c495c0c9188/f2f7dc8efc522ab2"
	lbArn       = "arn:aws:elasticloadbalancing:us-west-2:123456789012:loadbalancer/app/my-alb/50dc6c495c0c9188"
	dnsName     = "my-alb-1234567890.us-west-2.elb.amazonaws.com"
)

func setupReader(t *testing.T, protocol string, port int, albDNS string) (*mockResourceReader, *Listener) {
	t.Helper()
	reader := &mockResourceReader{}

	// Listener read
	reader.On("ReadResource", mock.Anything, mock.MatchedBy(func(req *resource.ReadRequest) bool {
		return req.ResourceType == "AWS::ElasticLoadBalancingV2::Listener" ||
			req.NativeID == listenerArn
	})).Return(&resource.ReadResult{
		ResourceType: "AWS::ElasticLoadBalancingV2::Listener",
		Properties:   listenerPropsJSON(protocol, port, lbArn),
	}, nil)

	// ALB read
	reader.On("ReadResource", mock.Anything, mock.MatchedBy(func(req *resource.ReadRequest) bool {
		return req.ResourceType == "AWS::ElasticLoadBalancingV2::LoadBalancer"
	})).Return(&resource.ReadResult{
		ResourceType: "AWS::ElasticLoadBalancingV2::LoadBalancer",
		Properties:   albPropsJSON(albDNS),
	}, nil)

	listener := &Listener{cfg: &config.Config{Region: "us-west-2"}, reader: reader}
	return reader, listener
}

func TestListener_Read_HTTPS_StandardPort(t *testing.T) {
	reader, listener := setupReader(t, "HTTPS", 443, dnsName)

	result, err := listener.Read(context.Background(), &resource.ReadRequest{
		NativeID:     listenerArn,
		ResourceType: "AWS::ElasticLoadBalancingV2::Listener",
	})

	require.NoError(t, err)
	assert.Equal(t, "AWS::ElasticLoadBalancingV2::Listener", result.ResourceType)
	assert.Empty(t, result.ErrorCode)

	var props map[string]any
	err = json.Unmarshal([]byte(result.Properties), &props)
	require.NoError(t, err)
	assert.Equal(t, "https://"+dnsName, props["Url"])

	reader.AssertExpectations(t)
}

func TestListener_Read_HTTP_StandardPort(t *testing.T) {
	reader, listener := setupReader(t, "HTTP", 80, dnsName)

	result, err := listener.Read(context.Background(), &resource.ReadRequest{
		NativeID:     listenerArn,
		ResourceType: "AWS::ElasticLoadBalancingV2::Listener",
	})

	require.NoError(t, err)

	var props map[string]any
	err = json.Unmarshal([]byte(result.Properties), &props)
	require.NoError(t, err)
	assert.Equal(t, "http://"+dnsName, props["Url"])

	reader.AssertExpectations(t)
}

func TestListener_Read_HTTPS_NonStandardPort(t *testing.T) {
	reader, listener := setupReader(t, "HTTPS", 8443, dnsName)

	result, err := listener.Read(context.Background(), &resource.ReadRequest{
		NativeID:     listenerArn,
		ResourceType: "AWS::ElasticLoadBalancingV2::Listener",
	})

	require.NoError(t, err)

	var props map[string]any
	err = json.Unmarshal([]byte(result.Properties), &props)
	require.NoError(t, err)
	assert.Equal(t, fmt.Sprintf("https://%s:8443", dnsName), props["Url"])

	reader.AssertExpectations(t)
}

func TestListener_Read_HTTP_NonStandardPort(t *testing.T) {
	reader, listener := setupReader(t, "HTTP", 8080, dnsName)

	result, err := listener.Read(context.Background(), &resource.ReadRequest{
		NativeID:     listenerArn,
		ResourceType: "AWS::ElasticLoadBalancingV2::Listener",
	})

	require.NoError(t, err)

	var props map[string]any
	err = json.Unmarshal([]byte(result.Properties), &props)
	require.NoError(t, err)
	assert.Equal(t, fmt.Sprintf("http://%s:8080", dnsName), props["Url"])

	reader.AssertExpectations(t)
}

func TestListener_Read_ALBReadFails_OmitsUrl(t *testing.T) {
	reader := &mockResourceReader{}

	// Listener read succeeds
	reader.On("ReadResource", mock.Anything, mock.MatchedBy(func(req *resource.ReadRequest) bool {
		return req.ResourceType == "AWS::ElasticLoadBalancingV2::Listener"
	})).Return(&resource.ReadResult{
		ResourceType: "AWS::ElasticLoadBalancingV2::Listener",
		Properties:   listenerPropsJSON("HTTPS", 443, lbArn),
	}, nil)

	// ALB read fails
	reader.On("ReadResource", mock.Anything, mock.MatchedBy(func(req *resource.ReadRequest) bool {
		return req.ResourceType == "AWS::ElasticLoadBalancingV2::LoadBalancer"
	})).Return(nil, fmt.Errorf("access denied"))

	listener := &Listener{cfg: &config.Config{Region: "us-west-2"}, reader: reader}
	result, err := listener.Read(context.Background(), &resource.ReadRequest{
		NativeID:     listenerArn,
		ResourceType: "AWS::ElasticLoadBalancingV2::Listener",
	})

	require.NoError(t, err)
	assert.Equal(t, "AWS::ElasticLoadBalancingV2::Listener", result.ResourceType)

	var props map[string]any
	err = json.Unmarshal([]byte(result.Properties), &props)
	require.NoError(t, err)
	_, hasUrl := props["Url"]
	assert.False(t, hasUrl, "Url should not be present when ALB read fails")

	reader.AssertExpectations(t)
}

func TestListener_Read_ALBNotFound_OmitsUrl(t *testing.T) {
	reader := &mockResourceReader{}

	// Listener read succeeds
	reader.On("ReadResource", mock.Anything, mock.MatchedBy(func(req *resource.ReadRequest) bool {
		return req.ResourceType == "AWS::ElasticLoadBalancingV2::Listener"
	})).Return(&resource.ReadResult{
		ResourceType: "AWS::ElasticLoadBalancingV2::Listener",
		Properties:   listenerPropsJSON("HTTPS", 443, lbArn),
	}, nil)

	// ALB read returns NotFound error code
	reader.On("ReadResource", mock.Anything, mock.MatchedBy(func(req *resource.ReadRequest) bool {
		return req.ResourceType == "AWS::ElasticLoadBalancingV2::LoadBalancer"
	})).Return(&resource.ReadResult{
		ResourceType: "AWS::ElasticLoadBalancingV2::LoadBalancer",
		ErrorCode:    "NotFound",
	}, nil)

	listener := &Listener{cfg: &config.Config{Region: "us-west-2"}, reader: reader}
	result, err := listener.Read(context.Background(), &resource.ReadRequest{
		NativeID:     listenerArn,
		ResourceType: "AWS::ElasticLoadBalancingV2::Listener",
	})

	require.NoError(t, err)

	var props map[string]any
	err = json.Unmarshal([]byte(result.Properties), &props)
	require.NoError(t, err)
	_, hasUrl := props["Url"]
	assert.False(t, hasUrl, "Url should not be present when ALB is not found")

	reader.AssertExpectations(t)
}

func TestListener_Read_ListenerNotFound(t *testing.T) {
	reader := &mockResourceReader{}

	// Listener read returns NotFound
	reader.On("ReadResource", mock.Anything, mock.MatchedBy(func(req *resource.ReadRequest) bool {
		return req.ResourceType == "AWS::ElasticLoadBalancingV2::Listener"
	})).Return(&resource.ReadResult{
		ResourceType: "AWS::ElasticLoadBalancingV2::Listener",
		ErrorCode:    "NotFound",
	}, nil)

	listener := &Listener{cfg: &config.Config{Region: "us-west-2"}, reader: reader}
	result, err := listener.Read(context.Background(), &resource.ReadRequest{
		NativeID:     listenerArn,
		ResourceType: "AWS::ElasticLoadBalancingV2::Listener",
	})

	require.NoError(t, err)
	assert.Equal(t, resource.OperationErrorCode("NotFound"), result.ErrorCode)

	reader.AssertExpectations(t)
}

func TestGetPort(t *testing.T) {
	tests := []struct {
		name     string
		props    map[string]any
		wantPort int
		wantOK   bool
	}{
		{"float64", map[string]any{"Port": float64(443)}, 443, true},
		{"int", map[string]any{"Port": int(8080)}, 8080, true},
		{"missing", map[string]any{}, 0, false},
		{"string", map[string]any{"Port": "443"}, 0, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			port, ok := getPort(tc.props)
			assert.Equal(t, tc.wantOK, ok)
			if ok {
				assert.Equal(t, tc.wantPort, port)
			}
		})
	}
}

func TestIsStandardPort(t *testing.T) {
	assert.True(t, isStandardPort("http", 80))
	assert.True(t, isStandardPort("https", 443))
	assert.False(t, isStandardPort("http", 8080))
	assert.False(t, isStandardPort("https", 8443))
	assert.False(t, isStandardPort("http", 443))
	assert.False(t, isStandardPort("https", 80))
}
