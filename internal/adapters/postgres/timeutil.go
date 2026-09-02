package postgres

import "time"

// zeroToNil turns a zero time.Time into a nil parameter, so an unset
// optional timestamp (core.Period{} defaults, "never connected",
// "never verified") is stored as SQL NULL rather than the year-1
// sentinel Go's zero time would otherwise write — NULL is what every
// nullable TIMESTAMPTZ column in these migrations expects, and what lets a
// query like `WHERE connected_at IS NULL` mean what it says.
func zeroToNil(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

// nilToZero is zeroToNil's read-side inverse: a NULL column scanned into a
// *time.Time comes back nil, which this turns back into the domain's zero
// time.Time rather than leaving call sites to nil-check a pointer that the
// domain type was never written to hold.
func nilToZero(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}
