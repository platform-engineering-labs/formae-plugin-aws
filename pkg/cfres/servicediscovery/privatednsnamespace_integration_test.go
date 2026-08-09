// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build integration

// Integration test for the registration contract an ECS service relies on: ECS
// registers a running task's address as an instance of the Cloud Map service
// named in the service's registry, and removes it again once the ECS service is
// deleted.
//
// Creating and deleting the resources says nothing about whether a task ever
// appeared in the registry, which is the only reason the registry association
// exists. This test stands up the same shape as the ECS-with-Cloud-Map
// conformance fixture — a VPC with internet egress, an ECS cluster running one
// FARGATE/awsvpc task, and a Cloud Map private DNS namespace and service — and
// reads the registry contents directly.
//
// The namespace itself goes through this package's provisioner rather than the
// Cloud Map SDK, so the path a real apply takes is the path under test.

package servicediscovery

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	servicediscoverysdk "github.com/aws/aws-sdk-go-v2/service/servicediscovery"
	servicediscoverytypes "github.com/aws/aws-sdk-go-v2/service/servicediscovery/types"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/config"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
)

const (
	// itestPrefix names every resource this test creates. It carries the test
	// prefix the shared account's sweep matches on, so anything a killed run
	// leaves behind is reaped rather than accumulating.
	itestPrefix = "formae-plugin-sdk-test-cmi"

	// The range is this test's alone, so a VPC it leaves behind is told apart
	// from a conformance fixture's by its address range as well as its name.
	itestVpcCIDR    = "10.238.0.0/16"
	itestSubnetCIDR = "10.238.1.0/24"

	// itestImage matches the conformance fixture: a public image the task pulls
	// over the internet gateway below.
	itestImage = "nginx:latest"

	// awsInstanceIPv4Attribute is the attribute Cloud Map records an A record's
	// address under, which for an awsvpc task is the task ENI's private address.
	awsInstanceIPv4Attribute = "AWS_INSTANCE_IPV4"

	// taskPrivateIPDetail is the detail an ECS task's ENI attachment reports its
	// private address under.
	taskPrivateIPDetail = "privateIPv4Address"

	// elasticNetworkInterfaceAttachment is the attachment type an awsvpc task
	// carries its ENI as.
	elasticNetworkInterfaceAttachment = "ElasticNetworkInterface"
)

// The ceilings the waits run against. Each is generous against what the
// operation takes in practice — a namespace settles in well under a minute, a
// task reaches RUNNING in a couple, and deregistration follows the task
// stopping — and every one of them fails the test when it runs out, so a
// contract that never holds is a failure rather than a pass that waited.
const (
	itestNamespaceTimeout      = 4 * time.Minute
	itestTaskRunningTimeout    = 6 * time.Minute
	itestRegistrationTimeout   = 3 * time.Minute
	itestServiceDrainTimeout   = 5 * time.Minute
	itestDeregistrationTimeout = 5 * time.Minute
	itestCleanupTimeout        = 2 * time.Minute
	itestPollInterval          = 5 * time.Second
	itestNamespacePollInterval = 3 * time.Second
)

