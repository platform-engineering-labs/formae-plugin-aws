// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package config

import (
	"context"
	"encoding/json"
	"os"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
)

type Config struct {
	Region  string `json:"Region"`
	Profile string `json:"Profile"`
}

func (c *Config) ToAwsConfig(ctx context.Context) (aws.Config, error) {
	var opts []func(*awsconfig.LoadOptions) error

	opts = append(opts, awsconfig.WithRegion(c.Region))
	if c.Profile != "" {
		opts = append(opts, awsconfig.WithSharedConfigProfile(c.Profile))
	}

	// Support profiles that assume a role guarded by MFA. Without a token
	// provider the SDK fails to load such profiles with:
	//   "assume role with MFA enabled, but AssumeRoleTokenProvider session
	//    option not set".
	// Only prompt interactively when stdin is a terminal; otherwise leave the
	// default chain untouched so headless agents keep working with statically
	// supplied credentials (e.g. exported environment variables).
	if isTerminal(os.Stdin) {
		opts = append(opts, awsconfig.WithAssumeRoleCredentialOptions(func(o *stscreds.AssumeRoleOptions) {
			o.TokenProvider = stscreds.StdinTokenProvider
		}))
	}

	return awsconfig.LoadDefaultConfig(ctx, opts...)
}

// isTerminal reports whether f is attached to an interactive terminal. It is
// used to decide whether MFA token codes can be prompted for on stdin.
func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// FromTargetConfig parses the target configuration JSON into a Config struct
func FromTargetConfig(targetConfig json.RawMessage) *Config {
	if targetConfig == nil {
		return &Config{}
	}
	config := &Config{}
	_ = json.Unmarshal(targetConfig, config)

	return config
}

