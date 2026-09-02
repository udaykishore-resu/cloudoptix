package http

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestParseListOptions(t *testing.T) {
	t.Run("defaults are left for the service to apply", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/x", nil)
		opts := ParseListOptions(r)
		require.Equal(t, 0, opts.Limit) // not this package's job to default it — see doc comment
		require.Empty(t, opts.Cursor)
		require.False(t, opts.Desc)
	})

	t.Run("parses cursor, sort, limit and order", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/x?cursor=abc123&sort=created_at&limit=25&order=desc", nil)
		opts := ParseListOptions(r)
		require.Equal(t, "abc123", opts.Cursor)
		require.Equal(t, "created_at", opts.SortBy)
		require.Equal(t, 25, opts.Limit)
		require.True(t, opts.Desc)
	})

	t.Run("order is case-insensitive", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/x?order=DESC", nil)
		require.True(t, ParseListOptions(r).Desc)
	})
}

func TestParsePeriod(t *testing.T) {
	t.Run("defaults to trailing 30 days", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/x", nil)
		p, err := ParsePeriod(r)
		require.NoError(t, err)
		require.WithinDuration(t, p.End, time.Now().UTC(), time.Minute)
		require.WithinDuration(t, p.Start, time.Now().UTC().AddDate(0, 0, -30), time.Minute)
	})

	t.Run("parses explicit from/to", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/x?from=2026-01-01T00:00:00Z&to=2026-01-31T00:00:00Z", nil)
		p, err := ParsePeriod(r)
		require.NoError(t, err)
		require.Equal(t, "2026-01-01T00:00:00Z", p.Start.Format(time.RFC3339))
		require.Equal(t, "2026-01-31T00:00:00Z", p.End.Format(time.RFC3339))
	})

	t.Run("rejects malformed timestamp", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/x?from=not-a-date", nil)
		_, err := ParsePeriod(r)
		require.Error(t, err)
	})

	t.Run("rejects to before from", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/x?from=2026-02-01T00:00:00Z&to=2026-01-01T00:00:00Z", nil)
		_, err := ParsePeriod(r)
		require.Error(t, err)
	})
}

func TestQueryList_AcceptsRepeatedAndCommaSeparated(t *testing.T) {
	r := httptest.NewRequest("GET", "/x?region=us-east-1&region=us-west-2,eu-west-1", nil)
	got := queryList(r, "region")
	require.ElementsMatch(t, []string{"us-east-1", "us-west-2", "eu-west-1"}, got)
}

func TestQueryList_TrimsAndDropsEmpty(t *testing.T) {
	r := httptest.NewRequest("GET", "/x?region=%20us-east-1%20,,us-west-2", nil)
	got := queryList(r, "region")
	require.ElementsMatch(t, []string{"us-east-1", "us-west-2"}, got)
}

func TestQueryRegionsAccountIDsEnvironmentsIDs(t *testing.T) {
	r := httptest.NewRequest("GET", "/x?region=us-east-1&account_id=111111111111&environment=Production&resource_id=res_1", nil)
	require.Equal(t, "us-east-1", string(QueryRegions(r, "region")[0]))
	require.Equal(t, "111111111111", string(QueryAccountIDs(r, "account_id")[0]))
	require.Equal(t, "production", string(QueryEnvironments(r, "environment")[0])) // normalized
	require.Equal(t, "res_1", string(QueryIDs(r, "resource_id")[0]))
}

func TestQueryMoney(t *testing.T) {
	t.Run("absent defaults to zero", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/x", nil)
		require.True(t, QueryMoney(r, "min_monthly_cost").IsZero())
	})
	t.Run("parses a bare decimal as USD", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/x?min_monthly_cost=12.50", nil)
		got := QueryMoney(r, "min_monthly_cost")
		require.False(t, got.IsZero())
	})
	t.Run("malformed value defaults to zero rather than erroring", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/x?min_monthly_cost=not-a-number", nil)
		require.True(t, QueryMoney(r, "min_monthly_cost").IsZero())
	})
}

func TestQueryBool(t *testing.T) {
	require.False(t, QueryBool(httptest.NewRequest("GET", "/x", nil), "collapse"))
	require.True(t, QueryBool(httptest.NewRequest("GET", "/x?collapse=true", nil), "collapse"))
	require.False(t, QueryBool(httptest.NewRequest("GET", "/x?collapse=nonsense", nil), "collapse"))
}

func TestQueryInt(t *testing.T) {
	require.Equal(t, 20, QueryInt(httptest.NewRequest("GET", "/x", nil), "limit", 20))
	require.Equal(t, 5, QueryInt(httptest.NewRequest("GET", "/x?limit=5", nil), "limit", 20))
	require.Equal(t, 20, QueryInt(httptest.NewRequest("GET", "/x?limit=nonsense", nil), "limit", 20))
}

func TestQueryFloat(t *testing.T) {
	require.Zero(t, QueryFloat(httptest.NewRequest("GET", "/x", nil), "min_confidence"))
	require.Equal(t, 0.8, QueryFloat(httptest.NewRequest("GET", "/x?min_confidence=0.8", nil), "min_confidence"))
}