// TestPrivateDnsNamespace_Integration_ECSRegistersAndDeregistersTask asserts
// that an ECS service whose registry names a Cloud Map service puts its running
// task's address into that service, and takes it out again when the ECS service
// is deleted.
func TestPrivateDnsNamespace_Integration_ECSRegistersAndDeregistersTask(t *testing.T) {
	region := os.Getenv("AWS_REGION")
	if region == "" {
		t.Skip("AWS_REGION not set; skipping integration test")
	}

	ctx := context.Background()
	cfg := &config.Config{Region: region}
	awsCfg, err := cfg.ToAwsConfig(ctx)
	require.NoError(t, err, "loading AWS config")

	ec2Client := ec2.NewFromConfig(awsCfg)
	ecsClient := ecs.NewFromConfig(awsCfg)
	cloudMapClient := servicediscoverysdk.NewFromConfig(awsCfg)

	suffix := strings.ReplaceAll(uuid.New().String()[:8], "-", "")
	names := itestNames(suffix)

	vpcID, subnetID := setupNetwork(t, ctx, ec2Client, names)
	clusterARN := setupCluster(t, ctx, ecsClient, names)
	taskDefinitionARN := setupTaskDefinition(t, ctx, ecsClient, names)
	namespaceID := setupNamespace(t, ctx, cfg, names, vpcID)
	cloudMapServiceID, cloudMapServiceARN := setupCloudMapService(t, ctx, cloudMapClient, names, namespaceID)

	// The ECS service under test: one FARGATE/awsvpc task registering with the
	// Cloud Map service above.
	_, err = ecsClient.CreateService(ctx, &ecs.CreateServiceInput{
		Cluster:            aws.String(clusterARN),
		ServiceName:        aws.String(names.ecsService),
		TaskDefinition:     aws.String(taskDefinitionARN),
		DesiredCount:       aws.Int32(1),
		LaunchType:         ecstypes.LaunchTypeFargate,
		SchedulingStrategy: ecstypes.SchedulingStrategyReplica,
		NetworkConfiguration: &ecstypes.NetworkConfiguration{
			AwsvpcConfiguration: &ecstypes.AwsVpcConfiguration{
				Subnets:        []string{subnetID},
				AssignPublicIp: ecstypes.AssignPublicIpEnabled,
			},
		},
		ServiceRegistries: []ecstypes.ServiceRegistry{
			{RegistryArn: aws.String(cloudMapServiceARN)},
		},
	})
	require.NoError(t, err, "creating ECS service %s", names.ecsService)

	// The delete below is the test's own step; this covers a failure before it.
	ecsServiceDeleted := false
	t.Cleanup(func() {
		if ecsServiceDeleted {
			return
		}
		if err := deleteECSService(context.Background(), ecsClient, clusterARN, names.ecsService); err != nil {
			t.Logf("warning: deleting ECS service %s: %v", names.ecsService, err)
		}
	})

	// --- registration ---

	taskIP := awaitRunningTaskIP(t, ctx, ecsClient, clusterARN, names.ecsService)
	t.Logf("task of ECS service %s is running at %s", names.ecsService, taskIP)

	var registeredIPs []string
	err = waitFor(ctx, itestRegistrationTimeout, itestPollInterval, func(ctx context.Context) (bool, error) {
		ips, err := registeredInstanceIPs(ctx, cloudMapClient, cloudMapServiceID)
		if err != nil {
			return false, err
		}
		registeredIPs = ips
		return len(ips) > 0, nil
	})
	require.NoError(t, err,
		"Cloud Map service %s never reported a registered instance: ECS did not register its running task at %s",
		cloudMapServiceID, taskIP)
	assert.Equal(t, []string{taskIP}, registeredIPs,
		"the registered instance should carry the running task's private address")

	// --- deregistration ---

	require.NoError(t, deleteECSService(ctx, ecsClient, clusterARN, names.ecsService),
		"deleting ECS service %s", names.ecsService)
	ecsServiceDeleted = true

	// ECS deregisters the instances of a deleted service asynchronously, so the
	// wait tolerates the settling window and fails once it runs out.
	var remainingIPs []string
	err = waitFor(ctx, itestDeregistrationTimeout, itestPollInterval, func(ctx context.Context) (bool, error) {
		ips, err := registeredInstanceIPs(ctx, cloudMapClient, cloudMapServiceID)
		if err != nil {
			return false, err
		}
		remainingIPs = ips
		return len(ips) == 0, nil
	})
	require.NoError(t, err,
		"Cloud Map service %s still holds instances %v after its ECS service was deleted",
		cloudMapServiceID, remainingIPs)
}

// itestResourceNames are the names of the resources one run of this test
// creates, all derived from the same suffix so a run's resources are
// recognisable together.
type itestResourceNames struct {
	suffix         string
	vpc            string
	subnet         string
	internetGW     string
	cluster        string
	taskDefinition string
	namespace      string
	cloudMapSvc    string
	ecsService     string
}

func itestNames(suffix string) itestResourceNames {
	return itestResourceNames{
		suffix:         suffix,
		vpc:            fmt.Sprintf("%s-vpc-%s", itestPrefix, suffix),
		subnet:         fmt.Sprintf("%s-subnet-%s", itestPrefix, suffix),
		internetGW:     fmt.Sprintf("%s-igw-%s", itestPrefix, suffix),
		cluster:        fmt.Sprintf("%s-cluster-%s", itestPrefix, suffix),
		taskDefinition: fmt.Sprintf("%s-taskdef-%s", itestPrefix, suffix),
		namespace:      fmt.Sprintf("%s-ns-%s.local", itestPrefix, suffix),
		cloudMapSvc:    fmt.Sprintf("%s-svc-%s", itestPrefix, suffix),
		ecsService:     fmt.Sprintf("%s-ecs-%s", itestPrefix, suffix),
	}
}

