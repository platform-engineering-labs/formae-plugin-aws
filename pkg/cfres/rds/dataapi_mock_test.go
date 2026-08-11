// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package rds

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/aws-sdk-go-v2/service/rdsdata"
	"github.com/stretchr/testify/mock"
)

type mockDataAPIClient struct {
	mock.Mock
	// statements records the SQL of every call in order, so tests can assert
	// which statements reached the wire and in what sequence.
	statements []string
}

func (m *mockDataAPIClient) ExecuteStatement(ctx context.Context, params *rdsdata.ExecuteStatementInput, optFns ...func(*rdsdata.Options)) (*rdsdata.ExecuteStatementOutput, error) {
	if params.Sql != nil {
		m.statements = append(m.statements, *params.Sql)
	}
	args := m.Called(ctx, params)
	out, _ := args.Get(0).(*rdsdata.ExecuteStatementOutput)
	return out, args.Error(1)
}

type mockRDSClusterClient struct {
	mock.Mock
}

func (m *mockRDSClusterClient) DescribeDBClusters(ctx context.Context, params *rds.DescribeDBClustersInput, optFns ...func(*rds.Options)) (*rds.DescribeDBClustersOutput, error) {
	args := m.Called(ctx, params)
	out, _ := args.Get(0).(*rds.DescribeDBClustersOutput)
	return out, args.Error(1)
}
