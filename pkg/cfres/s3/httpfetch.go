// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package s3

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// lookupIP is the DNS resolver used by guardURL; overridable in tests.
var lookupIP = net.LookupIP

// guardURLFn is the URL guard used by fetchHTTPSource; overridable in tests.
var guardURLFn = guardURL

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
		if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsPrivate() ||
			ip.Equal(net.ParseIP("169.254.169.254")) {
			return fmt.Errorf("source host %q resolves to a disallowed address", host)
		}
	}
	return nil
}

func newHardenedClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
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
			// strip auth + secret headers on host change (compare against first hop)
			if len(via) > 0 && !strings.EqualFold(req.URL.Host, via[0].URL.Host) {
				req.Header.Del("Authorization")
			}
			return nil
		},
	}
}
