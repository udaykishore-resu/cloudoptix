package memstore

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// deepCopy returns an independent copy of v via a JSON round-trip. See the
// package doc for why this technique was chosen over a hand-written clone per
// type: correctness that survives the domain model growing, at the cost of
// CPU this store — backing tests and the demo tenant, not production traffic
// — can spare.
//
// It relies on every stored domain type being exported and json-tagged, which
// holds for everything this package persists. The two domain types with
// unexported fields, cloud.Inventory and cloud.Topology, are never stored
// directly: they are computed views the resource and topology repositories
// build fresh from stored slices on every read, so a JSON round-trip of them
// (which would silently drop the unexported index fields) never happens.
func deepCopy[T any](v T) T {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("memstore: value of type %T is not JSON-serialisable: %v", v, err))
	}
	var out T
	if err := json.Unmarshal(raw, &out); err != nil {
		panic(fmt.Sprintf("memstore: round-trip of type %T failed: %v", v, err))
	}
	return out
}

// cursorPayload is what a Page's NextCursor encodes: the sort key and id of
// the last item returned. Encoding the resume point rather than a numeric
// offset is what makes pagination stable while discovery is concurrently
// inserting rows — an offset-based page silently skips or repeats rows when
// the underlying set changes between requests; a keyset cursor never does,
// which is the same contract the Postgres adapter's keyset pagination gives.
type cursorPayload struct {
	Key string `json:"k"`
	ID  string `json:"i"`
}

func encodeCursor(key, id string) string {
	raw, _ := json.Marshal(cursorPayload{Key: key, ID: id})
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeCursor(s string) (cursorPayload, bool) {
	if s == "" {
		return cursorPayload{}, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return cursorPayload{}, false
	}
	var c cursorPayload
	if err := json.Unmarshal(raw, &c); err != nil {
		return cursorPayload{}, false
	}
	return c, true
}

// paginate applies keyset pagination to a slice that the caller has already
// filtered and sorted according to the ordering keyOf describes. keyOf
// extracts the (sortKey, id) pair used both to build the next cursor and to
// locate the resume point of an incoming one.
//
// If the cursor's item is no longer present (e.g. it was deleted between two
// page requests), pagination stops rather than guessing a resume point: a
// keyset cursor has no well-defined meaning once its anchor is gone, and
// silently resuming from the start would duplicate rows the caller already
// saw.
func paginate[T any](items []T, opts ports.ListOptions, keyOf func(T) (string, string)) ports.Page[T] {
	opts = opts.Normalize()
	start := 0
	if c, ok := decodeCursor(opts.Cursor); ok {
		found := false
		for i, it := range items {
			k, id := keyOf(it)
			if k == c.Key && id == c.ID {
				start = i + 1
				found = true
				break
			}
		}
		if !found {
			start = len(items)
		}
	}
	end := start + opts.Limit
	if end > len(items) {
		end = len(items)
	}
	if start > len(items) {
		start = len(items)
	}
	page := ports.Page[T]{Total: len(items)}
	if start < end {
		page.Items = append(page.Items, items[start:end]...)
	}
	if end < len(items) && len(page.Items) > 0 {
		lastK, lastID := keyOf(page.Items[len(page.Items)-1])
		page.NextCursor = encodeCursor(lastK, lastID)
	}
	return page
}

// sortStrings is a tiny helper used by the several "list distinct keys sorted"
// spots across the aggregation code.
func sortStrings(ss []string) []string {
	sort.Strings(ss)
	return ss
}
