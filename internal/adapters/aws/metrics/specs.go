// This file declares, per resource kind, exactly which CloudWatch metrics
// cloudwatch.go queries and where each one's reduced value lands in
// ports.ResourceMetrics.
package metrics

import (
	"github.com/aws/aws-sdk-go-v2/aws"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
)

// field identifies which ports.ResourceMetrics slot one metricSpec's
// reduced Percentiles feeds. fieldErrorNumerator/fieldErrorDenominator are
// not slots of their own — assemble combines the two into ErrorRate, a
// windowed scalar rather than a distribution (see assemble's doc comment).
type field int

const (
	fieldNone field = iota
	fieldCPU
	fieldMemory
	fieldNetworkIn
	fieldNetworkOut
	fieldThroughput
	fieldRequests
	fieldLatencyP99
	fieldErrorNumerator
	fieldErrorDenominator
	fieldConcurrency
	fieldConnections
	fieldCustom
)

// metricSpec is one CloudWatch metric this package knows how to query and
// reduce.
type metricSpec struct {
	namespace, metricName, stat string
	dimensions                  []cwtypes.Dimension
	period                      int32
	target                      field
	customKey                   string // used only when target == fieldCustom
	// asRate divides each returned Sum-statistic value by its period,
	// turning "total events in this bucket" into "events per second" —
	// needed for any Sum-based metric (NetworkIn, RequestCount,
	// Invocations, ...) feeding a field ResourceMetrics documents as a
	// per-second rate.
	asRate bool
	// scale multiplies the (already rate-converted, if asRate) value —
	// used for unit conversions CloudWatch itself doesn't do, such as
	// TargetResponseTime's seconds needing to become the milliseconds
	// LatencyP99 is documented in.
	scale float64
}

// queryTarget is what cloudwatch.go's fetchBatch/assemble need to route one
// GetMetricData query's result back to the right resource and field.
type queryTarget struct {
	resourceIdx int
	field       field
	customKey   string
	asRate      bool
	scale       float64
	period      int32
}