// setupNetwork provisions the VPC and subnet the task runs in, with the DNS
// attributes the namespace's private hosted zone needs and an internet gateway
// the task pulls its image over. Returns the VPC and subnet ids.
func setupNetwork(t *testing.T, ctx context.Context, client *ec2.Client, names itestResourceNames) (string, string) {
	t.Helper()

	vpcOut, err := client.CreateVpc(ctx, &ec2.CreateVpcInput{
		CidrBlock:         aws.String(itestVpcCIDR),
		TagSpecifications: nameTagSpecification(ec2types.ResourceTypeVpc, names.vpc),
	})
	require.NoError(t, err, "creating VPC")
	vpcID := aws.ToString(vpcOut.Vpc.VpcId)
	t.Cleanup(func() {
		deleteWithRetry(t, "VPC "+vpcID, func(ctx context.Context) error {
			_, err := client.DeleteVpc(ctx, &ec2.DeleteVpcInput{VpcId: aws.String(vpcID)})
			return err
		})
	})

	// A namespace's hosted zone resolves for the VPC only with both DNS
	// attributes on, and each takes a call of its own.
	_, err = client.ModifyVpcAttribute(ctx, &ec2.ModifyVpcAttributeInput{
		VpcId:            aws.String(vpcID),
		EnableDnsSupport: &ec2types.AttributeBooleanValue{Value: aws.Bool(true)},
	})
	require.NoError(t, err, "enabling DNS support on VPC %s", vpcID)
	_, err = client.ModifyVpcAttribute(ctx, &ec2.ModifyVpcAttributeInput{
		VpcId:              aws.String(vpcID),
		EnableDnsHostnames: &ec2types.AttributeBooleanValue{Value: aws.Bool(true)},
	})
	require.NoError(t, err, "enabling DNS hostnames on VPC %s", vpcID)

	igwOut, err := client.CreateInternetGateway(ctx, &ec2.CreateInternetGatewayInput{
		TagSpecifications: nameTagSpecification(ec2types.ResourceTypeInternetGateway, names.internetGW),
	})
	require.NoError(t, err, "creating internet gateway")
	igwID := aws.ToString(igwOut.InternetGateway.InternetGatewayId)
	t.Cleanup(func() {
		deleteWithRetry(t, "internet gateway "+igwID, func(ctx context.Context) error {
			if _, err := client.DetachInternetGateway(ctx, &ec2.DetachInternetGatewayInput{
				InternetGatewayId: aws.String(igwID),
				VpcId:             aws.String(vpcID),
			}); err != nil {
				return err
			}
			_, err := client.DeleteInternetGateway(ctx, &ec2.DeleteInternetGatewayInput{
				InternetGatewayId: aws.String(igwID),
			})
			return err
		})
	})
	_, err = client.AttachInternetGateway(ctx, &ec2.AttachInternetGatewayInput{
		InternetGatewayId: aws.String(igwID),
		VpcId:             aws.String(vpcID),
	})
	require.NoError(t, err, "attaching internet gateway %s to VPC %s", igwID, vpcID)

	// The subnet is implicitly associated with the VPC's main route table, so a
	// default route on that table is all the egress the task needs. The route
	// and the table go with the VPC, so neither needs a cleanup of its own.
	routeTableID := mainRouteTableID(t, ctx, client, vpcID)
	_, err = client.CreateRoute(ctx, &ec2.CreateRouteInput{
		RouteTableId:         aws.String(routeTableID),
		DestinationCidrBlock: aws.String("0.0.0.0/0"),
		GatewayId:            aws.String(igwID),
	})
	require.NoError(t, err, "creating default route on route table %s", routeTableID)

	subnetOut, err := client.CreateSubnet(ctx, &ec2.CreateSubnetInput{
		VpcId:             aws.String(vpcID),
		CidrBlock:         aws.String(itestSubnetCIDR),
		AvailabilityZone:  aws.String(firstAvailabilityZone(t, ctx, client)),
		TagSpecifications: nameTagSpecification(ec2types.ResourceTypeSubnet, names.subnet),
	})
	require.NoError(t, err, "creating subnet")
	subnetID := aws.ToString(subnetOut.Subnet.SubnetId)
	t.Cleanup(func() {
		// A stopped task's ENI is released after the task itself is gone, so the
		// subnet delete is retried until the release lands.
		deleteWithRetry(t, "subnet "+subnetID, func(ctx context.Context) error {
			_, err := client.DeleteSubnet(ctx, &ec2.DeleteSubnetInput{SubnetId: aws.String(subnetID)})
			return err
		})
	})

	return vpcID, subnetID
}

