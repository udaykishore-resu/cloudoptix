package compiler

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/adapters/pricing"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/simulate"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

func testCompiler(t *testing.T) *Compiler {
	t.Helper()
	c := New(pricing.New())
	c.Clock = core.FixedClock{T: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	return c
}

// TestCompile_DeltaCoverageConfidenceArithmetic exercises the full pipeline —
// a create, an update, an unpriced resource and a usage-dependent resource —
// and checks every roll-up field CompilationResult.Summarize computes.
func TestCompile_DeltaCoverageConfidenceArithmetic(t *testing.T) {
	c := testCompiler(t)
	planJSON := `{
	  "format_version": "1.2",
	  "resource_changes": [
	    {
	      "address": "aws_instance.api",
	      "type": "aws_instance",
	      "name": "api",
	      "change": {"actions": ["create"], "before": null,
	        "after": {"instance_type": "m5.large", "availability_zone": "us-east-1a", "tags": {"owner": "x"}}}
	    },
	    {
	      "address": "aws_db_instance.primary",
	      "type": "aws_db_instance",
	      "name": "primary",
	      "change": {"actions": ["update"],
	        "before": {"instance_class": "db.r5.large", "engine": "postgres", "multi_az": false, "allocated_storage": 100, "tags": {"owner": "x"}},
	        "after":  {"instance_class": "db.r5.xlarge", "engine": "postgres", "multi_az": false, "allocated_storage": 100, "tags": {"owner": "x"}}}
	    },
	    {
	      "address": "aws_msk_cluster.events",
	      "type": "aws_msk_cluster",
	      "name": "events",
	      "change": {"actions": ["create"], "before": null, "after": {"tags": {"owner": "x"}}}
	    },
	    {
	      "address": "aws_lambda_function.worker",
	      "type": "aws_lambda_function",
	      "name": "worker",
	      "change": {"actions": ["create"], "before": null,
	        "after": {"memory_size": 512, "architectures": ["x86_64"], "tags": {"owner": "x"}}}
	    }
	  ]
	}`
	result, err := c.Compile("tenant-1", ports.CompileRequest{
		Source: simulate.SourceTerraformPlan, Label: "PR #42", Region: "us-east-1",
		Content: []byte(planJSON),
	})
	require.NoError(t, err)

	require.Len(t, result.Changes, 4)
	byAddr := map[string]simulate.PricedChange{}
	for _, c := range result.Changes {
		byAddr[c.Address] = c
	}

	// The EC2 instance is deterministically priced: not usage-dependent.
	ec2 := byAddr["aws_instance.api"]
	assert.False(t, ec2.Unpriced)
	assert.False(t, ec2.UsageDependent)
	assert.True(t, ec2.AfterMonthly.GreaterThan(core.ZeroUSD()))
	assert.True(t, ec2.BeforeMonthly.IsZero())

	// The RDS resize has both a before and after cost, and a positive delta.
	rds := byAddr["aws_db_instance.primary"]
	assert.False(t, rds.Unpriced)
	assert.True(t, rds.BeforeMonthly.GreaterThan(core.ZeroUSD()))
	assert.True(t, rds.AfterMonthly.GreaterThan(rds.BeforeMonthly))
	assert.True(t, rds.MonthlyDelta.GreaterThan(core.ZeroUSD()))

	// MSK has no pricing data at all: Unpriced, not silently zero-and-free.
	msk := byAddr["aws_msk_cluster.events"]
	assert.True(t, msk.Unpriced)
	assert.NotEmpty(t, msk.UnpricedReason)
	assert.True(t, msk.BeforeMonthly.IsZero())
	assert.True(t, msk.AfterMonthly.IsZero())

	// Lambda is usage-dependent with stated, non-empty assumptions.
	lambda := byAddr["aws_lambda_function.worker"]
	assert.False(t, lambda.Unpriced)
	assert.True(t, lambda.UsageDependent)
	assert.NotEmpty(t, lambda.Assumptions)
	assert.True(t, lambda.AfterMonthly.GreaterThan(core.ZeroUSD()))

	// Roll-up arithmetic.
	wantBaseline := rds.BeforeMonthly // only RDS has a nonzero before
	assert.Equal(t, wantBaseline.Micros(), result.BaselineMonthly.Micros())
	wantProjected := ec2.AfterMonthly.MustAdd(rds.AfterMonthly).MustAdd(lambda.AfterMonthly)
	assert.Equal(t, wantProjected.Micros(), result.ProjectedMonthly.Micros())
	assert.Equal(t, result.ProjectedMonthly.MustSub(result.BaselineMonthly).Micros(), result.MonthlyDelta.Micros())
	assert.InDelta(t, result.MonthlyDelta.Ratio(result.BaselineMonthly)*100, result.DeltaPct, 0.001)

	assert.Equal(t, 1, result.UnpricedCount)
	assert.InDelta(t, 3.0/4.0, result.Coverage, 0.0001)
	// Confidence discounts coverage by the usage-dependent share of projected cost.
	assert.True(t, float64(result.Confidence) < result.Coverage, "usage-dependent share must discount confidence below raw coverage")
	assert.True(t, float64(result.Confidence) > 0)

	assert.Equal(t, 3, result.CreatedCount) // instance + msk + lambda
	assert.Equal(t, 1, result.UpdatedCount)

	// Changes are sorted by |delta| descending (top movers).
	require.True(t, len(result.Changes) >= 2)
	for i := 1; i < len(result.Changes); i++ {
		assert.GreaterOrEqual(t, result.Changes[i-1].MonthlyDelta.Abs().Micros(), result.Changes[i].MonthlyDelta.Abs().Micros())
	}
}

func TestCompile_CountExpansionProducesRisk(t *testing.T) {
	c := testCompiler(t)
	planJSON := `{
	  "resource_changes": [
	    {"address": "aws_instance.worker[0]", "type": "aws_instance", "name": "worker", "index": 0,
	     "change": {"actions": ["create"], "before": null, "after": {"instance_type": "m5.large", "tags": {"a":"b"}}}},
	    {"address": "aws_instance.worker[1]", "type": "aws_instance", "name": "worker", "index": 1,
	     "change": {"actions": ["create"], "before": null, "after": {"instance_type": "m5.large", "tags": {"a":"b"}}}},
	    {"address": "aws_instance.worker[2]", "type": "aws_instance", "name": "worker", "index": 2,
	     "change": {"actions": ["create"], "before": null, "after": {"instance_type": "m5.large", "tags": {"a":"b"}}}}
	  ]
	}`
	result, err := c.Compile("t1", ports.CompileRequest{
		Source: simulate.SourceTerraformPlan, Region: "us-east-1", Content: []byte(planJSON),
	})
	require.NoError(t, err)
	var found *simulate.CostRisk
	for i := range result.Risks {
		if result.Risks[i].Code == "count_expansion" {
			found = &result.Risks[i]
		}
	}
	require.NotNil(t, found, "expected a count_expansion risk")
	assert.Equal(t, "aws_instance.worker", found.Address)
	assert.True(t, found.MonthlyImpact.GreaterThan(core.ZeroUSD()))
}

func TestCompile_UnknownResourceTypeIsUnpriced(t *testing.T) {
	c := testCompiler(t)
	planJSON := `{"resource_changes": [
	  {"address": "aws_glue_job.etl", "type": "aws_glue_job", "name": "etl",
	   "change": {"actions": ["create"], "before": null, "after": {}}}
	]}`
	result, err := c.Compile("t1", ports.CompileRequest{Source: simulate.SourceTerraformPlan, Region: "us-east-1", Content: []byte(planJSON)})
	require.NoError(t, err)
	require.Len(t, result.Changes, 1)
	assert.True(t, result.Changes[0].Unpriced)
	assert.Contains(t, result.Changes[0].UnpricedReason, "aws_glue_job")
}

func TestCompile_FreeResourceIsNotUnpriced(t *testing.T) {
	c := testCompiler(t)
	planJSON := `{"resource_changes": [
	  {"address": "aws_iam_role.exec", "type": "aws_iam_role", "name": "exec",
	   "change": {"actions": ["create"], "before": null, "after": {}}}
	]}`
	result, err := c.Compile("t1", ports.CompileRequest{Source: simulate.SourceTerraformPlan, Region: "us-east-1", Content: []byte(planJSON)})
	require.NoError(t, err)
	require.Len(t, result.Changes, 1)
	ch := result.Changes[0]
	assert.False(t, ch.Unpriced, "a known-free resource type is free, not unpriced")
	assert.True(t, ch.AfterMonthly.IsZero())
	assert.Equal(t, 1.0, result.Coverage, "a free resource counts as priced coverage")
}

func TestCompile_VPCGatewayEndpointIsFreeNotUnpriced(t *testing.T) {
	c := testCompiler(t)
	planJSON := `{"resource_changes": [
	  {"address": "aws_vpc_endpoint.s3", "type": "aws_vpc_endpoint", "name": "s3",
	   "change": {"actions": ["create"], "before": null, "after": {"vpc_endpoint_type": "Gateway", "service_name": "com.amazonaws.us-east-1.s3"}}}
	]}`
	result, err := c.Compile("t1", ports.CompileRequest{Source: simulate.SourceTerraformPlan, Region: "us-east-1", Content: []byte(planJSON)})
	require.NoError(t, err)
	require.Len(t, result.Changes, 1)
	ch := result.Changes[0]
	assert.False(t, ch.Unpriced)
	assert.True(t, ch.AfterMonthly.IsZero())
}

func TestCompile_AssumptionOverride(t *testing.T) {
	c := testCompiler(t)
	planJSON := `{"resource_changes": [
	  {"address": "aws_lambda_function.worker", "type": "aws_lambda_function", "name": "worker",
	   "change": {"actions": ["create"], "before": null, "after": {"memory_size": 256}}}
	]}`
	base, err := c.Compile("t1", ports.CompileRequest{Source: simulate.SourceTerraformPlan, Region: "us-east-1", Content: []byte(planJSON)})
	require.NoError(t, err)

	overridden, err := c.Compile("t1", ports.CompileRequest{
		Source: simulate.SourceTerraformPlan, Region: "us-east-1", Content: []byte(planJSON),
		Assumptions: map[string]float64{"aws_lambda_function.worker:lambda_invocations_month": 10_000_000},
	})
	require.NoError(t, err)
	assert.True(t, overridden.Changes[0].AfterMonthly.GreaterThan(base.Changes[0].AfterMonthly))
}
