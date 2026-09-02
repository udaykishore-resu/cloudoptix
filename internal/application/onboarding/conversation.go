package onboarding

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/spec"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// isPreConnectionIssue marks the two spec.Validate checks that are, by
// design, unknowable until AFTER a tenant exists and the customer has
// followed the AWS onboarding instructions Approve itself returns: an IAM
// role ARN and a per-account external id. Requiring them before Approve
// would be a chicken-and-egg problem — the role does not exist yet, and its
// ARN is exactly what the customer creates using the PolicyDocuments this
// call is about to hand back. spec.Validate() still reports these two
// checks (it is shared with SpecService, where an account IS expected to
// already be connected — e.g. a later specification revision), so
// onboarding filters them out specifically at the approval gate, rather
// than relaxing the shared domain check for every caller of Validate.
func isPreConnectionIssue(iss core.ValidationIssue) bool {
	return strings.HasPrefix(iss.Path, "aws.accounts") &&
		(strings.HasSuffix(iss.Path, ".roleArn") || strings.HasSuffix(iss.Path, ".externalId"))
}

// filterForApproval drops the pre-connection account issues from a
// validation result, for the copy of Validation that gets stored on the
// approved spec.Version — spec.Version.Approve itself refuses over any
// v.Validation.HasBlocking(), so the stored validation must already agree
// with onboarding's own approvalBlocking decision, or the two would
// disagree at the exact moment it matters most.
func filterForApproval(v core.ValidationResult) core.ValidationResult {
	var out core.ValidationResult
	for _, iss := range v.Issues {
		if isPreConnectionIssue(iss) {
			continue
		}
		out.Issues = append(out.Issues, iss)
	}
	return out
}

// approvalBlocking reports whether validation contains an issue onboarding
// approval must refuse over, excluding the pre-connection account issues
// described above.
func approvalBlocking(v core.ValidationResult) bool {
	return filterForApproval(v).HasBlocking()
}

// extractionSystemPrompt frames the structured-output call every turn makes.
// It is deliberately short: the schema itself, not the prompt, is what
// constrains the answer shape.
const extractionSystemPrompt = "You are CloudOptix's onboarding assistant. Extract every fact the user has " +
	"stated about their company, application, AWS estate, business and objectives into the given schema. " +
	"Only fill a property when the user actually said something that supports it; never guess."

// currentStage derives the conversation's stage purely from what the draft
// currently knows, rather than storing it as separate mutable state — a
// message that answers a later stage's question does not need an explicit
// stage transition, because the very next call to currentStage sees the
// newly-filled field and reports the stage that comes after it.
func currentStage(draft spec.Spec) string {
	for _, st := range stageOrder {
		if st == StageReview {
			continue
		}
		if !stageComplete(st, draft) {
			return st
		}
	}
	return StageReview
}

// processTurn is the shared interpreter behind both Start (for its optional
// initial message) and Send: it classifies the message, applies it to
// draft, and returns the assistant's reply plus whether this turn ran
// without the model (degraded).
func (s *Service) processTurn(ctx context.Context, draft *spec.Spec, history []ports.Message, message string) (string, bool) {
	stageBefore := currentStage(*draft)

	if isDontKnow(message) {
		paths := markUnknown(draft, stageBefore)
		pruneAnsweredQuestions(draft)
		stage := currentStage(*draft)
		batch := registerQuestions(draft, stage)

		var b strings.Builder
		if len(paths) > 0 {
			fmt.Fprintf(&b, "No problem — I'll leave %s unanswered for now.", strings.Join(labelsFor(paths), ", "))
		} else {
			b.WriteString("No problem.")
		}
		if q := formatQuestionBatch(batch); q != "" {
			b.WriteString("\n\n")
			b.WriteString(q)
		} else if stage == StageReview {
			b.WriteString("\n\n")
			b.WriteString(reviewReadyMessage(*draft))
		}
		return b.String(), false
	}

	if isShowSummaryRequest(message) {
		return renderKnowledgeSummary(*draft), false
	}

	// Extraction runs first, over the FULL cumulative conversation — but a
	// direct edit is applied AFTER it and wins any conflict. Extraction
	// re-derives every field from scratch each turn (including fields an
	// earlier turn already stated), which is what lets an answer stated
	// out of order still be captured; the cost is that an earlier
	// statement still sitting in the transcript ("availability target is
	// 99.9%") would otherwise silently out-vote a direct correction issued
	// later ("change production SLO to 99.99%") if the edit were applied
	// first. Applying the edit last makes the explicit, deliberate
	// instruction the final word for this turn, exactly like a review-
	// screen edit already is via ApplyEdit.
	allMessages := append(append([]ports.Message{}, history...), ports.Message{Role: ports.RoleUser, Content: message})
	structured, degraded := s.extract(ctx, allMessages)
	applyExtraction(draft, structured)

	var editNote string
	if change, ok := applyDirectEdit(draft, message); ok {
		editNote = fmt.Sprintf("Updated %s: %s -> %s.\n\n", pathLabels[change.Path], change.Before, change.After)
	}

	runInference(draft)
	pruneAnsweredQuestions(draft)

	stageAfter := currentStage(*draft)
	batch := registerQuestions(draft, stageAfter)

	var b strings.Builder
	b.WriteString(editNote)
	if degraded {
		b.WriteString("(I'm having trouble reaching the assistant model right now, so I'm working from pattern matching alone — I may miss things a full answer would catch.)\n\n")
	}
	if ack := acknowledgeCapture(structured); ack != "" {
		b.WriteString(ack)
		b.WriteString("\n\n")
	}
	if q := formatQuestionBatch(batch); q != "" {
		b.WriteString(q)
	} else if stageAfter == StageReview {
		b.WriteString(reviewReadyMessage(*draft))
	}
	return strings.TrimSpace(b.String()), degraded
}

