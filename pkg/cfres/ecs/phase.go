// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package ecs

import (
	"context"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
)

func (s *Service) checkPhaseA(ctx context.Context, req *resource.StatusRequest,
	op resource.Operation, unixStart int64, ccapiToken string) (*resource.StatusResult, bool) {
	panic("checkPhaseA: implemented in Task 13")
}

func (s *Service) statusPhaseB(ctx context.Context, req *resource.StatusRequest,
	op resource.Operation, unixStart int64, cluster, service string) (*resource.StatusResult, error) {
	panic("statusPhaseB: implemented in Task 14")
}
