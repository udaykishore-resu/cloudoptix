package core

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// Sentinel errors that the whole platform maps onto HTTP status codes and
// retry decisions. Adapters wrap these rather than inventing their own, so the
// HTTP layer never needs to know which adapter failed.
var (
	ErrNotFound        = errors.New("not found")
	ErrAlreadyExists   = errors.New("already exists")
	ErrInvalidInput    = errors.New("invalid input")
	ErrUnauthenticated = errors.New("unauthenticated")
	ErrForbidden       = errors.New("forbidden")
	ErrConflict        = errors.New("conflict")
	ErrPreconditionOff = errors.New("precondition failed")
	ErrUnavailable     = errors.New("dependency unavailable")
	ErrThrottled       = errors.New("throttled")
	ErrTimeout         = errors.New("timeout")
	ErrNotImplemented  = errors.New("not implemented")
	// ErrTenantMismatch is raised whenever a repository or service is handed
	// an object belonging to a different tenant than the request scope. It is
	// always a bug or an attack, never a user error, and is logged at error
	// level with the audit trail attached.
	ErrTenantMismatch = errors.New("tenant mismatch")
	// ErrPolicyDenied is raised by the policy engine. It is separated from
	// ErrForbidden because it carries a decision record for the audit log.
	ErrPolicyDenied = errors.New("policy denied")
	// ErrGrounding is raised when an AI-produced structure references data
	// that does not exist in the tenant's model. It is the tripwire that stops
	// hallucinated resources reaching the execution path.
	ErrGrounding = errors.New("ungrounded model output")
)

// Error is the structured application error carried across layers. It keeps a
// machine code for clients, a human message, a wrapped sentinel for
// errors.Is checks, and arbitrary detail fields for the API problem document.
type Error struct {
	Code     string
	Message  string
	Sentinel error
	Details  map[string]any
	Err      error
}

// NewError builds a structured error around a sentinel.
func NewError(sentinel error, code, format string, args ...any) *Error {
	return &Error{
		Code:     code,
		Message:  fmt.Sprintf(format, args...),
		Sentinel: sentinel,
	}
}

// WithDetail attaches a structured field.
func (e *Error) WithDetail(key string, value any) *Error {
	if e.Details == nil {
		e.Details = map[string]any{}
	}
	e.Details[key] = value
	return e
}

// Wrap attaches an underlying cause.
func (e *Error) Wrap(err error) *Error { e.Err = err; return e }

// Error satisfies the error interface.
func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString(e.Code)
	b.WriteString(": ")
	b.WriteString(e.Message)
	if e.Err != nil {
		b.WriteString(": ")
		b.WriteString(e.Err.Error())
	}
	return b.String()
}

// Unwrap exposes the cause chain.
func (e *Error) Unwrap() error {
	if e.Err != nil {
		return e.Err
	}
	return e.Sentinel
}

// Is lets errors.Is match either the sentinel or the wrapped cause.
func (e *Error) Is(target error) bool {
	if e.Sentinel != nil && errors.Is(e.Sentinel, target) {
		return true
	}
	return e.Err != nil && errors.Is(e.Err, target)
}

// HTTPStatus maps an error onto the status code the API should return.
func HTTPStatus(err error) int {
	switch {
	case err == nil:
		return http.StatusOK
	case errors.Is(err, ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, ErrAlreadyExists), errors.Is(err, ErrConflict):
		return http.StatusConflict
	case errors.Is(err, ErrInvalidInput), errors.Is(err, ErrGrounding):
		return http.StatusBadRequest
	case errors.Is(err, ErrUnauthenticated):
		return http.StatusUnauthorized
	// A tenant mismatch is reported as 404, not 403: telling a caller that an
	// object exists in another tenant is itself a cross-tenant information
	// leak.
	case errors.Is(err, ErrTenantMismatch):
		return http.StatusNotFound
	case errors.Is(err, ErrForbidden), errors.Is(err, ErrPolicyDenied):
		return http.StatusForbidden
	case errors.Is(err, ErrPreconditionOff):
		return http.StatusPreconditionFailed
	case errors.Is(err, ErrThrottled):
		return http.StatusTooManyRequests
	case errors.Is(err, ErrTimeout):
		return http.StatusGatewayTimeout
	case errors.Is(err, ErrUnavailable):
		return http.StatusServiceUnavailable
	case errors.Is(err, ErrNotImplemented):
		return http.StatusNotImplemented
	default:
		return http.StatusInternalServerError
	}
}