func nameTagSpecification(resourceType ec2types.ResourceType, name string) []ec2types.TagSpecification {
	return []ec2types.TagSpecification{
		{
			ResourceType: resourceType,
			Tags:         []ec2types.Tag{{Key: aws.String("Name"), Value: aws.String(name)}},
		},
	}
}

// mainRouteTableID reports the route table a VPC's subnets are associated with
// unless they are associated with one explicitly.
func mainRouteTableID(t *testing.T, ctx context.Context, client *ec2.Client, vpcID string) string {
	t.Helper()
	out, err := client.DescribeRouteTables(ctx, &ec2.DescribeRouteTablesInput{
		Filters: []ec2types.Filter{{Name: aws.String("vpc-id"), Values: []string{vpcID}}},
	})
	require.NoError(t, err, "describing route tables of VPC %s", vpcID)
	for _, routeTable := range out.RouteTables {
		for _, association := range routeTable.Associations {
			if aws.ToBool(association.Main) {
				return aws.ToString(routeTable.RouteTableId)
			}
		}
	}
	t.Fatalf("VPC %s reports no main route table", vpcID)
	return ""
}

// firstAvailabilityZone picks the availability zone the subnet is created in,
// so the test is not tied to the zone names of one region. The zones are sorted
// so the same region always yields the same zone: not every zone of a region
// offers FARGATE capacity, and the first zone of a region does.
func firstAvailabilityZone(t *testing.T, ctx context.Context, client *ec2.Client) string {
	t.Helper()
	out, err := client.DescribeAvailabilityZones(ctx, &ec2.DescribeAvailabilityZonesInput{
		Filters: []ec2types.Filter{
			{Name: aws.String("state"), Values: []string{"available"}},
			{Name: aws.String("zone-type"), Values: []string{"availability-zone"}},
		},
	})
	require.NoError(t, err, "describing availability zones")
	zoneNames := make([]string, 0, len(out.AvailabilityZones))
	for _, zone := range out.AvailabilityZones {
		zoneNames = append(zoneNames, aws.ToString(zone.ZoneName))
	}
	require.NotEmpty(t, zoneNames, "no availability zone is available")
	sort.Strings(zoneNames)
	return zoneNames[0]
}

func setupCluster(t *testing.T, ctx context.Context, client *ecs.Client, names itestResourceNames) string {
	t.Helper()
	out, err := client.CreateCluster(ctx, &ecs.CreateClusterInput{
		ClusterName: aws.String(names.cluster),
	})
	require.NoError(t, err, "creating ECS cluster %s", names.cluster)
	clusterARN := aws.ToString(out.Cluster.ClusterArn)
	t.Cleanup(func() {
		deleteWithRetry(t, "ECS cluster "+clusterARN, func(ctx context.Context) error {
			_, err := client.DeleteCluster(ctx, &ecs.DeleteClusterInput{Cluster: aws.String(clusterARN)})
			return err
		})
	})
	return clusterARN
}

