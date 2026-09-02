package deterministic

import (
	"encoding/json"
	"strings"

	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// toolResultEntry is one accumulated tool outcome pulled back out of the
// message history for the final-answer composition step.
type toolResultEntry struct {
	name    string
	summary string
	isError bool
}

// collectToolResults reads every Role==RoleTool message out of msgs. By
// convention (documented on internal/application/copilot's agentic loop),
// the copilot appends a tool result as a message whose Name is the tool's
// name and whose Content is the JSON-encoded tool result — the same
// convention this package's Complete relies on to know which tools have
// already answered (see toolmatch.go). A JSON object carrying a "summary"
// string field is read directly; anything else is used verbatim, so a tool
// this package doesn't have special knowledge of still contributes its raw
// result to the composed answer rather than being silently dropped.
func collectToolResults(msgs []ports.Message) []toolResultEntry {
	var out []toolResultEntry
	for _, m := range msgs {
		if m.Role != ports.RoleTool {
			continue
		}
		entry := toolResultEntry{name: m.Name}
		var generic map[string]any
		if err := json.Unmarshal([]byte(unwrapUntrusted(m.Content)), &generic); err == nil {
			if s, ok := generic["summary"].(string); ok && s != "" {
				entry.summary = s
			}
			if e, ok := generic["error"].(string); ok && e != "" {
				entry.isError = true
				entry.summary = e
			}
		}
		if entry.summary == "" {
			entry.summary = unwrapUntrusted(m.Content)
		}
		out = append(out, entry)
	}
	return out
}

func calledToolNames(entries []toolResultEntry) map[string]bool {
	out := make(map[string]bool, len(entries))
	for _, e := range entries {
		out[e.name] = true
	}
	return out
}

// composeAnswer stitches the accumulated tool summaries into a single
// grounded reply. It never invents a figure of its own — every sentence it
// emits is either one of the tool's own summary strings, verbatim, or a
// connective phrase with no numeric content — which is exactly what makes
// this composer's output pass grounding verification: every number in the
// answer traces back to a tool result the caller can point at.
func composeAnswer(question string, entries []toolResultEntry) string {
	if len(entries) == 0 {
		return "I don't have enough grounded data to answer that yet — no tool returned a result for this question."
	}
	var successes []string
	var failures []string
	for _, e := range entries {
		if e.isError {
			failures = append(failures, e.summary)
			continue
		}
		successes = append(successes, e.summary)
	}
	var b strings.Builder
	if len(successes) > 0 {
		b.WriteString(strings.Join(successes, " "))
	}
	if len(failures) > 0 {
		if b.Len() > 0 {
			b.WriteString(" ")
		}
		b.WriteString("Some data could not be retrieved: " + strings.Join(failures, "; ") + ".")
	}
	if b.Len() == 0 {
		return "I looked, but found no grounded data to answer that."
	}
	return b.String()
}

// unwrapUntrusted removes the delimiter internal/adapters/llm/middleware's
// SanitizingProvider wraps every tool result in before it reaches a
// provider, and reverses that layer's angle-bracket escaping.
//
// This provider has to know about that wrapper for a reason worth stating:
// the sanitizer exists so a *prose-reading* model cannot be redirected by
// text that arrived from a tool, and it achieves that by turning the tool's
// JSON into labelled prose. This provider does not read prose — it parses
// the tool result structurally — so without unwrapping, every tool result
// arrives unparseable and the delimiter itself ends up quoted verbatim in
// the answer a user reads. The two ship and are wired together always (the
// fallback provider pairs them by construction), so tolerating the wrapper
// here is the contract between them, not a leak across a boundary.
//
// Content that carries no wrapper is returned unchanged, so this is safe to
// apply unconditionally.
func unwrapUntrusted(content string) string {
	trimmed := strings.TrimSpace(content)
	if !strings.HasPrefix(trimmed, "<untrusted_") {
		return content
	}
	open := strings.IndexByte(trimmed, '>')
	if open < 0 {
		return content
	}
	body := trimmed[open+1:]
	if close := strings.LastIndex(body, "</untrusted_"); close >= 0 {
		body = body[:close]
	}
	// escapeDelimiters replaced < and > with these single-glyph stand-ins;
	// restoring them is what makes the payload valid JSON again.
	body = strings.ReplaceAll(body, "‹", "<")
	body = strings.ReplaceAll(body, "›", ">")
	return strings.TrimSpace(body)
}
