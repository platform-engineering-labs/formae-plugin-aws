// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package s3

import (
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
	// blob server records whether it saw an Authorization header
	var sawAuth bool
	blob := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization") != ""
		_, _ = w.Write([]byte("ok"))
	}))
	defer blob.Close()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, blob.URL, http.StatusFound) // 302 to a different host:port
	}))
	defer api.Close()
	req, _ := http.NewRequest(http.MethodGet, api.URL, nil)
	req.Header.Set("Authorization", "Bearer secret")
	resp, err := newHardenedClient(30 * time.Second).Do(req)
	if err != nil {
		t.Fatal(err)
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
