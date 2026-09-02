package rag

import (
	"context"
	"hash/fnv"
	"math"
	"strings"
)

// EmbedDim is the dimensionality of every vector this package produces,
// whether from HashEmbedder or from a provider's real embedding call. A fixed
// dimension is required so cosine similarity between a hash-embedded document
// and a hash-embedded query is always well-defined, and so switching a
// tenant's provider on or off never invalidates the rest of the index.
const EmbedDim = 256

// Embedder turns text into vectors for indexing and query. It is a narrower
// interface than ports.LLMProvider so the store's dependency on a model
// provider is exactly the one method it needs.
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// HashEmbedder is the deterministic, offline embedding used when no provider
// is configured, or when the configured provider's Embed call fails (the
// Anthropic Messages API has no embeddings endpoint at all, for example — see
// internal/adapters/llm/anthropic). It implements feature hashing: every
// unigram and bigram in the (lowercased, punctuation-stripped) text is hashed
// into one of EmbedDim buckets and accumulated with a sign derived from a
// second hash, which is the standard "hashing trick" used to approximate a
// bag-of-words embedding without maintaining a vocabulary. The result is
// L2-normalized so cosine similarity behaves the same way it would for a
// model-produced embedding.
//
// This is not a stand-in for a real embedding model's semantic quality, and
// the package doc explains why that gap is exactly what the BM25 half of the
// hybrid score exists to cover. It is, however, a real, reproducible
// embedding: the same text always hashes to the same vector, across
// processes and across time, which is what makes retrieval usable offline and
// what makes a retrieval test deterministic.
type HashEmbedder struct{ Dim int }

// NewHashEmbedder builds an embedder at the package's standard dimension.
func NewHashEmbedder() HashEmbedder { return HashEmbedder{Dim: EmbedDim} }

// Embed implements Embedder.
func (h HashEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	dim := h.Dim
	if dim <= 0 {
		dim = EmbedDim
	}
	out := make([][]float32, len(texts))
	for i, t := range texts {
		out[i] = hashVector(t, dim)
	}
	return out, nil
}

func hashVector(text string, dim int) []float32 {
	vec := make([]float32, dim)
	toks := tokenize(text)
	features := make([]string, 0, 2*len(toks))
	features = append(features, toks...)
	for i := 0; i+1 < len(toks); i++ {
		features = append(features, toks[i]+"_"+toks[i+1])
	}
	for _, f := range features {
		idx, sign := hashFeature(f, dim)
		vec[idx] += sign
	}
	normalize(vec)
	return vec
}

// hashFeature maps a token to a bucket index and a +1/-1 sign. Two
// independent hash functions (FNV-1a over the raw string, and FNV-1a over the
// string with a salt) are used for the index and the sign so that the sign is
// not trivially correlated with the index, which reduces systematic bias when
// many features collide into the same bucket.
func hashFeature(feature string, dim int) (int, float32) {
	h1 := fnv.New32a()
	_, _ = h1.Write([]byte(feature))
	idx := int(h1.Sum32() % uint32(dim))

	h2 := fnv.New32a()
	_, _ = h2.Write([]byte("sign:" + feature))
	sign := float32(1)
	if h2.Sum32()%2 == 0 {
		sign = -1
	}
	return idx, sign
}

func normalize(vec []float32) {
	var sumSq float64
	for _, v := range vec {
		sumSq += float64(v) * float64(v)
	}
	if sumSq == 0 {
		return
	}
	norm := float32(math.Sqrt(sumSq))
	for i := range vec {
		vec[i] /= norm
	}
}

// tokenize lowercases, strips non-alphanumeric runs to spaces, and splits on
// whitespace. Both the hashing embedder and the BM25 scorer use it, so a
// query and a document are tokenized identically.
func tokenize(text string) []string {
	var b strings.Builder
	b.Grow(len(text))
	for _, r := range strings.ToLower(text) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteRune(' ')
		}
	}
	fields := strings.Fields(b.String())
	out := fields[:0]
	for _, f := range fields {
		if len(f) > 1 || (f != "" && !isStopword(f)) {
			out = append(out, f)
		}
	}
	return out
}

var stopwords = map[string]bool{
	"a": true, "an": true, "the": true, "is": true, "of": true, "to": true,
	"in": true, "and": true, "or": true, "for": true, "on": true, "it": true,
}

func isStopword(w string) bool { return stopwords[w] }

// CosineSimilarity returns the cosine of the angle between two vectors of
// equal length, or 0 for a degenerate (zero-length or mismatched) input.
func CosineSimilarity(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}
