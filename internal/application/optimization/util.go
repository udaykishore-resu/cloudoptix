package optimization

import (
	"strconv"
	"strings"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
)

// Small, generic helpers shared across rule files. Nothing here is
// rule-specific; rule-specific decision logic belongs in the rule's own file.

func indexOfFold(list []string, want string) int {
	w := strings.ToLower(strings.TrimSpace(want))
	for i, v := range list {
		if strings.ToLower(strings.TrimSpace(v)) == w {
			return i
		}
	}
	return -1
}

func containsFold(list []string, want string) bool { return indexOfFold(list, want) >= 0 }

func parseFloatAttr(v string, def float64) float64 {
	if v == "" {
		return def
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil {
		return def
	}
	return f
}

func parseIntAttr(v string, def int) int {
	return int(parseFloatAttr(v, float64(def)) + 0.5)
}

// parseDateAttr parses the "2006-01-02" date-only format the discovery
// adapters use for attributes like stopped_at, so a rule doesn't need to
// know that formatting convention itself.
func parseDateAttr(v string) (time.Time, error) {
	return time.Parse("2006-01-02", strings.TrimSpace(v))
}

func daysSince(t time.Time, now time.Time) float64 {
	if t.IsZero() {
		return 0
	}
	return now.Sub(t).Hours() / 24
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

// dbSizeLadder is the standard AWS instance-size suffix ordering, used to
// step an RDS/Aurora instance class down or up one rung. RDS instance
// classes (db.m5.xlarge, db.r6g.2xlarge, ...) follow the same
// family.size naming convention as EC2 but live in a separate price-book
// table with no InstanceFamily-style lookup on ports.PricingCatalog, so this
// package walks the well-known size suffix ordering itself rather than
// asking the catalog for a family list it does not expose for RDS. Every
// candidate this produces is still verified against the catalog's own
// DatabasePrice before being trusted — an unrecognised class.size
// combination is never fabricated.
var dbSizeLadder = []string{
	"micro", "small", "medium", "large", "xlarge",
	"2xlarge", "4xlarge", "8xlarge", "12xlarge", "16xlarge", "24xlarge",
}

// stepDBClass returns the RDS/Aurora instance class one rung away from class
// on the standard size ladder (down when larger is false, up when true), or
// ("", false) when class does not parse or is already at the ladder's edge.
func stepDBClass(class string, larger bool) (string, bool) {
	idx := strings.LastIndex(class, ".")
	if idx <= 0 || idx == len(class)-1 {
		return "", false
	}
	prefix, size := class[:idx], class[idx+1:]
	pos := indexOfFold(dbSizeLadder, size)
	if pos < 0 {
		return "", false
	}
	if larger {
		if pos+1 >= len(dbSizeLadder) {
			return "", false
		}
		return prefix + "." + dbSizeLadder[pos+1], true
	}
	if pos == 0 {
		return "", false
	}
	return prefix + "." + dbSizeLadder[pos-1], true
}

// moneyOrZero converts a lookup's (core.Money, bool) into a plain core.Money,
// used at call sites that already handle the bool separately and just want
// the value.
func moneyOrZero(m core.Money, ok bool) core.Money {
	if !ok {
		return core.ZeroUSD()
	}
	return m
}
