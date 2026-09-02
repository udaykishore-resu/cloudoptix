package memstore

import "time"

// timeNowUTC is the store's one time source. Centralising it here (rather
// than calling time.Now().UTC() at each call site) is what would let a future
// test inject a core.Clock if the need arose; today it is a thin wrapper kept
// for that single point of change.
func timeNowUTC() time.Time { return time.Now().UTC() }
