// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package sqs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres/prov"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/cfres/registry"
	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/config"
)

type sqsClientInterface interface {
	SetQueueAttributes(ctx context.Context, params *sqs.SetQueueAttributesInput, optFns ...func(*sqs.Options)) (*sqs.SetQueueAttributesOutput, error)
	GetQueueAttributes(ctx context.Context, params *sqs.GetQueueAttributesInput, optFns ...func(*sqs.Options)) (*sqs.GetQueueAttributesOutput, error)
}

type QueuePolicy struct {
	cfg *config.Config
}

var _ prov.Provisioner = &QueuePolicy{}

func init() {
	registry.Register("AWS::SQS::QueuePolicy",
		[]resource.Operation{
			resource.OperationCreate,
			resource.OperationRead,
			resource.OperationUpdate,
			resource.OperationDelete,
			resource.OperationCheckStatus,
		},
		func(cfg *config.Config) prov.Provisioner {
			return &QueuePolicy{cfg: cfg}
		})
}

func (qp *QueuePolicy) Create(ctx context.Context, request *resource.CreateRequest) (*resource.CreateResult, error) {
	awsCfg, err := qp.cfg.ToAwsConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading AWS config: %w", err)
	}
	return qp.createWithClient(ctx, sqs.NewFromConfig(awsCfg), request)
}

func (qp *QueuePolicy) createWithClient(ctx context.Context, client sqsClientInterface, request *resource.CreateRequest) (*resource.CreateResult, error) {
	queues, policyDoc, err := parseQueuePolicyProperties(request.Properties)
	if err != nil {
		return nil, err
	}

	policyJSON, err := json.Marshal(policyDoc)
	if err != nil {
		return nil, fmt.Errorf("marshalling policy document: %w", err)
	}
	policyStr := string(policyJSON)

	for _, queueURL := range queues {
		if _, err := client.SetQueueAttributes(ctx, &sqs.SetQueueAttributesInput{
			QueueUrl: &queueURL,
			Attributes: map[string]string{
				string(sqstypes.QueueAttributeNamePolicy): policyStr,
			},
		}); err != nil {
			return nil, fmt.Errorf("setting policy on queue %s: %w", queueURL, err)
		}
	}

	nativeID := strings.Join(queues, "|")

	resultProps := map[string]any{
		"Queues":         queues,
		"PolicyDocument": policyDoc,
	}
	resultJSON, _ := json.Marshal(resultProps)

	return &resource.CreateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationCreate,
			OperationStatus:    resource.OperationStatusSuccess,
			NativeID:           nativeID,
			ResourceProperties: resultJSON,
		},
	}, nil
}

func (qp *QueuePolicy) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	awsCfg, err := qp.cfg.ToAwsConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading AWS config: %w", err)
	}
	return qp.readWithClient(ctx, sqs.NewFromConfig(awsCfg), request)
}

func (qp *QueuePolicy) readWithClient(ctx context.Context, client sqsClientInterface, request *resource.ReadRequest) (*resource.ReadResult, error) {
	queues := strings.Split(request.NativeID, "|")
	firstQueue := queues[0]

	output, err := client.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl:       &firstQueue,
		AttributeNames: []sqstypes.QueueAttributeName{sqstypes.QueueAttributeNamePolicy},
	})
	if err != nil {
		var queueNotExist *sqstypes.QueueDoesNotExist
		if errors.As(err, &queueNotExist) {
			return &resource.ReadResult{
				ResourceType: request.ResourceType,
				ErrorCode:    resource.OperationErrorCodeNotFound,
			}, nil
		}
		return nil, fmt.Errorf("getting queue attributes for %s: %w", firstQueue, err)
	}

	policyStr, ok := output.Attributes[string(sqstypes.QueueAttributeNamePolicy)]
	if !ok || policyStr == "" {
		return &resource.ReadResult{
			ResourceType: request.ResourceType,
			ErrorCode:    resource.OperationErrorCodeNotFound,
		}, nil
	}

	var policyDoc any
	if err := json.Unmarshal([]byte(policyStr), &policyDoc); err != nil {
		return nil, fmt.Errorf("parsing policy document: %w", err)
	}

	props := map[string]any{
		"Queues":         queues,
		"PolicyDocument": policyDoc,
	}
	propsJSON, _ := json.Marshal(props)

	return &resource.ReadResult{
		ResourceType: request.ResourceType,
		Properties:   string(propsJSON),
	}, nil
}

func (qp *QueuePolicy) Update(ctx context.Context, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	awsCfg, err := qp.cfg.ToAwsConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading AWS config: %w", err)
	}
	return qp.updateWithClient(ctx, sqs.NewFromConfig(awsCfg), request)
}