func setupTaskDefinition(t *testing.T, ctx context.Context, client *ecs.Client, names itestResourceNames) string {
	t.Helper()
	out, err := client.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family:                  aws.String(names.taskDefinition),
		RequiresCompatibilities: []ecstypes.Compatibility{ecstypes.CompatibilityFargate},
		NetworkMode:             ecstypes.NetworkModeAwsvpc,
		Cpu:                     aws.String("256"),
		Memory:                  aws.String("512"),
		ContainerDefinitions: []ecstypes.ContainerDefinition{
			{
				Name:      aws.String("test-container"),
				Image:     aws.String(itestImage),
				Essential: aws.Bool(true),
				PortMappings: []ecstypes.PortMapping{
					{ContainerPort: aws.Int32(80), Protocol: ecstypes.TransportProtocolTcp},
				},
			},
		},
	})
	require.NoError(t, err, "registering task definition %s", names.taskDefinition)
	taskDefinitionARN := aws.ToString(out.TaskDefinition.TaskDefinitionArn)
	t.Cleanup(func() {
		deleteWithRetry(t, "task definition "+taskDefinitionARN, func(ctx context.Context) error {
			_, err := client.DeregisterTaskDefinition(ctx, &ecs.DeregisterTaskDefinitionInput{
				TaskDefinition: aws.String(taskDefinitionARN),
			})
			return err
		})
	})
	return taskDefinitionARN
}

// setupNamespace creates the Cloud Map private DNS namespace through this
// package's provisioner and waits for it to settle, so the resource the
// registration is asserted against is one a real apply would have produced.
func setupNamespace(
	t *testing.T,
	ctx context.Context,
	cfg *config.Config,
	names itestResourceNames,
	vpcID string,
) string {
	t.Helper()

	provisioner := &PrivateDnsNamespace{
		cfg:           cfg,
		clientFactory: defaultServiceDiscoveryClientFactory,
		now:           func() time.Time { return time.Now().UTC() },
	}

	properties, err := json.Marshal(map[string]any{
		"Name":        names.namespace,
		"Vpc":         vpcID,
		"Description": "Integration test namespace for ECS service discovery",
		"Properties": map[string]any{
			"DnsProperties": map[string]any{"SOA": map[string]any{"TTL": 60}},
		},
	})
	require.NoError(t, err, "marshaling namespace properties")

	createResult, err := provisioner.Create(ctx, &resource.CreateRequest{
		ResourceType: resourceType,
		Label:        names.namespace,
		Properties:   properties,
	})
	require.NoError(t, err, "creating namespace %s", names.namespace)
	require.NotNil(t, createResult.ProgressResult, "namespace create returned no progress")
	namespaceID := createResult.ProgressResult.NativeID
	require.NotEmpty(t, namespaceID, "namespace create returned no native id")

	t.Cleanup(func() {
		deleteNamespace(t, provisioner, namespaceID)
	})

	require.NoError(t,
		awaitProvisionerSuccess(ctx, provisioner, namespaceID, createResult.ProgressResult.RequestID, itestNamespaceTimeout),
		"waiting for namespace %s to be created", namespaceID)
	return namespaceID
}

// deleteNamespace deletes a namespace through the provisioner and waits for the
// delete to settle. The provisioner re-issues a delete Cloud Map rejects while
// the namespace still holds resources, so this tolerates a Cloud Map service
// whose own delete has not landed yet.
func deleteNamespace(t *testing.T, provisioner *PrivateDnsNamespace, namespaceID string) {
	t.Helper()
	ctx := context.Background()
	deleteResult, err := provisioner.Delete(ctx, &resource.DeleteRequest{
		NativeID:     namespaceID,
		ResourceType: resourceType,
	})
	if err != nil {
		t.Logf("warning: deleting namespace %s: %v", namespaceID, err)
		return
	}
	if deleteResult.ProgressResult.OperationStatus == resource.OperationStatusSuccess {
		return
	}
	if err := awaitProvisionerSuccess(
		ctx, provisioner, namespaceID, deleteResult.ProgressResult.RequestID, itestNamespaceTimeout,
	); err != nil {
		t.Logf("warning: waiting for namespace %s to be deleted: %v", namespaceID, err)
	}
}

// awaitProvisionerSuccess polls the provisioner's Status until the operation the
// request id names reports success, carrying forward the request id each poll
// reports so a phase change is followed.
func awaitProvisionerSuccess(
	ctx context.Context,
	provisioner *PrivateDnsNamespace,
	namespaceID string,
	requestID string,
	timeout time.Duration,
) error {
	return waitFor(ctx, timeout, itestNamespacePollInterval, func(ctx context.Context) (bool, error) {
		statusResult, err := provisioner.Status(ctx, &resource.StatusRequest{
			RequestID:    requestID,
			NativeID:     namespaceID,
			ResourceType: resourceType,
		})
		if err != nil {
			return false, err
		}
		progress := statusResult.ProgressResult
		if progress.RequestID != "" {
			requestID = progress.RequestID
		}
		switch progress.OperationStatus {
		case resource.OperationStatusSuccess:
			return true, nil
		case resource.OperationStatusFailure:
			return false, fmt.Errorf("namespace %s reported failure: %s", namespaceID, progress.StatusMessage)
		default:
			return false, nil
		}
	})
}

