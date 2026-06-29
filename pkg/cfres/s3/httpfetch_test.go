// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package s3

import (
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestGuardURL_RejectsNonHTTPS(t *testing.T) {
	if err := guardURL("http://example.com/x"); err == nil {
		t.Fatal("expected http:// to be rejected")
	}
}
func TestGuardURL_RejectsMetadataIP(t *testing.T) {
	if err := guardURL("https://169.254.169.254/latest/meta-data/"); err == nil {
		t.Fatal("expected metadata IP to be rejected")
	}
}
func TestGuardURL_RejectsLoopbackAndRFC1918(t *testing.T) {
	for _, u := range []string{"https://127.0.0.1/x", "https://10.0.0.5/x", "https://192.168.1.1/x"} {
		if err := guardURL(u); err == nil {
			t.Fatalf("expected %s to be rejected", u)
		}
	}
}
func TestHardenedClient_DropsAuthOnHostChange(t *testing.T) {
	// stub DNS so guardURL accepts the httptest TLS servers (which bind to loopback)
	orig := lookupIP
	lookupIP = func(host string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("93.184.216.34")}, nil
	}
	defer func() { lookupIP = orig }()

	// blob server records whether it saw an Authorization header
	var sawAuth bool
	blob := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization") != ""
		_, _ = w.Write([]byte("ok"))
	}))
	defer blob.Close()

	api := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, blob.URL, http.StatusFound) // https→https, different host:port
	}))
	defer api.Close()

	client := newHardenedClient(30 * time.Second)
	// accept self-signed certs from both test servers
	client.Transport = &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // test-only
	}

	req, _ := http.NewRequest(http.MethodGet, api.URL, nil)
	req.Header.Set("Authorization", "Bearer secret")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request failed (guard rejected redirect): %v", err)
	}
	_ = resp.Body.Close()
	if sawAuth {
		t.Fatal("Authorization leaked to the redirect target")
	}
}
func TestHardenedClient_RejectsDowngradeToHTTP(t *testing.T) {
	httpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer httpSrv.Close()
	tlsSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, httpSrv.URL, http.StatusFound) // https -> http
	}))
	defer tlsSrv.Close()
	c := newHardenedClient(30 * time.Second)
	c.Transport = tlsSrv.Client().Transport // trust the test TLS cert
	req, _ := http.NewRequest(http.MethodGet, tlsSrv.URL, nil)
	if _, err := c.Do(req); err == nil {
		t.Fatal("expected https->http downgrade to be rejected")
	}
}
