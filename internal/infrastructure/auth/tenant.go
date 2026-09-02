package auth

import (
	"context"
	"fmt"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/tenancy"
)

// UserLookup is the read the tenant resolver needs from the user store. It is
// satisfied by ports.UserRepository (GetBySubject has an identical shape) —
// declared structurally here rather than importing ports.UserRepository
// directly so this package's dependency surface stays exactly what it uses.
type UserLookup interface {
	GetBySubject(ctx context.Context, subject string) (tenancy.User, error)
}

// TenantHeader is the HTTP header a request uses to select which of a
// multi-tenant user's memberships a session should run as.
const TenantHeader = "X-CloudOptix-Tenant"

// ErrTenantHeaderMissing is returned when a token carries no tenant claim
// and the request supplied no tenant header either — there is nothing to
// resolve.
var ErrTenantHeaderMissing = fmt.Errorf("auth: request specifies no tenant (neither the token nor the %s header)", TenantHeader)

// ErrTenantMismatch is returned when the token's tenant claim and the
// request's tenant header disagree. This is treated as an authentication
// failure, not resolved in either claim's favour — a client presenting two
// different tenant scopes in the same request is either confused or probing,
// and CloudOptix does not guess which one it meant.
var ErrTenantMismatch = fmt.Errorf("auth: token tenant and %s header disagree", TenantHeader)

// ResolveTenant determines the tenant a request runs as and returns a fully
// scoped, membership-checked core.Principal.
//
// The resolution order: a token-scoped tenant claim and the request's
// tenant header must agree if both are present; whichever is present alone
// picks the tenant; and membership is then verified against the user's own
// record regardless of what either the token or the header claimed — a
// stale or forged claim of tenant scope is caught here even after it has
// passed signature validation, because tenancy.User.Principal only grants
// roles for a membership that is both on file and currently active.
func ResolveTenant(ctx context.Context, claims Claims, tenantHeader string, users UserLookup, clock core.Clock) (core.Principal, error) {
	tenant, err := pickTenant(claims.TenantID, tenantHeader)
	if err != nil {
		return core.Principal{}, core.NewError(core.ErrUnauthenticated, "tenant_unresolved", "%s", err.Error()).Wrap(err)
	}

	user, err := users.GetBySubject(ctx, claims.Subject)
	if err != nil {
		return core.Principal{}, core.NewError(core.ErrUnauthenticated, "unknown_subject",
			"no CloudOptix user record for token subject").Wrap(err)
	}
	if user.Disabled {
		return core.Principal{}, core.NewError(core.ErrForbidden, "user_disabled", "user %s is disabled", user.Email)
	}

	if clock == nil {
		clock = core.SystemClock{}
	}
	// tenancy.User.Principal returns core.Forbidden when the user has no
	// active membership in tenant — this is the check that catches a token
	// whose tenant claim outlived the grant it was issued under, or that
	// never had one to begin with. Signature validity proves who the caller
	// is; it says nothing about which tenants they currently belong to.
	principal, err := user.Principal(tenant, clock.Now())
	if err != nil {
		return core.Principal{}, err
	}
	return principal, nil
}

func pickTenant(claimTenant, headerTenant string) (core.TenantID, error) {
	switch {
	case claimTenant == "" && headerTenant == "":
		return "", ErrTenantHeaderMissing
	case claimTenant == "":
		return core.TenantID(headerTenant), nil
	case headerTenant == "":
		return core.TenantID(claimTenant), nil
	case claimTenant != headerTenant:
		return "", ErrTenantMismatch
	default:
		return core.TenantID(claimTenant), nil
	}
}
