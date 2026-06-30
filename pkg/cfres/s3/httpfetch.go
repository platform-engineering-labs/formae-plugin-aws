// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package s3

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// lookupIP is the DNS resolver used by guardURL; overridable in tests.
var lookupIP = net.LookupIP

// guardURLFn is the URL guard used by fetchHTTPSource; overridable in tests.
var guardURLFn = guardURL

// dialIPGuard is called at dial time with the already-resolved IP; overridable
// in tests that point at loopback httptest servers through fetchHTTPSource.
var dialIPGuard = func(ip net.IP) error {
	if isDisallowedIP(ip) {
		return fmt.Errorf("dial to disallowed address blocked")
	}
	return nil
}

// isDisallowedIP reports whether ip is in a range that must not be dialed
// (loopback, link-local unicast/multicast, private/RFC-1918, IMDS endpoint).
func isDisallowedIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsPrivate() ||
		ip.Equal(net.ParseIP("169.254.169.254"))
}

func guardURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid source URL")
	}
	if u.Scheme != "https" {
		return fmt.Errorf("source URL must be https")
	}
	host := u.Hostname()
	ips, err := lookupIP(host)
	if err != nil {
		return fmt.Errorf("cannot resolve source host %q", host)
	}
	for _, ip := range ips {
		if isDisallowedIP(ip) {
			return fmt.Errorf("source host %q resolves to a disallowed address", host)
		}
	}
	return nil
}

func newHardenedClient(timeout time.Duration) *http.Client {
	// Clone the default transport so we inherit sane defaults (connection
	// pooling, timeouts, etc.) and only override DialContext.
	base := http.DefaultTransport.(*http.Transport).Clone()
	dialer := &net.Dialer{
		Control: func(network, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			ip := net.ParseIP(host)
			if ip == nil {
				// address is already resolved at this point; if ParseIP fails
				// something is very wrong — reject to be safe.
				return fmt.Errorf("dial address %q is not a valid IP", host)
			}
			return dialIPGuard(ip)
		},
	}
	base.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		return dialer.DialContext(ctx, network, addr)
	}

	return &http.Client{
		Timeout:   timeout,
		Transport: base,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("too many redirects")
			}
			// reject any non-https redirect target unconditionally
			if req.URL.Scheme != "https" {
				return fmt.Errorf("refusing redirect to non-https %q", req.URL.Scheme)
			}
			if err := guardURL(req.URL.String()); err != nil {
				return err
			}
			// strip ALL headers from the original request on host change so that
			// any secret header (Authorization, X-Api-Key, etc.) is not leaked
			// to a different host.
			if len(via) > 0 && !strings.EqualFold(req.URL.Host, via[0].URL.Host) {
				for k := range via[0].Header {
					req.Header.Del(k)
				}
			}
			return nil
		},
	}
}
