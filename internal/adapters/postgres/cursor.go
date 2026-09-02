package postgres

import (
	"encoding/base64"
	"encoding/json"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
)

// cursor is the decoded form of a ports.ListOptions.Cursor: the sort-key
// values of the last row the caller saw. Every List method in this package
// paginates by keyset — a WHERE clause comparing these values against the
// row's own sort columns — never by OFFSET. OFFSET pagination re-scans and
// discards every prior page on each request, which on resources (hundreds
// of thousands of rows per tenant) is the difference between a page-10
// request costing the same as page-1 and costing ten times as much; worse,
// a discovery run upserting concurrently can shift OFFSET-based pages so a
// row is skipped or duplicated mid-scroll, which keyset pagination cannot
// do because it anchors on a value, not a position.
type cursor struct {
	Values []string `json:"v"`
}

// encodeCursor packs a row's sort-key values into the opaque token handed
// back as Page.NextCursor. Values are passed in the same order the ORDER BY
// clause lists them, so decodeCursor's caller can zip them back onto the
// same columns without guessing.
func encodeCursor(values ...string) string {
	raw, _ := json.Marshal(cursor{Values: values}) // Marshal of []string cannot fail
	return base64.RawURLEncoding.EncodeToString(raw)
}

// decodeCursor is encodeCursor's inverse. An empty string decodes to a nil,
// zero-length cursor (the first page); anything else that fails to decode
// is a malformed or tampered token, reported as core.ErrInvalidInput rather
// than treated as "no cursor" — silently restarting from page one on a bad
// token would look like data loss to a client paging through a large list.
func decodeCursor(s string) ([]string, error) {
	if s == "" {
		return nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, core.Invalid("malformed pagination cursor")
	}
	var c cursor
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, core.Invalid("malformed pagination cursor")
	}
	return c.Values, nil
}

// expectCursor decodes and validates that the cursor carries exactly n
// values, which is what every keyset WHERE clause in this package assumes
// before it indexes into the slice.
func expectCursor(s string, n int) ([]string, error) {
	vals, err := decodeCursor(s)
	if err != nil {
		return nil, err
	}
	if vals == nil {
		return nil, nil
	}
	if len(vals) != n {
		return nil, core.Invalid("malformed pagination cursor: expected %d fields, got %d", n, len(vals))
	}
	return vals, nil
}