func (qp *QueuePolicy) updateWithClient(ctx context.Context, client sqsClientInterface, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	currentQueues := strings.Split(request.NativeID, "|")

	newQueues, policyDoc, err := parseQueuePolicyProperties(request.DesiredProperties)
	if err != nil {
		return nil, err
	}

	policyJSON, err := json.Marshal(policyDoc)
	if err != nil {
		return nil, fmt.Errorf("marshalling policy document: %w", err)
	}
	policyStr := string(policyJSON)

	// Remove policy from queues that are no longer in the desired list
	newQueueSet := make(map[string]bool, len(newQueues))
	for _, q := range newQueues {
		newQueueSet[q] = true
	}
	for _, q := range currentQueues {
		if !newQueueSet[q] {
			q := q
			if _, err := client.SetQueueAttributes(ctx, &sqs.SetQueueAttributesInput{
				QueueUrl: &q,
				Attributes: map[string]string{
					string(sqstypes.QueueAttributeNamePolicy): "",
				},
			}); err != nil {
				return nil, fmt.Errorf("removing policy from queue %s: %w", q, err)
			}
		}
	}

	// Set policy on all desired queues
	for _, queueURL := range newQueues {
		if _, err := client.SetQueueAttributes(ctx, &sqs.SetQueueAttributesInput{
			QueueUrl: &queueURL,
			Attributes: map[string]string{
				string(sqstypes.QueueAttributeNamePolicy): policyStr,
			},
		}); err != nil {
			return nil, fmt.Errorf("setting policy on queue %s: %w", queueURL, err)
		}
	}

	newNativeID := strings.Join(newQueues, "|")

	// Post-update Read
	readResult, err := qp.readWithClient(ctx, client, &resource.ReadRequest{
		NativeID:     newNativeID,
		ResourceType: request.ResourceType,
	})

	var resultProps json.RawMessage
	if err == nil && readResult.ErrorCode == "" {
		resultProps = json.RawMessage(readResult.Properties)
	}

	return &resource.UpdateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationUpdate,
			OperationStatus:    resource.OperationStatusSuccess,
			NativeID:           newNativeID,
			ResourceProperties: resultProps,
		},
	}, nil
}

func (qp *QueuePolicy) Delete(ctx context.Context, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	awsCfg, err := qp.cfg.ToAwsConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading AWS config: %w", err)
	}
	return qp.deleteWithClient(ctx, sqs.NewFromConfig(awsCfg), request)
}

func (qp *QueuePolicy) deleteWithClient(ctx context.Context, client sqsClientInterface, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	queues := strings.Split(request.NativeID, "|")

	for _, queueURL := range queues {
		if _, err := client.SetQueueAttributes(ctx, &sqs.SetQueueAttributesInput{
			QueueUrl: &queueURL,
			Attributes: map[string]string{
				string(sqstypes.QueueAttributeNamePolicy): "",
			},
		}); err != nil {
			var queueNotExist *sqstypes.QueueDoesNotExist
			if errors.As(err, &queueNotExist) {
				continue
			}
			return nil, fmt.Errorf("removing policy from queue %s: %w", queueURL, err)
		}
	}

	return &resource.DeleteResult{
		ProgressResult: &resource.ProgressResult{
			Operation:       resource.OperationDelete,
			OperationStatus: resource.OperationStatusSuccess,
			NativeID:        request.NativeID,
		},
	}, nil
}

func (qp *QueuePolicy) Status(_ context.Context, _ *resource.StatusRequest) (*resource.StatusResult, error) {
	return nil, fmt.Errorf("queue policy operations are synchronous - status polling not needed")
}

func (qp *QueuePolicy) List(_ context.Context, _ *resource.ListRequest) (*resource.ListResult, error) {
	return nil, fmt.Errorf("operation not implemented - queue policy is not discoverable")
}

func parseQueuePolicyProperties(raw json.RawMessage) ([]string, any, error) {
	var props map[string]any
	if err := json.Unmarshal(raw, &props); err != nil {
		return nil, nil, fmt.Errorf("parsing properties: %w", err)
	}

	queuesProp, ok := props["Queues"]
	if !ok {
		return nil, nil, fmt.Errorf("queues is required")
	}

	queuesArr, ok := queuesProp.([]any)
	if !ok || len(queuesArr) == 0 {
		return nil, nil, fmt.Errorf("queues must be a non-empty array")
	}

	queues := make([]string, 0, len(queuesArr))
	for _, q := range queuesArr {
		s, ok := q.(string)
		if !ok {
			return nil, nil, fmt.Errorf("queue URL must be a string, got %T", q)
		}
		queues = append(queues, s)
	}

	policyDoc, ok := props["PolicyDocument"]
	if !ok {
		return nil, nil, fmt.Errorf("PolicyDocument is required")
	}

	return queues, policyDoc, nil
}
