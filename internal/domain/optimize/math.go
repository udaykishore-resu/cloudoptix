package optimize

import "math"

// powFloat is math.Pow, isolated so the priority formula reads as arithmetic
// rather than as a call graph.
func powFloat(base, exp float64) float64 { return math.Pow(base, exp) }