// specsFor returns every metric this package collects for one resource,
// or nil if its kind has none — matching CloudWatch itself, which has no
// utilisation metric for kinds like a VPC or a security group.
func specsFor(r cloud.Resource, period int32) []metricSpec {
	switch r.Kind {
	case cloud.KindEC2Instance:
		dims := []cwtypes.Dimension{dim("InstanceId", r.NativeID)}
		return []metricSpec{
			{namespace: "AWS/EC2", metricName: "CPUUtilization", stat: "Average", dimensions: dims, period: period, target: fieldCPU},
			{namespace: "AWS/EC2", metricName: "NetworkIn", stat: "Sum", dimensions: dims, period: period, target: fieldNetworkIn, asRate: true},
			{namespace: "AWS/EC2", metricName: "NetworkOut", stat: "Sum", dimensions: dims, period: period, target: fieldNetworkOut, asRate: true},
		}

	case cloud.KindRDSInstance:
		dims := []cwtypes.Dimension{dim("DBInstanceIdentifier", r.NativeID)}
		return []metricSpec{
			{namespace: "AWS/RDS", metricName: "CPUUtilization", stat: "Average", dimensions: dims, period: period, target: fieldCPU},
			{namespace: "AWS/RDS", metricName: "DatabaseConnections", stat: "Average", dimensions: dims, period: period, target: fieldConnections},
			{namespace: "AWS/RDS", metricName: "FreeStorageSpace", stat: "Average", dimensions: dims, period: period, target: fieldCustom, customKey: "free_storage_bytes"},
		}

	case cloud.KindLambdaFunction:
		dims := []cwtypes.Dimension{dim("FunctionName", r.NativeID)}
		return []metricSpec{
			{namespace: "AWS/Lambda", metricName: "Invocations", stat: "Sum", dimensions: dims, period: period, target: fieldRequests, asRate: true},
			{namespace: "AWS/Lambda", metricName: "Duration", stat: "p99", dimensions: dims, period: period, target: fieldLatencyP99},
			{namespace: "AWS/Lambda", metricName: "Errors", stat: "Sum", dimensions: dims, period: period, target: fieldErrorNumerator},
			{namespace: "AWS/Lambda", metricName: "Invocations", stat: "Sum", dimensions: dims, period: period, target: fieldErrorDenominator},
			{namespace: "AWS/Lambda", metricName: "ConcurrentExecutions", stat: "Maximum", dimensions: dims, period: period, target: fieldConcurrency},
		}

	case cloud.KindALB:
		dims := []cwtypes.Dimension{dim("LoadBalancer", albDimensionValue(r.ARN))}
		return []metricSpec{
			{namespace: "AWS/ApplicationELB", metricName: "RequestCount", stat: "Sum", dimensions: dims, period: period, target: fieldRequests, asRate: true},
			{namespace: "AWS/ApplicationELB", metricName: "TargetResponseTime", stat: "p99", dimensions: dims, period: period, target: fieldLatencyP99, scale: 1000}, // seconds -> ms
			{namespace: "AWS/ApplicationELB", metricName: "HTTPCode_Target_5XX_Count", stat: "Sum", dimensions: dims, period: period, target: fieldErrorNumerator},
			{namespace: "AWS/ApplicationELB", metricName: "RequestCount", stat: "Sum", dimensions: dims, period: period, target: fieldErrorDenominator},
		}

	case cloud.KindNLB:
		dims := []cwtypes.Dimension{dim("LoadBalancer", albDimensionValue(r.ARN))}
		return []metricSpec{
			{namespace: "AWS/NetworkELB", metricName: "ActiveFlowCount", stat: "Average", dimensions: dims, period: period, target: fieldConnections},
			{namespace: "AWS/NetworkELB", metricName: "ProcessedBytes", stat: "Sum", dimensions: dims, period: period, target: fieldThroughput, asRate: true},
		}

	case cloud.KindDynamoDBTable:
		dims := []cwtypes.Dimension{dim("TableName", r.NativeID)}
		return []metricSpec{
			{namespace: "AWS/DynamoDB", metricName: "ConsumedReadCapacityUnits", stat: "Sum", dimensions: dims, period: period, target: fieldCustom, customKey: "consumed_rcu_per_sec", asRate: true},
			{namespace: "AWS/DynamoDB", metricName: "ConsumedWriteCapacityUnits", stat: "Sum", dimensions: dims, period: period, target: fieldCustom, customKey: "consumed_wcu_per_sec", asRate: true},
			{namespace: "AWS/DynamoDB", metricName: "ThrottledRequests", stat: "Sum", dimensions: dims, period: period, target: fieldCustom, customKey: "throttled_requests"},
		}

	case cloud.KindElastiCache:
		dims := []cwtypes.Dimension{dim("CacheClusterId", r.NativeID)}
		return []metricSpec{
			{namespace: "AWS/ElastiCache", metricName: "CPUUtilization", stat: "Average", dimensions: dims, period: period, target: fieldCPU},
			{namespace: "AWS/ElastiCache", metricName: "CurrConnections", stat: "Average", dimensions: dims, period: period, target: fieldConnections},
			{namespace: "AWS/ElastiCache", metricName: "Evictions", stat: "Sum", dimensions: dims, period: period, target: fieldCustom, customKey: "evictions_per_sec", asRate: true},
		}

	case cloud.KindS3Bucket:
		// Storage metrics are published once a day regardless of the
		// requested window's own period (see s3MetricPeriod's doc
		// comment), so these two override the caller-selected period.
		sizeDims := []cwtypes.Dimension{dim("BucketName", r.NativeID), dim("StorageType", "StandardStorage")}
		objDims := []cwtypes.Dimension{dim("BucketName", r.NativeID), dim("StorageType", "AllStorageTypes")}
		return []metricSpec{
			{namespace: "AWS/S3", metricName: "BucketSizeBytes", stat: "Average", dimensions: sizeDims, period: s3MetricPeriod, target: fieldCustom, customKey: "bucket_size_bytes"},
			{namespace: "AWS/S3", metricName: "NumberOfObjects", stat: "Average", dimensions: objDims, period: s3MetricPeriod, target: fieldCustom, customKey: "object_count"},
		}

	case cloud.KindECSService:
		dims := []cwtypes.Dimension{dim("ClusterName", r.Attr("cluster_id", "")), dim("ServiceName", r.NativeID)}
		return []metricSpec{
			{namespace: "AWS/ECS", metricName: "CPUUtilization", stat: "Average", dimensions: dims, period: period, target: fieldCPU},
			{namespace: "AWS/ECS", metricName: "MemoryUtilization", stat: "Average", dimensions: dims, period: period, target: fieldMemory},
		}

	default:
		return nil
	}
}

func dim(name, value string) cwtypes.Dimension {
	return cwtypes.Dimension{Name: aws.String(name), Value: aws.String(value)}
}
