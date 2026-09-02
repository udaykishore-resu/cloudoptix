package rag

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// HybridAlpha weights the blend between the (normalized) cosine similarity
// score and the (normalized) BM25 lexical score in the final hybrid rank.
// 0.5 gives semantic and lexical evidence equal say, which is the right
// default for a corpus that mixes narrative FinOps guidance (where meaning
// matters most) with rule and instance-type references (where an exact token
// match matters most) — see the package doc for why neither signal alone is
// sufficient.
const HybridAlpha = 0.5

// indexedChunk is one unit the store ranks: a document, or one chunk of a
// document too long to embed and score as a single unit.
type indexedChunk struct {
	doc       ports.Document // Content holds this chunk's text, not the parent's
	parentID  string
	chunkIdx  int
	chunkOf   int
	embedding []float32
}

// Store is an in-process, hybrid-search implementation of
// ports.KnowledgeStore. See the package doc for the two design decisions that
// matter: hybrid (not pure vector) ranking, and tenant partitioning enforced
// here rather than by a caller-supplied filter.
type Store struct {
	mu sync.RWMutex

	// platform holds documents with no tenant (TenantID == ""): the shipped
	// corpus, visible to every tenant's queries.
	platform []indexedChunk
	// tenant holds each tenant's own indexed documents: their approved
	// specification, their policy, their past optimization outcomes.
	tenant map[core.TenantID][]indexedChunk

	embedder Embedder
	provider ports.LLMProvider // optional; nil means "always use embedder"

	chunkWords, overlapWords int
	clock                    func() time.Time
}

var _ ports.KnowledgeStore = (*Store)(nil)

// New builds a Store. provider may be nil; when set, its Embed method is
// tried first for every Index and Search call and the store falls back to
// HashEmbedder on any error, so a provider outage degrades retrieval quality
// rather than breaking it.
func New(provider ports.LLMProvider) *Store {
	return &Store{
		tenant:       map[core.TenantID][]indexedChunk{},
		embedder:     NewHashEmbedder(),
		provider:     provider,
		chunkWords:   DefaultChunkWords,
		overlapWords: DefaultOverlapWords,
		clock:        func() time.Time { return time.Now().UTC() },
	}
}

// embed obtains vectors for texts, preferring the configured provider and
// falling back to the deterministic hashing embedder. It is a method (not a
// free function) so tests can swap s.embedder or observe the fallback.
func (s *Store) embed(ctx context.Context, texts []string) [][]float32 {
	if s.provider != nil {
		if vecs, err := s.provider.Embed(ctx, texts); err == nil && len(vecs) == len(texts) {
			return vecs
		}
	}
	vecs, _ := s.embedder.Embed(ctx, texts)
	return vecs
}

// Index chunks and embeds documents, replacing any prior chunks for the same
// document id. A document with TenantID == "" is platform-wide; anything else
// is scoped to that tenant and invisible to every other tenant's Search call.
func (s *Store) Index(ctx context.Context, docs []ports.Document) error {
	if len(docs) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, d := range docs {
		if d.ID == "" {
			return core.Invalid("document id is required")
		}
		if d.UpdatedAt.IsZero() {
			d.UpdatedAt = s.clock()
		}
		chunks := Chunk(d.Content, s.chunkWords, s.overlapWords)
		if len(chunks) == 0 {
			chunks = []string{d.Content}
		}
		texts := make([]string, len(chunks))
		copy(texts, chunks)
		vectors := s.embed(ctx, texts)

		newEntries := make([]indexedChunk, len(chunks))
		for i, text := range chunks {
			chunkDoc := d
			chunkDoc.Content = text
			if len(chunks) > 1 {
				chunkDoc.ID = fmt.Sprintf("%s#%d", d.ID, i)
			}
			var vec []float32
			if i < len(vectors) {
				vec = vectors[i]
			}
			newEntries[i] = indexedChunk{doc: chunkDoc, parentID: d.ID, chunkIdx: i, chunkOf: len(chunks), embedding: vec}
		}

		if d.TenantID.IsZero() {
			s.platform = replaceParent(s.platform, d.ID, newEntries)
		} else {
			s.tenant[d.TenantID] = replaceParent(s.tenant[d.TenantID], d.ID, newEntries)
		}
	}
	return nil
}

func replaceParent(existing []indexedChunk, parentID string, fresh []indexedChunk) []indexedChunk {
	out := existing[:0:0]
	for _, e := range existing {
		if e.parentID != parentID {
			out = append(out, e)
		}
	}
	return append(out, fresh...)
}

