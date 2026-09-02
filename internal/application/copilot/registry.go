package copilot

import (
	"fmt"
	"sort"
	"sync"

	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// Registry holds the copilot's fixed set of read-only tools.
//
// Register is the enforcement point for the package's central invariant:
// it refuses any ports.Tool whose Definition().ReadOnly is not true. A
// caller cannot register a mutating tool by accident, by a future edit
// that forgets to set the flag, or by wiring in a tool built for another
// purpose — the registry simply will not hold it.
type Registry struct {
	mu    sync.RWMutex
	tools map[string]ports.Tool
	order []string
}

// NewRegistry builds an empty Registry.
func NewRegistry() *Registry {
	return &Registry{tools: map[string]ports.Tool{}}
}

// Register adds a tool. It returns an error — rather than panicking — for a
// duplicate name or a tool that does not declare ReadOnly: true, so a
// composition root can decide how to handle a misconfigured tool set
// (refuse to start, log and skip, etc.) instead of the registry deciding
// for it.
func (r *Registry) Register(t ports.Tool) error {
	def := t.Definition()
	if def.Name == "" {
		return fmt.Errorf("copilot: tool has no name")
	}
	if !def.ReadOnly {
		return fmt.Errorf("copilot: tool %q does not declare ReadOnly: true and cannot be registered — "+
			"the copilot registers only read-only tools", def.Name)
	}
	if def.RequiredPermission == "" {
		return fmt.Errorf("copilot: tool %q has no RequiredPermission", def.Name)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.tools[def.Name]; exists {
		return fmt.Errorf("copilot: tool %q is already registered", def.Name)
	}
	r.tools[def.Name] = t
	r.order = append(r.order, def.Name)
	return nil
}

// MustRegister is Register, panicking on error — for use at process start-up
// where a misconfigured built-in tool set is a programming error, not a
// runtime condition to recover from.
func (r *Registry) MustRegister(t ports.Tool) {
	if err := r.Register(t); err != nil {
		panic(err)
	}
}

// Get returns the named tool.
func (r *Registry) Get(name string) (ports.Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

// Definitions returns every registered tool's definition, in registration
// order, restricted to those the given permission set may call — the same
// permission check the copilot's own execution path enforces again before
// invoking a tool, so a definition offered to the model and a tool actually
// callable can never drift apart.
func (r *Registry) Definitions(has func(perm ports.ToolDefinition) bool) []ports.ToolDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]ports.ToolDefinition, 0, len(r.order))
	for _, name := range r.order {
		def := r.tools[name].Definition()
		if has == nil || has(def) {
			out = append(out, def)
		}
	}
	return out
}

// Names returns every registered tool name, sorted, for diagnostics and
// tests.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.tools))
	for name := range r.tools {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
