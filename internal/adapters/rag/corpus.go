package rag

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"strconv"
	"strings"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

//go:embed corpus/*.md
var corpusFS embed.FS

// frontMatter is the minimal "---\nkey: value\n---" header each corpus
// document carries. It is parsed rather than hard-coded per file so a new
// markdown file dropped into corpus/ is picked up by LoadCorpus without a
// code change.
func parseFrontMatter(raw string) (meta map[string]string, body string) {
	meta = map[string]string{}
	lines := strings.Split(raw, "\n")
	if len(lines) < 2 || strings.TrimSpace(lines[0]) != "---" {
		return meta, raw
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
	}
	if end == -1 {
		return meta, raw
	}
	for _, l := range lines[1:end] {
		if idx := strings.IndexByte(l, ':'); idx > 0 {
			key := strings.TrimSpace(l[:idx])
			val := strings.TrimSpace(l[idx+1:])
			meta[key] = val
		}
	}
	return meta, strings.Join(lines[end+1:], "\n")
}

// LoadCorpus parses the embedded platform knowledge base — AWS pricing
// mechanics, right-sizing and commitment guidance, FinOps Foundation
// principles, the Well-Architected cost pillar, CloudOptix's own rule
// catalog, and safe-change practice — into platform-wide Documents
// (TenantID == "").
func LoadCorpus() ([]ports.Document, error) {
	entries, err := fs.ReadDir(corpusFS, "corpus")
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	docs := make([]ports.Document, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		raw, err := fs.ReadFile(corpusFS, "corpus/"+e.Name())
		if err != nil {
			return nil, err
		}
		meta, body := parseFrontMatter(string(raw))
		title := meta["title"]
		if title == "" {
			title = strings.TrimSuffix(e.Name(), ".md")
		}
		source := meta["source"]
		if source == "" {
			source = "cloudoptix_rules"
		}
		docs = append(docs, ports.Document{
			ID:      "corpus:" + strings.TrimSuffix(e.Name(), ".md"),
			Source:  source,
			Title:   title,
			Content: strings.TrimSpace(body),
			Metadata: map[string]string{
				"file": e.Name(),
			},
			UpdatedAt: now,
		})
	}
	return docs, nil
}

// SeedPlatformCorpus loads and indexes the embedded knowledge base into
// store. Call it once at process start (or once per test); indexing is
// idempotent because Store.Index replaces a document's prior chunks by id.
func SeedPlatformCorpus(ctx context.Context, store ports.KnowledgeStore) error {
	docs, err := LoadCorpus()
	if err != nil {
		return err
	}
	return store.Index(ctx, docs)
}

// TenantKnowledge is the tenant-specific material a copilot answer can cite
// alongside the platform corpus: the organisation's own approved
// specification, its active governance policy, and the outcomes of
// optimizations it has actually run. Indexing these — rather than only ever
// answering from platform-wide guidance — is what lets the copilot say "your
// policy already requires approval for this" or "the last time this rule
// fired on your estate, it saved $340/mo as predicted" instead of only
// generic advice.
type TenantKnowledge struct {
	SpecYAML       string
	SpecVersion    int
	PolicyName     string
	PolicySummary  string
	OutcomeEntries []OutcomeEntry
}

// OutcomeEntry is one past optimization outcome rendered to prose for
// indexing. Callers build this from execute.Outcome rather than this package
// depending on the execute package directly, which keeps rag's dependency
// surface to core, ports and the standard library only.
type OutcomeEntry struct {
	RuleID               string
	Summary              string
	PredictedUSDPerMonth float64
	ActualUSDPerMonth    float64
	Accurate             bool
}

// IndexTenantKnowledge renders a tenant's own decisions into Documents and
// indexes them under that tenant's partition, invisible to every other
// tenant. This is the loader the onboarding Approve flow and the discovery
// pipeline call whenever the spec, policy or savings history changes.
func IndexTenantKnowledge(ctx context.Context, store ports.KnowledgeStore, tenant core.TenantID, tk TenantKnowledge) error {
	if tenant.IsZero() {
		return core.Invalid("tenant is required to index tenant knowledge")
	}
	now := time.Now().UTC()
	var docs []ports.Document

	if strings.TrimSpace(tk.SpecYAML) != "" {
		docs = append(docs, ports.Document{
			ID:        "tenant_spec",
			TenantID:  tenant,
			Source:    "tenant_spec",
			Title:     "Approved specification (cloudoptix.yaml)",
			Content:   tk.SpecYAML,
			Metadata:  map[string]string{"version": strconv.Itoa(tk.SpecVersion)},
			UpdatedAt: now,
		})
	}
	if strings.TrimSpace(tk.PolicySummary) != "" {
		title := "Active governance policy"
		if tk.PolicyName != "" {
			title = "Active governance policy: " + tk.PolicyName
		}
		docs = append(docs, ports.Document{
			ID: "tenant_policy", TenantID: tenant, Source: "tenant_policy",
			Title: title, Content: tk.PolicySummary, UpdatedAt: now,
		})
	}
	if len(tk.OutcomeEntries) > 0 {
		var b strings.Builder
		b.WriteString("Past optimization outcomes for this tenant, most recent first:\n\n")
		for _, o := range tk.OutcomeEntries {
			accuracy := "matched the prediction"
			if !o.Accurate {
				accuracy = "diverged from the prediction"
			}
			fmt.Fprintf(&b, "- rule %s: %s (predicted $%.2f/mo, actual $%.2f/mo, %s)\n",
				o.RuleID, o.Summary, o.PredictedUSDPerMonth, o.ActualUSDPerMonth, accuracy)
		}
		docs = append(docs, ports.Document{
			ID: "tenant_outcomes", TenantID: tenant, Source: "tenant_outcomes",
			Title: "Past optimization outcomes", Content: b.String(), UpdatedAt: now,
		})
	}
	if len(docs) == 0 {
		return nil
	}
	return store.Index(ctx, docs)
}