// candidateSet returns the chunks a tenant's query is allowed to see: every
// platform document plus that tenant's own, optionally narrowed to a set of
// Document.Source values. This is the tenant-partition boundary described in
// the package doc — it is computed here, inside the store, on every call.
func (s *Store) candidateSet(tenant core.TenantID, sources []string) []indexedChunk {
	var out []indexedChunk
	out = append(out, s.platform...)
	if !tenant.IsZero() {
		out = append(out, s.tenant[tenant]...)
	}
	if len(sources) == 0 {
		return out
	}
	allow := make(map[string]bool, len(sources))
	for _, s := range sources {
		allow[s] = true
	}
	filtered := out[:0]
	for _, c := range out {
		if allow[c.doc.Source] {
			filtered = append(filtered, c)
		}
	}
	return filtered
}

// Search implements ports.KnowledgeStore.Search: hybrid cosine+BM25 ranking
// over the tenant-partitioned candidate set.
func (s *Store) Search(ctx context.Context, tenant core.TenantID, query string, k int, sources []string) ([]ports.RetrievedDocument, error) {
	if k <= 0 {
		k = 5
	}
	s.mu.RLock()
	candidates := s.candidateSet(tenant, sources)
	s.mu.RUnlock()

	if len(candidates) == 0 || strings.TrimSpace(query) == "" {
		return nil, nil
	}

	queryVecs := s.embed(ctx, []string{query})
	var qVec []float32
	if len(queryVecs) == 1 {
		qVec = queryVecs[0]
	}
	queryTerms := tokenize(query)

	texts := make([]string, len(candidates))
	for i, c := range candidates {
		texts[i] = c.doc.Content
	}
	corpus := newBM25Corpus(texts)

	cosScores := make([]float64, len(candidates))
	bm25Scores := make([]float64, len(candidates))
	for i, c := range candidates {
		cosScores[i] = CosineSimilarity(qVec, c.embedding)
		bm25Scores[i] = corpus.Score(i, queryTerms)
	}
	cosNorm := minMaxNormalize(cosScores)
	bm25Norm := minMaxNormalize(bm25Scores)

	type scored struct {
		chunk    indexedChunk
		score    float64
		relevant bool
	}
	results := make([]scored, len(candidates))
	for i, c := range candidates {
		results[i] = scored{
			chunk: c,
			score: HybridAlpha*cosNorm[i] + (1-HybridAlpha)*bm25Norm[i],
			// Min-max normalization collapses to zero for every candidate
			// when the raw scores are all equal — most visibly with exactly
			// one candidate in scope, where "equal to itself" is trivially
			// true. That is a property of the normalization, not evidence of
			// irrelevance, so relevance is judged on the raw, un-normalized
			// scores instead of the blended one.
			relevant: cosScores[i] > 1e-9 || bm25Scores[i] > 1e-9,
		}
	}
	sort.SliceStable(results, func(i, j int) bool { return results[i].score > results[j].score })

	if len(results) > k {
		results = results[:k]
	}
	out := make([]ports.RetrievedDocument, 0, len(results))
	for _, r := range results {
		if !r.relevant {
			continue
		}
		out = append(out, ports.RetrievedDocument{
			Document: r.chunk.doc,
			Score:    r.score,
			Snippet:  snippet(r.chunk.doc.Content, 320),
		})
	}
	return out, nil
}

func snippet(text string, maxLen int) string {
	text = strings.TrimSpace(text)
	if len(text) <= maxLen {
		return text
	}
	cut := text[:maxLen]
	if idx := strings.LastIndexByte(cut, ' '); idx > maxLen/2 {
		cut = cut[:idx]
	}
	return cut + "…"
}

// Delete removes a tenant's documents (and every chunk belonging to them) by
// id. Platform documents cannot be deleted through the tenant-scoped path —
// there is deliberately no way for a tenant call to remove shared knowledge.
func (s *Store) Delete(ctx context.Context, tenant core.TenantID, ids []string) error {
	if tenant.IsZero() {
		return core.Invalid("tenant is required to delete documents")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	remove := make(map[string]bool, len(ids))
	for _, id := range ids {
		remove[id] = true
	}
	existing := s.tenant[tenant]
	kept := existing[:0:0]
	for _, c := range existing {
		if !remove[c.parentID] {
			kept = append(kept, c)
		}
	}
	s.tenant[tenant] = kept
	return nil
}

// Count returns the number of distinct documents (not chunks) visible to a
// tenant: the platform corpus plus that tenant's own documents.
func (s *Store) Count(ctx context.Context, tenant core.TenantID) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	seen := map[string]bool{}
	for _, c := range s.platform {
		seen[c.parentID] = true
	}
	for _, c := range s.tenant[tenant] {
		seen["t:"+c.parentID] = true
	}
	return len(seen), nil
}
