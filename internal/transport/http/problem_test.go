package http

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
)

func decodeProblem(t *testing.T, rec *httptest.ResponseRecorder) Problem {
	t.Helper()
	require.Equal(t, problemContentType, rec.Header().Get("Content-Type"))
	var p Problem
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &p))
	return p
}

func TestWriteProblem_StatusMapping(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{"not found", core.NotFound("recommendation", "rec_123"), http.StatusNotFound, "not_found"},
		{"invalid input", core.Invalid("bad field"), http.StatusBadRequest, "invalid_input"},
		{"forbidden", core.Forbidden("nope"), http.StatusForbidden, "forbidden"},
		{
			"tenant mismatch maps to 404, not 403",
			core.NewError(core.ErrTenantMismatch, "tenant_mismatch", "object belongs to another tenant"),
			http.StatusNotFound, "tenant_mismatch",
		},
		{"plain sentinel, no core.Error wrapper", core.ErrThrottled, http.StatusTooManyRequests, "throttled"},
		{"unknown error defaults to 500", errors.New("boom"), http.StatusInternalServerError, "internal_error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/api/v1/recommendations/rec_123", nil)
			WriteProblem(rec, req, tc.err)

			require.Equal(t, tc.wantStatus, rec.Code)
			p := decodeProblem(t, rec)
			require.Equal(t, tc.wantStatus, p.Status)
			require.Equal(t, tc.wantCode, p.Code)
			require.Equal(t, "/api/v1/recommendations/rec_123", p.Instance)
		})
	}
}

// TestWriteProblem_TenantMismatchIndistinguishableFromNotFound is the
// contract that actually matters for tenant isolation: a genuine 404 and a
// cross-tenant 404 must render identically to the client, or the
// distinguishing detail itself becomes the leak the 404 mapping exists to
// prevent.
func TestWriteProblem_TenantMismatchIndistinguishableFromNotFound(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/recommendations/rec_123", nil)

	recA := httptest.NewRecorder()
	WriteProblem(recA, req, core.NotFound("recommendation", "rec_123"))
	pA := decodeProblem(t, recA)

	recB := httptest.NewRecorder()
	WriteProblem(recB, req, core.NewError(core.ErrTenantMismatch, "tenant_mismatch", "object belongs to another tenant"))
	pB := decodeProblem(t, recB)

	require.Equal(t, pA.Status, pB.Status)
	require.Equal(t, pA.Type, pB.Type)
	require.Equal(t, pA.Title, pB.Title)
}

// TestWriteProblem_ServerErrorDetailIsRedacted confirms a 5xx never echoes
// the underlying error text (which could carry a DSN, a stack fragment, or
// any other operator-only detail) to the client.
func TestWriteProblem_ServerErrorDetailIsRedacted(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/costs/summary", nil)
	rec := httptest.NewRecorder()
	WriteProblem(rec, req, errors.New("dial postgres://user:hunter2@10.0.0.5:5432/db: connection refused"))

	p := decodeProblem(t, rec)
	require.Equal(t, http.StatusInternalServerError, p.Status)
	require.NotContains(t, p.Detail, "hunter2")
	require.NotContains(t, p.Detail, "10.0.0.5")
	require.Equal(t, "an internal error occurred", p.Detail)
}

func TestWriteProblem_ValidationIssuesSurfaced(t *testing.T) {
	var vr core.ValidationResult
	vr.Add("name", "required", core.SeverityHigh, "name is required")
	err := core.Invalid("policy failed validation").WithDetail("issues", vr.Issues)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/policies", nil)
	rec := httptest.NewRecorder()
	WriteProblem(rec, req, err)

	p := decodeProblem(t, rec)
	require.Len(t, p.Issues, 1)
	require.Equal(t, "name", p.Issues[0].Path)
}

func TestWriteProblem_NilErrorWritesNothing(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/costs/summary", nil)
	rec := httptest.NewRecorder()
	WriteProblem(rec, req, nil)
	require.Equal(t, 200, rec.Code) // httptest default; WriteProblem never called WriteHeader
	require.Empty(t, rec.Body.Bytes())
}
