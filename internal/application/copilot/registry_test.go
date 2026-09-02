package copilot

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// mutatingTool is a fake tool that lies about being read-only, to exercise
// Register's refusal path. It never actually mutates anything — this is a
// registration-time check, not a runtime one — but Definition() is what a
// real mutating tool would also declare wrong if the copilot registered it
// by mistake.
type mutatingTool struct {
	name     string
	readOnly bool
	perm     core.Permission
}

func (m mutatingTool) Definition() ports.ToolDefinition {
	return ports.ToolDefinition{Name: m.name, ReadOnly: m.readOnly, RequiredPermission: m.perm}
}

func (m mutatingTool) Invoke(context.Context, core.TenantID, map[string]any) (any, error) {
	return toolResult("unused", nil), nil
}

func TestRegistry_RefusesNonReadOnlyTool(t *testing.T) {
	r := NewRegistry()
	err := r.Register(mutatingTool{name: "delete_everything", readOnly: false, perm: core.PermExecutionStart})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ReadOnly")

	_, ok := r.Get("delete_everything")
	assert.False(t, ok, "a refused tool must not be registered")
}

func TestRegistry_RefusesEmptyName(t *testing.T) {
	r := NewRegistry()
	err := r.Register(mutatingTool{name: "", readOnly: true, perm: core.PermCostRead})
	assert.Error(t, err)
}

func TestRegistry_RefusesMissingPermission(t *testing.T) {
	r := NewRegistry()
	err := r.Register(mutatingTool{name: "get_thing", readOnly: true, perm: ""})
	assert.Error(t, err)
}

func TestRegistry_RefusesDuplicateName(t *testing.T) {
	r := NewRegistry()
	require.NoError(t, r.Register(mutatingTool{name: "get_thing", readOnly: true, perm: core.PermCostRead}))
	err := r.Register(mutatingTool{name: "get_thing", readOnly: true, perm: core.PermCostRead})
	assert.Error(t, err)
}

func TestRegistry_AcceptsReadOnlyTool(t *testing.T) {
	r := NewRegistry()
	require.NoError(t, r.Register(mutatingTool{name: "get_thing", readOnly: true, perm: core.PermCostRead}))
	tool, ok := r.Get("get_thing")
	require.True(t, ok)
	assert.True(t, tool.Definition().ReadOnly)
}

func TestRegistry_MustRegisterPanicsOnInvalidTool(t *testing.T) {
	r := NewRegistry()
	assert.Panics(t, func() {
		r.MustRegister(mutatingTool{name: "bad", readOnly: false, perm: core.PermCostRead})
	})
}

func TestRegistry_DefinitionsFiltersByPermission(t *testing.T) {
	r := NewRegistry()
	require.NoError(t, r.Register(mutatingTool{name: "readable", readOnly: true, perm: core.PermCostRead}))
	require.NoError(t, r.Register(mutatingTool{name: "restricted", readOnly: true, perm: core.PermAuditRead}))

	defs := r.Definitions(func(def ports.ToolDefinition) bool { return def.RequiredPermission == core.PermCostRead })
	require.Len(t, defs, 1)
	assert.Equal(t, "readable", defs[0].Name)
}

func TestRegistry_BuiltinToolsAllRegister(t *testing.T) {
	// Every real tool this package ships must itself satisfy the read-only
	// invariant the registry enforces — this is the check that a future tool
	// added to the built-in set without ReadOnly: true fails loudly in CI
	// rather than silently being excluded from the deterministic provider's
	// 16-tool contract.
	assert.NotPanics(t, func() {
		r := BuildRegistry(nil, nil)
		names := r.Names()
		assert.Len(t, names, 16, "expected all 16 tools deterministic/toolmatch.go names to register: %v", names)
	})
}
