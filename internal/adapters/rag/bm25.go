package rag

import "math"

// bm25K1 and bm25B are the standard Okapi BM25 tuning constants: k1 controls
// how quickly additional occurrences of a term saturate its contribution,
// and b controls how strongly document length is normalized against. These
// are the values Elasticsearch and Lucene ship as defaults, chosen here for
// the same reason: they behave well across prose of varying length without
// per-corpus tuning, and this package's corpus is small enough that tuning
// against it would just be overfitting to six documents.
const (
	bm25K1 = 1.2
	bm25B  = 0.75
)

// bm25Corpus holds the term statistics BM25 needs, computed fresh over
// exactly the candidate set a query is allowed to see (see rank.go). This is
// deliberate: a persistent, shared inverted index would mix document
// frequencies across tenants, so a term that is common in tenant B's
// architecture notes would silently affect how tenant A's query scores
// against the platform corpus. Recomputing per query keeps every score a pure
// function of what that query was actually allowed to search, at a cost that
// is negligible for an in-process index of this size.
type bm25Corpus struct {
	docTokens  [][]string
	docFreq    map[string]int // term -> number of docs containing it
	avgDocLen  float64
	numDocs    int
	termCounts []map[string]int // per-doc term frequency
}

func newBM25Corpus(docs []string) *bm25Corpus {
	c := &bm25Corpus{
		docTokens:  make([][]string, len(docs)),
		docFreq:    map[string]int{},
		termCounts: make([]map[string]int, len(docs)),
		numDocs:    len(docs),
	}
	var totalLen int
	for i, d := range docs {
		toks := tokenize(d)
		c.docTokens[i] = toks
		totalLen += len(toks)
		tf := map[string]int{}
		for _, t := range toks {
			tf[t]++
		}
		c.termCounts[i] = tf
		for t := range tf {
			c.docFreq[t]++
		}
	}
	if c.numDocs > 0 {
		c.avgDocLen = float64(totalLen) / float64(c.numDocs)
	}
	return c
}

// Score computes the BM25 relevance of document i against the (already
// tokenized) query terms.
func (c *bm25Corpus) Score(i int, queryTerms []string) float64 {
	if i < 0 || i >= c.numDocs {
		return 0
	}
	docLen := float64(len(c.docTokens[i]))
	tf := c.termCounts[i]
	var score float64
	for _, term := range queryTerms {
		f := float64(tf[term])
		if f == 0 {
			continue
		}
		df := c.docFreq[term]
		// idf with the +1 inside the log keeps the score non-negative even
		// for a term that appears in every document in a small corpus.
		idf := math.Log(1 + (float64(c.numDocs)-float64(df)+0.5)/(float64(df)+0.5))
		denom := f + bm25K1*(1-bm25B+bm25B*docLen/maxFloat(c.avgDocLen, 1))
		score += idf * (f * (bm25K1 + 1)) / denom
	}
	return score
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

// minMaxNormalize rescales a slice of scores into [0,1], returning all zeros
// when every score is equal (including the degenerate all-zero case). Cosine
// similarity and BM25 live on different, incomparable scales, so hybrid
// ranking blends them only after each has been normalized against the
// current candidate set.
func minMaxNormalize(scores []float64) []float64 {
	out := make([]float64, len(scores))
	if len(scores) == 0 {
		return out
	}
	lo, hi := scores[0], scores[0]
	for _, s := range scores {
		if s < lo {
			lo = s
		}
		if s > hi {
			hi = s
		}
	}
	if hi-lo < 1e-12 {
		return out
	}
	for i, s := range scores {
		out[i] = (s - lo) / (hi - lo)
	}
	return out
}
