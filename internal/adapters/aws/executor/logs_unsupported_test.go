package executor

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/execute"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

func TestUnsupportedExecutor_SetLogRetentionExplainsWhyEveryMethodFails(t *testing.T) {
	ex := NewSetLogRetentionExecutor()
	assert.Equal(t, optimize.ActionSetLogRetention, ex.Action())
	assert.Contains(t, ex.RequiredActions(), "logs:PutRetentionPolicy")

	_, err := ex.Plan(context.Background(), ports.ExecutionPlanInput{})
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrUnavailable)
	assert.ErrorContains(t, err, "CloudWatch Logs")

	assert.Error(t, ex.Preflight(context.Background(), nil, execute.Plan{}))
	_, err = ex.Apply(context.Background(), nil, execute.Plan{}, execute.Step{})
	assert.Error(t, err)
	assert.Error(t, ex.Rollback(context.Background(), nil, execute.Plan{}, execute.Step{}))
}
