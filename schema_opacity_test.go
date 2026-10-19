// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package main

// This test pins the opacity of every field in this schema that holds a
// credential value.
//
// Opacity is not declared per field. The core schema derives it from the
// declared type: `isSecretValueType` recurses through nullable types, union
// members and type aliases looking for a class named `SecretValue`, and a field
// whose type reaches one is marked `Opaque` in its rendered FieldHint. The agent
// hashes an opaque field at rest; a field that misses the type persists in
// cleartext. So a credential field typed `String` is a cleartext leak, and the
// mistake is invisible in the schema source unless someone knows to look for it.
//
// The assertion is made against the *rendered* hint rather than the source text,
// by evaluating `Fq.hints` / `Fq.subHints` through pkl, so it holds even if the
// derivation rule changes shape. Resource fields go through `hints`; subresource
// fields go through `subHints`, because `hints` requires a `ResourceHint`
// annotation that subresource classes do not carry.
//
// The table carries negative cases as well as positive ones. Without them the
// test could pass by reporting every field opaque, and `HeaderName` in
// particular must stay non-opaque: it is the identity half of a custom origin
// header pair and is used to match a header across a diff, so hashing it would
// break the match.
//
// What this does NOT cover: whether an opaque field behaves correctly through a
// provider Read that returns the live value. That interaction lives in the
// agent's suppression path, not in this schema.

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type opacityCase struct {
	module     string // import path relative to schema/pkl
	class      string
	sub        bool   // subresource: use subHints rather than hints
	field      string // rendered hint key
	wantOpaque bool
}

// Every credential-bearing field in the schema, plus negative controls.
var opacityCases = []opacityCase{
	// Fields that accept a generator binding via formae.ValueSource.
	{"ec2/verifiedaccesstrustprovider.pkl", "OidcOptions", true, "ClientSecret", true},
	{"ec2/vpnconnection.pkl", "VpnTunnelOptionsSpecification", true, "PreSharedKey", true},
	{"elasticloadbalancingv2/listener.pkl", "AuthenticateOidcConfig", true, "ClientSecret", true},
	{"elasticloadbalancingv2/listenerrule.pkl", "AuthenticateOidcConfig", true, "ClientSecret", true},
	{"iam/servercertificate.pkl", "ServerCertificate", false, "PrivateKey", true},
	{"lambda/permission.pkl", "Permission", false, "EventSourceToken", true},
	{"rds/databaserole.pkl", "DatabaseRole", false, "Password", true},
	{"rds/dbcluster.pkl", "DBCluster", false, "MasterUserPassword", true},
	{"rds/dbinstance.pkl", "DBInstance", false, "MasterUserPassword", true},
	{"rds/dbinstance.pkl", "DBInstance", false, "TdeCredentialPassword", true},

	// Fields whose union is spelled out rather than naming the alias.
	{"iam/user.pkl", "UserLoginProfile", true, "Password", true},
	{"secretsmanager/secret.pkl", "Secret", false, "SecretString", true},

	// Credentials that hold a value but take no generator binding.
	{"apigateway/apikey.pkl", "ApiKey", false, "Value", true},
	{"cloudfront/distribution.pkl", "OriginCustomHeader", true, "HeaderValue", true},

	// Negative controls: ordinary fields that must not be opaque.
	{"cloudfront/distribution.pkl", "OriginCustomHeader", true, "HeaderName", false},
	{"apigateway/apikey.pkl", "ApiKey", false, "Description", false},
	{"rds/dbinstance.pkl", "DBInstance", false, "MasterUsername", false},
}

func schemaPklDir() string {
	_, filename, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(filename), "schema", "pkl")
}

func (c opacityCase) key() string {
	return fmt.Sprintf("%s:%s:%s", c.module, c.class, c.field)
}

// renderOpacity evaluates every case in one pkl pass and returns the rendered
// Opaque flag per case key.
//
// The probe module has to live inside schema/pkl: pkl resolves an `@formae`
// dependency relative to the enclosing PklProject, and neither --project-dir
// nor an absolute path substitutes for that.
func renderOpacity(t *testing.T, cases []opacityCase) map[string]bool {
	t.Helper()

	dir := schemaPklDir()
	probe := filepath.Join(dir, "_opacity_probe.pkl")

	// Never clobber: if this exists, a previous run died and the file is not ours.
	if _, err := os.Stat(probe); err == nil {
		t.Fatalf("%s already exists; remove it before running this test", probe)
	}

	var imports, entries strings.Builder
	aliases := map[string]string{}
	for _, c := range cases {
		if _, seen := aliases[c.module]; !seen {
			alias := fmt.Sprintf("m%d", len(aliases))
			aliases[c.module] = alias
			fmt.Fprintf(&imports, "import %q as %s\n", c.module, alias)
		}
		fn := "hints"
		if c.sub {
			fn = "subHints"
		}
		fmt.Fprintf(&entries, "  [%q] = fq.%s(reflect.Class(%s.%s))[%q].Opaque\n",
			c.key(), fn, aliases[c.module], c.class, c.field)
	}

	src := fmt.Sprintf(`module aws._opacity_probe

import "pkl:reflect"
import "@formae/formae.pkl"
%s
local fq = new formae.Fq {}

opacity: Mapping<String, Boolean> = new {
%s}
`, imports.String(), entries.String())

	require.NoError(t, os.WriteFile(probe, []byte(src), 0o644))
	t.Cleanup(func() { _ = os.Remove(probe) })

	// PklProject.deps.json is gitignored, so a fresh checkout has no resolved
	// lock and evaluation cannot load the formae dependency. Resolve first;
	// it is a no-op against a warm cache. Skipping rather than failing when
	// this cannot run keeps an offline checkout usable, and CI resolves fine.
	resolve := exec.Command("pkl", "project", "resolve")
	resolve.Dir = dir
	if out, err := resolve.CombinedOutput(); err != nil {
		t.Skipf("cannot resolve the pkl project, so the rendered hints are unavailable: %v\n%s", err, out)
	}

	cmd := exec.Command("pkl", "eval", "-f", "json", "_opacity_probe.pkl")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "pkl eval failed:\n%s", out)

	var result struct {
		Opacity map[string]bool `json:"opacity"`
	}
	require.NoError(t, json.Unmarshal(out, &result), "unparseable pkl output:\n%s", out)
	return result.Opacity
}

func TestSchema_CredentialFieldsAreOpaque(t *testing.T) {
	if _, err := exec.LookPath("pkl"); err != nil {
		t.Skip("pkl not on PATH")
	}

	rendered := renderOpacity(t, opacityCases)

	for _, c := range opacityCases {
		t.Run(c.key(), func(t *testing.T) {
			got, ok := rendered[c.key()]
			require.True(t, ok, "no rendered hint for %s; the field or class was renamed", c.key())
			if c.wantOpaque {
				assert.True(t, got,
					"%s holds a credential but renders Opaque=false, so the agent stores it in cleartext; "+
						"the field's type must reach formae.SecretValue", c.key())
			} else {
				assert.False(t, got,
					"%s renders Opaque=true but is not a credential", c.key())
			}
		})
	}
}