// setupCloudMapService creates the Cloud Map service the ECS service registers
// its tasks with, in the shape an awsvpc task registers under: an A record for
// the task's own address, health reported by ECS rather than by Route 53.
func setupCloudMapService(
	t *testing.T,
	ctx context.Context,
	client *servicediscoverysdk.Client,
	names itestResourceNames,
	namespaceID string,
) (string, string) {
	t.Helper()
	out, err := client.CreateService(ctx, &servicediscoverysdk.CreateServiceInput{
		Name:        aws.String(names.cloudMapSvc),
		NamespaceId: aws.String(namespaceID),
		Description: aws.String("Integration test Cloud Map service for ECS registration"),
		DnsConfig: &servicediscoverytypes.DnsConfig{
			RoutingPolicy: servicediscoverytypes.RoutingPolicyMultivalue,
			DnsRecords: []servicediscoverytypes.DnsRecord{
				{Type: servicediscoverytypes.RecordTypeA, TTL: aws.Int64(15)},
			},
		},
		HealthCheckCustomConfig: &servicediscoverytypes.HealthCheckCustomConfig{
			FailureThreshold: aws.Int32(1),
		},
	})
	require.NoError(t, err, "creating Cloud Map service %s", names.cloudMapSvc)
	serviceID := aws.ToString(out.Service.Id)
	serviceARN := aws.ToString(out.Service.Arn)
	t.Cleanup(func() {
		// Cloud Map rejects the delete while instances are still registered, so
		// this is retried for as long as deregistration may take.
		deleteWithRetry(t, "Cloud Map service "+serviceID, func(ctx context.Context) error {
			_, err := client.DeleteService(ctx, &servicediscoverysdk.DeleteServiceInput{
				Id: aws.String(serviceID),
			})
			return err
		})
	})
	return serviceID, serviceARN
}

// awaitRunningTaskIP waits for the ECS service to have exactly one running task
// and reports the private address of that task's ENI — the address ECS is
// expected to register.
func awaitRunningTaskIP(
	t *testing.T,
	ctx context.Context,
	client *ecs.Client,
	cluster string,
	service string,
) string {
	t.Helper()

	var taskIP string
	err := waitFor(ctx, itestTaskRunningTimeout, itestPollInterval, func(ctx context.Context) (bool, error) {
		listed, err := client.ListTasks(ctx, &ecs.ListTasksInput{
			Cluster:       aws.String(cluster),
			ServiceName:   aws.String(service),
			DesiredStatus: ecstypes.DesiredStatusRunning,
		})
		if err != nil {
			return false, err
		}
		if len(listed.TaskArns) == 0 {
			return false, nil
		}
		described, err := client.DescribeTasks(ctx, &ecs.DescribeTasksInput{
			Cluster: aws.String(cluster),
			Tasks:   listed.TaskArns,
		})
		if err != nil {
			return false, err
		}
		for _, task := range described.Tasks {
			if aws.ToString(task.LastStatus) != "RUNNING" {
				continue
			}
			if ip := taskPrivateIP(task); ip != "" {
				taskIP = ip
				return true, nil
			}
		}
		return false, nil
	})
	if err != nil {
		t.Fatalf("ECS service %s never reached a running task with an address: %v%s",
			service, err, stoppedTaskReasons(ctx, client, cluster, service))
	}
	return taskIP
}

// taskPrivateIP reports the private address of an awsvpc task's ENI.
func taskPrivateIP(task ecstypes.Task) string {
	for _, attachment := range task.Attachments {
		if aws.ToString(attachment.Type) != elasticNetworkInterfaceAttachment {
			continue
		}
		for _, detail := range attachment.Details {
			if aws.ToString(detail.Name) == taskPrivateIPDetail {
				return aws.ToString(detail.Value)
			}
		}
	}
	return ""
}

