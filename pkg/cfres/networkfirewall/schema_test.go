// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package networkfirewall

// These are textual smoke tests over the PKL schema source for the
// AWS::NetworkFirewall::* resources. They assert that the enum-valued fields
// carry literal-union typealias types (e.g. "DROP"|"CONTINUE"|"REJECT") rather
// than a bare String, so that invalid values are rejected at pkl eval time
// instead of slipping through to a multi-minute apply that AWS then rejects.
//
// They are intentionally textual rather than full schema-emission tests:
// running ExtractSchema in a unit test requires a live pkl CLI subprocess and
// network access for package resolution, which is unsuitable for
// make test-unit. The live negative-path (an invalid value failing pkl eval)
// is exercised separately in the conformance/validation flow.

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func nfwSchemaPath(file string) string {
	_, filename, _, _ := runtime.Caller(0)
	// pkg/cfres/networkfirewall/ -> (repo root)/schema/pkl/networkfirewall/
	root := filepath.Join(filepath.Dir(filename), "..", "..", "..", "schema", "pkl", "networkfirewall")
	return filepath.Join(root, file)
}

func readNfwSchema(t *testing.T, file string) string {
	t.Helper()
	b, err := os.ReadFile(nfwSchemaPath(file))
	if err != nil {
		t.Fatalf("could not read %s: %v", file, err)
	}
	return string(b)
}

// typealiasRHS returns the right-hand side of "typealias <name> = <rhs>" from
// the schema source, trimmed.
func typealiasRHS(src, name string) (string, bool) {
	for _, line := range strings.Split(src, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "typealias "+name+" ") {
			continue
		}
		if i := strings.Index(trimmed, "="); i >= 0 {
			return strings.TrimSpace(trimmed[i+1:]), true
		}
	}
	return "", false
}

// fieldType returns the declared type of the first field whose declaration line
// (trimmed) begins with "<field>:", i.e. the text after the colon.
func fieldType(src, field string) (string, bool) {
	for _, line := range strings.Split(src, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, field+":") {
			return strings.TrimSpace(strings.TrimPrefix(trimmed, field+":")), true
		}
	}
	return "", false
}

func TestNetworkFirewall_Schema_EnumTypealiasesDeclared(t *testing.T) {
	cases := []struct {
		file    string
		name    string
		wantRHS string
	}{
		{"firewallpolicy.pkl", "RuleOrder", `"DEFAULT_ACTION_ORDER"|"STRICT_ORDER"`},
		{"firewallpolicy.pkl", "StreamExceptionPolicy", `"DROP"|"CONTINUE"|"REJECT"`},
		{"firewallpolicy.pkl", "StatefulDefaultAction", `"aws:drop_strict"|"aws:drop_established"|"aws:alert_strict"|"aws:alert_established"|"aws:drop_established_app_layer"|"aws:alert_established_app_layer"|"aws:drop_established_app_layer_to_server"|"aws:alert_established_app_layer_to_server"`},
		{"firewall.pkl", "IPAddressType", `"DUALSTACK"|"IPV4"|"IPV6"`},
		{"loggingconfiguration.pkl", "LogType", `"ALERT"|"FLOW"|"TLS"`},
		{"loggingconfiguration.pkl", "LogDestinationType", `"S3"|"CloudWatchLogs"|"KinesisDataFirehose"`},
		{"rulegroup.pkl", "RuleGroupType", `"STATELESS"|"STATEFUL"`},
		{"rulegroup.pkl", "GeneratedRulesType", `"ALLOWLIST"|"DENYLIST"|"ALERTLIST"|"REJECTLIST"`},
		{"rulegroup.pkl", "TargetType", `"TLS_SNI"|"HTTP_HOST"`},
		{"rulegroup.pkl", "RuleOrder", `"DEFAULT_ACTION_ORDER"|"STRICT_ORDER"`},
	}

	for _, c := range cases {
		t.Run(c.file+"/"+c.name, func(t *testing.T) {
			src := readNfwSchema(t, c.file)
			rhs, ok := typealiasRHS(src, c.name)
			if !ok {
				t.Fatalf("%s: typealias %s not declared", c.file, c.name)
			}
			if rhs != c.wantRHS {
				t.Errorf("%s: typealias %s = %q, want %q", c.file, c.name, rhs, c.wantRHS)
			}
		})
	}
}

func TestNetworkFirewall_Schema_EnumFieldsUseTypealias(t *testing.T) {
	cases := []struct {
		file     string
		field    string
		wantType string
	}{
		{"firewallpolicy.pkl", "ruleOrder", "RuleOrder?"},
		{"firewallpolicy.pkl", "streamExceptionPolicy", "StreamExceptionPolicy?"},
		{"firewallpolicy.pkl", "statefulDefaultActions", "Listing<StatefulDefaultAction>?"},
		{"firewall.pkl", "iPAddressType", "IPAddressType?"},
		{"loggingconfiguration.pkl", "logType", "LogType"},
		{"loggingconfiguration.pkl", "logDestinationType", "LogDestinationType"},
		{"rulegroup.pkl", "type", "RuleGroupType"},
		{"rulegroup.pkl", "targetTypes", "Listing<TargetType>"},
		{"rulegroup.pkl", "generatedRulesType", "GeneratedRulesType"},
		{"rulegroup.pkl", "ruleOrder", "RuleOrder?"},
	}

	for _, c := range cases {
		t.Run(c.file+"/"+c.field, func(t *testing.T) {
			src := readNfwSchema(t, c.file)
			got, ok := fieldType(src, c.field)
			if !ok {
				t.Fatalf("%s: field %s not found", c.file, c.field)
			}
			if got != c.wantType {
				t.Errorf("%s: field %s has type %q, want %q (literal-union typealias, not bare String)", c.file, c.field, got, c.wantType)
			}
		})
	}
}
