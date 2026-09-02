package http

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// ParseListOptions reads the platform's standard cursor-pagination and
// sorting query parameters (limit, cursor, sort, order) into a
// ports.ListOptions. ports.ListOptions.Normalize() (called by every
// application service already) applies the actual default/cap — this
// function only parses what the client sent, so a missing or malformed
// limit falls back to the service's own default rather than this package
// inventing a second one that could drift from it.
func ParseListOptions(r *http.Request) ports.ListOptions {
	q := r.URL.Query()
	opts := ports.ListOptions{
		Cursor: q.Get("cursor"),
		SortBy: q.Get("sort"),
	}
	if limit, err := strconv.Atoi(q.Get("limit")); err == nil {
		opts.Limit = limit
	}
	if strings.EqualFold(q.Get("order"), "desc") {
		opts.Desc = true
	}
	return opts
}

// ParsePeriod reads a "from"/"to" pair of RFC 3339 timestamps into a
// core.Period, defaulting to the trailing 30 days when either is absent —
// every cost and economics endpoint that accepts a period uses this so
// "no period specified" means the same window everywhere in the API rather
// than each handler picking its own default.
func ParsePeriod(r *http.Request) (core.Period, error) {
	q := r.URL.Query()
	now := time.Now().UTC()
	from, to := now.AddDate(0, 0, -30), now

	if v := q.Get("from"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return core.Period{}, core.Invalid("from: %q is not a valid RFC3339 timestamp", v)
		}
		from = t
	}
	if v := q.Get("to"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return core.Period{}, core.Invalid("to: %q is not a valid RFC3339 timestamp", v)
		}
		to = t
	}
	if to.Before(from) {
		return core.Period{}, core.Invalid("to (%s) must not be before from (%s)", to, from)
	}
	return core.Period{Start: from, End: to}, nil
}

// QueryRegions/QueryAccountIDs/QueryEnvironments parse a repeated or
// comma-separated query parameter into the corresponding typed slice — every
// list-filtering endpoint (resources, costs, recommendations, the twin
// graph) accepts these the same way, so a client learns the convention once.
func QueryRegions(r *http.Request, key string) []core.Region {
	vals := queryList(r, key)
	out := make([]core.Region, 0, len(vals))
	for _, v := range vals {
		out = append(out, core.Region(v))
	}
	return out
}

func QueryAccountIDs(r *http.Request, key string) []core.AccountID {
	vals := queryList(r, key)
	out := make([]core.AccountID, 0, len(vals))
	for _, v := range vals {
		out = append(out, core.AccountID(v))
	}
	return out
}

func QueryEnvironments(r *http.Request, key string) []core.Environment {
	vals := queryList(r, key)
	out := make([]core.Environment, 0, len(vals))
	for _, v := range vals {
		out = append(out, core.NormalizeEnvironment(v))
	}
	return out
}

func QueryIDs(r *http.Request, key string) []core.ID {
	vals := queryList(r, key)
	out := make([]core.ID, 0, len(vals))
	for _, v := range vals {
		out = append(out, core.ID(v))
	}
	return out
}

// queryList accepts both repeated params (?region=a&region=b) and one
// comma-separated param (?region=a,b) — clients differ on which is more
// natural to construct, and accepting both costs nothing.
func queryList(r *http.Request, key string) []string {
	raw := r.URL.Query()[key]
	var out []string
	for _, v := range raw {
		for _, part := range strings.Split(v, ",") {
			if part = strings.TrimSpace(part); part != "" {
				out = append(out, part)
			}
		}
	}
	return out
}

// QueryMoney parses a bare-decimal USD query parameter (e.g.
// "min_monthly_cost=500") into core.Money, defaulting to zero when absent —
// used by the resource and recommendation filters' minimum-cost thresholds.
func QueryMoney(r *http.Request, key string) core.Money {
	v := r.URL.Query().Get(key)
	if v == "" {
		return core.ZeroUSD()
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return core.ZeroUSD()
	}
	return core.USDollars(f)
}

// QueryBool parses a boolean query parameter, defaulting to false.
func QueryBool(r *http.Request, key string) bool {
	v, _ := strconv.ParseBool(r.URL.Query().Get(key))
	return v
}

// QueryInt parses an integer query parameter, returning def when absent or
// malformed.
func QueryInt(r *http.Request, key string, def int) int {
	v, err := strconv.Atoi(r.URL.Query().Get(key))
	if err != nil {
		return def
	}
	return v
}

// QueryFloat parses a float64 query parameter, defaulting to zero when
// absent or malformed — used by threshold filters such as
// min_confidence where "not specified" and "zero" mean the same thing (no
// filtering).
func QueryFloat(r *http.Request, key string) float64 {
	v, err := strconv.ParseFloat(r.URL.Query().Get(key), 64)
	if err != nil {
		return 0
	}
	return v
}