// stoppedTaskReasons renders why a service's tasks stopped, so a service that
// never reaches a running task fails with the reason rather than only with the
// timeout. It reports the empty string when there is nothing to add.
func stoppedTaskReasons(ctx context.Context, client *ecs.Client, cluster, service string) string {
	listed, err := client.ListTasks(ctx, &ecs.ListTasksInput{
		Cluster:       aws.String(cluster),
		ServiceName:   aws.String(service),
		DesiredStatus: ecstypes.DesiredStatusStopped,
	})
	if err != nil || len(listed.TaskArns) == 0 {
		return ""
	}
	described, err := client.DescribeTasks(ctx, &ecs.DescribeTasksInput{
		Cluster: aws.String(cluster),
		Tasks:   listed.TaskArns,
	})
	if err != nil {
		return ""
	}
	var reasons []string
	for _, task := range described.Tasks {
		if reason := aws.ToString(task.StoppedReason); reason != "" {
			reasons = append(reasons, reason)
		}
	}
	if len(reasons) == 0 {
		return ""
	}
	return "; stopped tasks reported: " + strings.Join(reasons, " | ")
}

// registeredInstanceIPs reports the addresses currently registered as instances
// of a Cloud Map service.
func registeredInstanceIPs(
	ctx context.Context,
	client *servicediscoverysdk.Client,
	serviceID string,
) ([]string, error) {
	var ips []string
	var nextToken *string
	for {
		out, err := client.ListInstances(ctx, &servicediscoverysdk.ListInstancesInput{
			ServiceId: aws.String(serviceID),
			NextToken: nextToken,
		})
		if err != nil {
			return nil, fmt.Errorf("ListInstances %s: %w", serviceID, err)
		}
		for _, instance := range out.Instances {
			ips = append(ips, instance.Attributes[awsInstanceIPv4Attribute])
		}
		if aws.ToString(out.NextToken) == "" {
			return ips, nil
		}
		nextToken = out.NextToken
	}
}

// deleteECSService deletes an ECS service and waits until ECS reports it gone
// with no task left running, which is what releases the task's ENI and lets the
// registered instance be deregistered.
func deleteECSService(ctx context.Context, client *ecs.Client, cluster, service string) error {
	if _, err := client.DeleteService(ctx, &ecs.DeleteServiceInput{
		Cluster: aws.String(cluster),
		Service: aws.String(service),
		Force:   aws.Bool(true),
	}); err != nil {
		return fmt.Errorf("DeleteService %s: %w", service, err)
	}
	return waitFor(ctx, itestServiceDrainTimeout, itestPollInterval, func(ctx context.Context) (bool, error) {
		out, err := client.DescribeServices(ctx, &ecs.DescribeServicesInput{
			Cluster:  aws.String(cluster),
			Services: []string{service},
		})
		if err != nil {
			return false, err
		}
		if len(out.Services) == 0 {
			return true, nil
		}
		described := out.Services[0]
		return aws.ToString(described.Status) == "INACTIVE" &&
			described.RunningCount == 0 &&
			described.PendingCount == 0, nil
	})
}

// deleteWithRetry runs a delete until it succeeds or the teardown ceiling runs
// out. A delete blocked by a dependency that is still being released — a task
// ENI, an instance ECS has yet to deregister — succeeds on a later attempt, and
// one that never does is reported rather than failing the test: the assertions
// have already run by then, and a leftover resource is the account sweep's to
// reap.
func deleteWithRetry(t *testing.T, description string, remove func(context.Context) error) {
	t.Helper()
	ctx := context.Background()
	var lastErr error
	err := waitFor(ctx, itestCleanupTimeout, itestPollInterval, func(ctx context.Context) (bool, error) {
		lastErr = remove(ctx)
		return lastErr == nil, nil
	})
	if err != nil {
		t.Logf("warning: deleting %s: %v (last error: %v)", description, err, lastErr)
	}
}

// waitFor calls probe every interval until it reports done, reports an error, or
// the timeout runs out. A probe that reports neither done nor an error is one
// whose condition has not been reached yet.
func waitFor(
	ctx context.Context,
	timeout time.Duration,
	interval time.Duration,
	probe func(context.Context) (bool, error),
) error {
	deadline := time.Now().Add(timeout)
	for {
		done, err := probe(ctx)
		if err != nil {
			return err
		}
		if done {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out after %s", timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}
