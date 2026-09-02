// Package rag implements ports.KnowledgeStore: an in-process, hybrid-search
// vector index that grounds the onboarding agent and the AI Cost Copilot in
// real documents instead of the model's own training data.
//
// # Why hybrid search, not pure vector search
//
// Cosine similarity over embeddings is excellent at matching meaning — "why
// did my bill go up" retrieves a document about cost anomalies even though it
// shares no words with the query. It is weak at exactly the queries a cost
// platform gets asked most: "what does m5.2xlarge cost", "explain
// ec2-underutilized-rightsize", "what is gp3". An embedding model compresses
// an exact identifier into the same few hundred dimensions as everything else
// in the sentence around it, so a document that merely discusses instance
// families in general can out-score the one document that actually names
// m5.2xlarge. A lexical score restores exact-term precision: it rewards a
// document for containing the literal token the user typed, at full strength,
// regardless of what else is embedded near it. This store therefore blends a
// cosine similarity score with a BM25-style lexical score into one hybrid
// rank (see rank.go), rather than picking one.
//
// # Why tenant partitioning lives in the store
//
// ports.KnowledgeStore.Search takes no tenant filter parameter to forget —
// tenant scoping is structural, computed inside Search from the store's own
// document index, exactly like core.GuardTenant is structural in every
// memstore repository. A caller cannot leak tenant B's approved specification
// into tenant A's copilot answer by omitting a filter, because there is no
// filter to omit: every query is intersected with (platform-wide documents)
// UNION (the querying tenant's own documents) before ranking ever runs.
//
// # Why a deterministic hashing embedder ships by default
//
// Retrieval must work with no API key, in CI, and in the demo tenant, exactly
// like the deterministic LLM provider (see internal/adapters/llm/deterministic).
// HashEmbedder (embed.go) is not a mock: it is a real, stable feature-hashing
// embedding — token and bigram hashing into a fixed-width vector, L2
// normalized — that produces genuinely useful cosine similarity for the
// corpus this package ships, with the property that the same text always
// produces the same vector. When a real ports.LLMProvider is supplied and its
// Embed method succeeds, the store uses it instead; HashEmbedder is the
// documented, always-available fallback, not a placeholder for one.
//
// # Governing principle
//
// Nothing in this package writes, ranks or filters anything but text
// documents and their relevance scores. It has no method that could execute
// infrastructure, approve anything, or assert a fact — it returns candidate
// passages and a score, and the caller (the onboarding agent or the copilot)
// remains responsible for grounding any claim it eventually makes against the
// tenant's actual structured data. This keeps the platform AI-assisted, not
// AI-controlled, one layer further down than the LLM boundary itself.
//
// Traceability: REQ-AI-010..013, SPEC-AI-005 (retrieval), SPEC-SEC-003
// (tenant isolation).
package rag
