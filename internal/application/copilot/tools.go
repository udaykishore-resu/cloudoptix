package copilot

import (
	"context"
	"fmt"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// baseTool is embedded by every tool implementation. It carries the one
// dependency every tool needs to read tenant data — a ports.UnitOfWork — and
// the fixed ToolDefinition it reports. ports.Tool.Invoke's signature carries
// no repository handle of its own, so each tool opens its own short,
// read-only transaction per call via uow.Do; nothing a tool does ever
// writes through it.
type baseTool struct {
	def ports.ToolDefinition
	uow ports.UnitOfWork
}

func (b baseTool) Definition() ports.ToolDefinition { return b.def }

// withRepos runs fn inside a UnitOfWork transaction and returns its result.
// Every tool's Invoke is a thin wrapper around this.
func withRepos[T any](ctx context.Context, uow ports.UnitOfWork, fn func(ctx context.Context, repos ports.Repositories) (T, error)) (T, error) {
	var result T
	err := uow.Do(ctx, func(ctx context.Context, repos ports.Repositories) error {
		r, err := fn(ctx, repos)
		result = r
		return err
	})
	return result, err
}

// toolResult builds the standard {"summary": ..., ...extra} payload every
// tool returns. summary is the plain-English, self-contained sentence the
// deterministic provider's answer composer (and a real model) reads
// directly — every number the copilot ever states traces back to one of
// these strings. extra carries the structured data behind it, for a UI that
// wants to render a table or chart instead of prose.
func toolResult(summary string, extra map[string]any) map[string]any {
	out := map[string]any{"summary": summary}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

func toolError(format string, args ...any) map[string]any {
	return map[string]any{"error": fmt.Sprintf(format, args...)}
}

// argString / argFloat / argInt / argStringSlice read a loosely-typed tool
// argument, tolerating both the deterministic provider's native Go types
// and a real model's JSON-decoded ones (float64 for every number).
func argString(args map[string]any, key, def string) string {
	if v, ok := args[key]; ok {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	return def
}

// firstArgString reads the first present, non-empty key from args. Tool
// JSON schemas name their parameters for a real model's benefit (e.g.
// "resource_id", "recommendation_id"), but the deterministic provider's
// argument builder (deterministic/toolmatch.go) uses shorter, fixed keys
// ("id", "resource_id") independent of any schema. Checking several
// candidate keys lets one Invoke implementation satisfy both callers
// without the deterministic provider needing to know a tool's declared
// schema.
func firstArgString(args map[string]any, keys ...string) string {
	for _, k := range keys {
		if s := argString(args, k, ""); s != "" {
			return s
		}
	}
	return ""
}

func argFloat(args map[string]any, key string, def float64) float64 {
	switch v := args[key].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case string:
		var f float64
		if _, err := fmt.Sscanf(v, "%g", &f); err == nil {
			return f
		}
	}
	return def
}

func argInt(args map[string]any, key string, def int) int {
	return int(argFloat(args, key, float64(def)))
}

// defaultPeriod is the trailing-30-day window every cost-related tool falls
// back to when the caller (model or human) did not specify one — the same
// default a cost dashboard's landing view uses.
func defaultPeriod() core.Period {
	return core.PeriodOfDays(time.Now().UTC(), 30)
}

// periodFromArgs reads an optional "days" tool argument and returns the
// corresponding trailing window, falling back to defaultPeriod when absent
// or non-positive.
func periodFromArgs(args map[string]any) core.Period {
	days := argFloat(args, "days", 30)
	if days <= 0 {
		days = 30
	}
	return core.PeriodOfDays(time.Now().UTC(), int(days))
}

// money formats a core.Money as the copilot's house style: whole dollars for
// anything over $100 (nobody needs cents on a five-figure monthly bill), two
// decimal places below that.
func money(m core.Money) string {
	u := m.Units()
	if u < 0 {
		u = -u
	}
	if u >= 100 {
		return fmt.Sprintf("$%.0f", m.Units())
	}
	return fmt.Sprintf("$%.2f", m.Units())
}

func pct(f float64) string {
	return fmt.Sprintf("%.1f%%", f*100)
}