// extract calls the configured provider for structured extraction,
// degrading gracefully (an empty result, degraded=true) rather than
// erroring when the provider is unavailable or the call itself fails. This
// package never reaches for a concrete provider implementation to degrade
// with — see the Service doc comment on why that would violate the
// hexagonal dependency rule — so a genuinely unavailable model means this
// turn simply re-shows whatever is already known and still outstanding,
// which keeps the conversation usable rather than broken.
func (s *Service) extract(ctx context.Context, messages []ports.Message) (map[string]any, bool) {
	if s.provider == nil || !s.provider.Healthy(ctx) {
		return nil, true
	}
	resp, err := s.provider.Complete(ctx, ports.CompletionRequest{
		Purpose: "onboarding_extraction", System: extractionSystemPrompt,
		Messages: messages, ResponseSchema: fullSchema(), MaxTokens: 1536,
	})
	if err != nil {
		return nil, true
	}
	return resp.Structured, false
}

// acknowledgeCapture names, briefly, what this turn's extraction actually
// found — the small courtesy that makes a batch extraction feel like it was
// heard rather than silently swallowed.
func acknowledgeCapture(structured map[string]any) string {
	if len(structured) == 0 {
		return ""
	}
	names := make([]string, 0, len(structured))
	for k := range structured {
		if f, ok := fieldByName[k]; ok {
			names = append(names, f.Name)
		}
	}
	if len(names) == 0 {
		return ""
	}
	sort.Strings(names)
	labels := make([]string, 0, len(names))
	for _, n := range names {
		labels = append(labels, strings.ReplaceAll(n, "_", " "))
	}
	if len(labels) > 4 {
		labels = append(labels[:4], fmt.Sprintf("and %d more", len(labels)-4))
	}
	return "Got it — noted " + strings.Join(labels, ", ") + "."
}

func labelsFor(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if l, ok := pathLabels[p]; ok {
			out = append(out, l)
		} else {
			out = append(out, p)
		}
	}
	return out
}

// renderKnowledgeSummary answers "show me what you know": a plain-language
// recap of everything CONFIRMED, INFERRED and still UNKNOWN.
func renderKnowledgeSummary(draft spec.Spec) string {
	collected, inferred, unknown, _ := buildFieldStates(draft)
	var b strings.Builder
	b.WriteString("Here's what I have so far:\n")
	if len(collected) > 0 {
		b.WriteString("\nConfirmed:\n")
		for _, f := range collected {
			fmt.Fprintf(&b, "- %s: %s\n", f.Label, f.Value)
		}
	}
	if len(inferred) > 0 {
		b.WriteString("\nInferred (let me know if any of these are wrong):\n")
		for _, f := range inferred {
			fmt.Fprintf(&b, "- %s: %s (%s)\n", f.Label, f.Value, f.Rationale)
		}
	}
	if len(unknown) > 0 {
		b.WriteString("\nStill unknown:\n")
		for _, f := range unknown {
			fmt.Fprintf(&b, "- %s\n", f.Label)
		}
	}
	if len(collected)+len(inferred) == 0 {
		b.WriteString("\nNothing yet — tell me about your company and application to get started.")
	}
	return strings.TrimRight(b.String(), "\n")
}

// reviewReadyMessage is shown once every blocking question has been
// resolved (answered or explicitly marked unknown).
func reviewReadyMessage(draft spec.Spec) string {
	c := draft.AssessCompleteness()
	if !c.ReadyForReview {
		return "A few required things are still missing: " + strings.Join(c.BlockingGaps, ", ") + "."
	}
	return "That's everything CloudOptix needs to get started. Say \"show me what you know\" for a full recap, or approve when you're ready."
}
