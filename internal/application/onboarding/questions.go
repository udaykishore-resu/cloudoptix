package onboarding

import (
	"fmt"
	"strings"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/spec"
)

// maxQuestionBatch bounds how many questions the agent puts in front of the
// user in one reply. Onboarding is a conversation, not a form: asking eight
// things at once reads as an interrogation and produces worse answers than
// asking a few and following up.
const maxQuestionBatch = 3

// dontKnowPhrases are the ways a user says "I have no answer for that".
// Matching is substring-based on lower-cased text, deliberately loose:
// missing one of these phrases costs the user one more turn of being asked
// again, which is a much smaller failure than the agent mistaking a real
// answer for "I don't know" and silently discarding it — so the list stays
// narrow and specific rather than trying to catch every possible phrasing.
var dontKnowPhrases = []string{
	"i don't know", "i dont know", "not sure", "no idea", "unsure",
	"idk", "not certain", "no clue", "not something i know",
}

// isDontKnow reports whether msg is (predominantly) a "no answer" reply
// rather than a substantive message that merely happens to contain doubt
// about something else. It requires the phrase to make up a large share of
// a short message, so "I don't know the exact number but it's around 50k
// requests a month" — which DOES carry an answer — is not swallowed.
func isDontKnow(msg string) bool {
	lower := strings.ToLower(strings.TrimSpace(msg))
	if lower == "" {
		return false
	}
	for _, p := range dontKnowPhrases {
		if strings.Contains(lower, p) {
			// Short messages built almost entirely around the phrase are
			// treated as "no answer"; a long message that happens to
			// contain it is treated as a substantive reply and processed
			// for extraction as normal.
			if len(lower) <= len(p)+20 {
				return true
			}
		}
	}
	return false
}

// registerQuestions ensures every currently-outstanding field in stage has a
// spec.OpenQuestion recorded (AskedCount incremented the first time only —
// this package never re-asks a question it has already put to the user; it
// waits for an answer or an explicit "I don't know"), and returns the batch
// that should appear in this turn's reply: newly-asked questions first, then
// still-pending ones from a previous turn, capped at maxQuestionBatch.
func registerQuestions(draft *spec.Spec, stage string) []spec.OpenQuestion {
	outstanding := outstandingInStage(stage, *draft)

	existing := map[string]int{} // path -> index into draft.OpenQuestions
	for i, q := range draft.OpenQuestions {
		existing[q.Path] = i
	}

	var newlyAsked, stillPending []spec.OpenQuestion
	for _, f := range outstanding {
		if idx, ok := existing[f.Path]; ok {
			stillPending = append(stillPending, draft.OpenQuestions[idx])
			continue
		}
		q := spec.OpenQuestion{
			Path: f.Path, Question: f.Question, Required: f.Blocking,
			Blocking: f.Blocking, AskedCount: 1,
		}
		draft.OpenQuestions = append(draft.OpenQuestions, q)
		newlyAsked = append(newlyAsked, q)
	}

	batch := append(newlyAsked, stillPending...)
	if len(batch) > maxQuestionBatch {
		batch = batch[:maxQuestionBatch]
	}
	return batch
}

// pruneAnsweredQuestions drops OpenQuestions whose field is now known or
// explicitly marked Unknown, so the review packet's open-question list only
// ever shows what is genuinely still outstanding.
func pruneAnsweredQuestions(draft *spec.Spec) {
	if len(draft.OpenQuestions) == 0 {
		return
	}
	out := draft.OpenQuestions[:0]
	for _, q := range draft.OpenQuestions {
		if isPathResolved(*draft, q.Path) {
			continue
		}
		out = append(out, q)
	}
	draft.OpenQuestions = out
}

// isPathResolved reports whether path is either known (per the matching
// stageField, when one exists) or explicitly marked Unknown.
func isPathResolved(draft spec.Spec, path string) bool {
	if draft.Provenance[path] == core.ProvenanceUnknown {
		return true
	}
	for _, fields := range stageFields {
		for _, f := range fields {
			if f.Path == path {
				return f.Known(draft)
			}
		}
	}
	return false
}

// formatQuestionBatch renders a batch of questions as the assistant's next
// message text.
func formatQuestionBatch(batch []spec.OpenQuestion) string {
	if len(batch) == 0 {
		return ""
	}
	if len(batch) == 1 {
		return batch[0].Question
	}
	var b strings.Builder
	b.WriteString("A few things to fill in:\n")
	for _, q := range batch {
		fmt.Fprintf(&b, "- %s\n", q.Question)
	}
	return strings.TrimRight(b.String(), "\n")
}
