package rag

import "strings"

// DefaultChunkWords and DefaultOverlapWords size the chunks documents are
// split into before indexing. Word counts rather than byte counts are used
// because the corpus is prose: a word-bounded chunk never splits mid-token,
// which matters for both the lexical scorer (bm25.go) and for producing a
// readable citation snippet.
const (
	DefaultChunkWords   = 180
	DefaultOverlapWords = 40
)

// Chunk splits text into overlapping word-bounded segments.
//
// Overlap exists so a fact stated near a chunk boundary is not orphaned: the
// sentence "gp3 is priced independently of IOPS" sitting at word 179 of a
// 180-word chunk boundary would otherwise be truncated out of both
// neighbouring chunks. Re-including the trailing overlapWords of one chunk at
// the head of the next means any single sentence in the source document is
// guaranteed to appear whole inside at least one chunk, at the cost of
// indexing that overlap twice.
func Chunk(text string, chunkWords, overlapWords int) []string {
	if chunkWords <= 0 {
		chunkWords = DefaultChunkWords
	}
	if overlapWords < 0 || overlapWords >= chunkWords {
		overlapWords = DefaultOverlapWords
	}
	words := strings.Fields(text)
	if len(words) == 0 {
		return nil
	}
	if len(words) <= chunkWords {
		return []string{strings.Join(words, " ")}
	}

	step := chunkWords - overlapWords
	var chunks []string
	for start := 0; start < len(words); start += step {
		end := start + chunkWords
		if end > len(words) {
			end = len(words)
		}
		chunks = append(chunks, strings.Join(words[start:end], " "))
		if end == len(words) {
			break
		}
	}
	return chunks
}
