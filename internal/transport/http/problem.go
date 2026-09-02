package http

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
)

// Problem is an RFC 7807 problem+json document, extended with the
// platform's own machine-readable error code (Code) and, when present, the
// field-level validation issues core.ValidationResult produces — the same
// shape the onboarding and policy-simulation UIs already render, so the API
// never needs a second error format for "the request had several things
// wrong with it" versus "the request had one thing wrong with it".
type Problem struct {
	Type      string                 `json:"type"`
	Title     string                 `json:"title"`
	Status    int                    `json:"status"`
	Detail    string                 `json:"detail,omitempty"`
	Instance  string                 `json:"instance,omitempty"`
	Code      string                 `json:"code"`
	RequestID string                 `json:"request_id,omitempty"`
	Issues    []core.ValidationIssue `json:"issues,omitempty"`
}

const problemContentType = "application/problem+json"

// problemBase maps a status code onto the RFC 7807 "type" URI and human
// title CloudOptix uses across the whole API — kept as one table so a
// client integrating against the API sees the same title for the same
// status everywhere, regardless of which handler produced it.
func problemBase(status int) (typ, title string) {
	switch status {
	case http.StatusBadRequest:
		return "https://docs.cloudoptix.io/errors/invalid-input", "Invalid Input"
	case http.StatusUnauthorized:
		return "https://docs.cloudoptix.io/errors/unauthenticated", "Unauthenticated"
	case http.StatusForbidden:
		return "https://docs.cloudoptix.io/errors/forbidden", "Forbidden"
	case http.StatusNotFound:
		return "https://docs.cloudoptix.io/errors/not-found", "Not Found"
	case http.StatusConflict:
		return "https://docs.cloudoptix.io/errors/conflict", "Conflict"
	case http.StatusPreconditionFailed:
		return "https://docs.cloudoptix.io/errors/precondition-failed", "Precondition Failed"
	case http.StatusTooManyRequests:
		return "https://docs.cloudoptix.io/errors/rate-limited", "Rate Limited"
	case http.StatusRequestEntityTooLarge:
		return "https://docs.cloudoptix.io/errors/payload-too-large", "Payload Too Large"
	case http.StatusGatewayTimeout:
		return "https://docs.cloudoptix.io/errors/timeout", "Upstream Timeout"
	case http.StatusServiceUnavailable:
		return "https://docs.cloudoptix.io/errors/unavailable", "Dependency Unavailable"
	case http.StatusNotImplemented:
		return "https://docs.cloudoptix.io/errors/not-implemented", "Not Implemented"
	default:
		return "https://docs.cloudoptix.io/errors/internal", "Internal Server Error"
	}
}

// WriteProblem renders err as application/problem+json.
//
// The mapping deliberately special-cases core.ErrTenantMismatch to 404
// rather than 403: a 403 tells the caller "this object exists, and you may
// not touch it", which for a cross-tenant request is itself the leak — it
// confirms another tenant's object ID is valid and lets an attacker
// enumerate IDs across the whole platform by watching 403-vs-404. A 404
// says nothing more than "there is nothing here for you", which is true
// from the caller's own tenant-scoped point of view and is the same
// response a request for a genuinely nonexistent ID gets.
func WriteProblem(w http.ResponseWriter, r *http.Request, err error) {
	if err == nil {
		return
	}
	status, code, detail, issues := classify(err)
	typ, title := problemBase(status)

	p := Problem{
		Type: typ, Title: title, Status: status, Detail: detail,
		Instance: r.URL.Path, Code: code, RequestID: RequestIDFrom(r.Context()),
		Issues: issues,
	}

	if status >= 500 {
		slog.ErrorContext(r.Context(), "request failed",
			slog.String("code", code), slog.Int("status", status),
			slog.String("path", r.URL.Path), slog.String("error", err.Error()))
	}

	w.Header().Set("Content-Type", problemContentType)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(p)
}

func classify(err error) (status int, code, detail string, issues []core.ValidationIssue) {
	var coreErr *core.Error
	if errors.As(err, &coreErr) {
		status = core.HTTPStatus(coreErr)
		code = coreErr.Code
		detail = coreErr.Message
		if v, ok := coreErr.Details["issues"].([]core.ValidationIssue); ok {
			issues = v
		}
		// A 5xx detail is never the raw wrapped error text: an internal
		// error's Message may echo a driver error (a DSN, a stack fragment)
		// that must not reach a client. A 4xx detail is safe and useful —
		// "recommendation rec_123 not found" tells the caller exactly what
		// to fix.
		if status >= 500 {
			detail = "an internal error occurred"
		}
		return status, code, detail, issues
	}

	status = core.HTTPStatus(err)
	code = genericCode(status)
	detail = err.Error()
	if status >= 500 {
		detail = "an internal error occurred"
	}
	return status, code, detail, nil
}

func genericCode(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "invalid_input"
	case http.StatusUnauthorized:
		return "unauthenticated"
	case http.StatusForbidden:
		return "forbidden"
	case http.StatusNotFound:
		return "not_found"
	case http.StatusConflict:
		return "conflict"
	case http.StatusPreconditionFailed:
		return "precondition_failed"
	case http.StatusTooManyRequests:
		return "throttled"
	case http.StatusGatewayTimeout:
		return "timeout"
	case http.StatusServiceUnavailable:
		return "unavailable"
	case http.StatusNotImplemented:
		return "not_implemented"
	default:
		return "internal_error"
	}
}
