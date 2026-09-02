package copilot

import (
	"regexp"
	"strings"
)

// injectionMarkerRe recognizes the same narrow set of phrases
// internal/adapters/llm/middleware's SanitizingProvider neutralises in any
// tool-result message before it reaches a real model. This package
// duplicates rather than imports that logic: internal/application may not
// import internal/adapters in non-test code (the layering rule
// internal/ports/repositories.go documents), and the composition root is
// what decides whether a given ports.LLMProvider passed to this package is
// middleware-wrapped at all. A RAG-retrieved document is the one input to
// the copilot that comes from outside CloudOptix's own data (a tenant's own
// uploaded policy document, or corpus text), so it is neutralised here too,
// as defense in depth rather than as a substitute for the middleware layer.
var injectionMarkerRe = regexp.MustCompile(`(?i)(ignore (?:all |any )?(?:previous|prior|above) instructions|` +
	`disregard (?:all |any )?(?:previous|prior|above) instructions|` +
	`you are now|new system prompt|system\s*:\s*|assistant\s*:\s*)`)

// sanitizeRetrievedText neutralises embedded-instruction phrases in text
// retrieved from the knowledge store before it is folded into a tool
// result. It does not remove or reject anything — a knowledge document
// legitimately discussing "ignore previous instructions" as an example of a
// prompt-injection risk should still be readable — it only defuses the
// specific phrase so a model reading it cannot act on it as a directive.
func sanitizeRetrievedText(s string) string {
	return injectionMarkerRe.ReplaceAllStringFunc(s, func(m string) string {
		return "[neutralised:" + strings.TrimSpace(strings.ToLower(m)) + "]"
	})
}