// Retryable reports whether a failed operation is worth retrying. The
// discovery and execution engines consult this before scheduling a backoff.
func Retryable(err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, ErrThrottled), errors.Is(err, ErrTimeout), errors.Is(err, ErrUnavailable):
		return true
	default:
		return false
	}
}

// NotFound is the common constructor for a missing aggregate.
func NotFound(kind string, id any) *Error {
	return NewError(ErrNotFound, "not_found", "%s %v not found", kind, id)
}

// Invalid is the common constructor for a rejected input.
func Invalid(format string, args ...any) *Error {
	return NewError(ErrInvalidInput, "invalid_input", format, args...)
}

// Forbidden is the common constructor for an authorization failure.
func Forbidden(format string, args ...any) *Error {
	return NewError(ErrForbidden, "forbidden", format, args...)
}

// Conflict is the common constructor for an optimistic-concurrency failure.
func Conflict(format string, args ...any) *Error {
	return NewError(ErrConflict, "conflict", format, args...)
}

// ValidationIssue is a single field-level problem. Spec validation, policy
// linting and cost-regression checks all return slices of these so the UI can
// render them uniformly.
type ValidationIssue struct {
	Path     string   `json:"path"`
	Code     string   `json:"code"`
	Message  string   `json:"message"`
	Severity Severity `json:"severity"`
	Hint     string   `json:"hint,omitempty"`
}

// ValidationResult aggregates issues and reports overall acceptability.
type ValidationResult struct {
	Issues []ValidationIssue `json:"issues"`
}

// Add appends an issue.
func (v *ValidationResult) Add(path, code string, sev Severity, format string, args ...any) {
	v.Issues = append(v.Issues, ValidationIssue{
		Path:     path,
		Code:     code,
		Severity: sev,
		Message:  fmt.Sprintf(format, args...),
	})
}

// AddHint appends an issue carrying remediation advice.
func (v *ValidationResult) AddHint(path, code string, sev Severity, hint, format string, args ...any) {
	v.Issues = append(v.Issues, ValidationIssue{
		Path:     path,
		Code:     code,
		Severity: sev,
		Message:  fmt.Sprintf(format, args...),
		Hint:     hint,
	})
}

// Merge folds another result into this one.
func (v *ValidationResult) Merge(other ValidationResult) {
	v.Issues = append(v.Issues, other.Issues...)
}

// HasBlocking reports whether any issue is HIGH or CRITICAL, which is the bar
// for refusing to persist or execute.
func (v ValidationResult) HasBlocking() bool {
	for _, i := range v.Issues {
		if i.Severity.Order() >= SeverityHigh.Order() {
			return true
		}
	}
	return false
}

// Count returns the number of issues at or above a severity.
func (v ValidationResult) Count(min Severity) int {
	n := 0
	for _, i := range v.Issues {
		if i.Severity.Order() >= min.Order() {
			n++
		}
	}
	return n
}

// Err converts a blocking result into an error, or nil when acceptable.
func (v ValidationResult) Err() error {
	if !v.HasBlocking() {
		return nil
	}
	msgs := make([]string, 0, len(v.Issues))
	for _, i := range v.Issues {
		if i.Severity.Order() >= SeverityHigh.Order() {
			msgs = append(msgs, i.Path+": "+i.Message)
		}
	}
	return Invalid("validation failed: %s", strings.Join(msgs, "; ")).WithDetail("issues", v.Issues)
}
