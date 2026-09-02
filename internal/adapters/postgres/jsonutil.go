package postgres

import (
	"encoding/json"
	"fmt"
)

// toJSON marshals v for a jsonb column parameter. Marshalling failure here
// means a domain value contains something json.Marshal fundamentally cannot
// encode (a channel, a func) — a programming error, not a runtime
// condition callers should be asked to handle — so it panics rather than
// threading an error through every single call site.
func toJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("postgres: marshal %T: %v", v, err))
	}
	return b
}

// fromJSON unmarshals a jsonb column's bytes into dst. Unlike toJSON, this
// direction returns an error: the bytes come from the database, and a
// row written by an older code version, or corrupted, is a runtime
// condition every caller must be able to report rather than crash on.
func fromJSON(raw []byte, dst any) error {
	if len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, dst)
}
