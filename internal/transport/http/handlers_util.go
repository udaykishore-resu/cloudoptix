package http

import (
	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/tenancy"
)

// planOf maps a request's plan string onto tenancy.Plan, defaulting to the
// trial tier — approving onboarding with an unrecognised or absent plan
// value should never fail the whole request over a cosmetic field.
func planOf(s string) tenancy.Plan {
	switch tenancy.Plan(s) {
	case tenancy.PlanTrial, tenancy.PlanStandard, tenancy.PlanEnterprise, tenancy.PlanInternal:
		return tenancy.Plan(s)
	default:
		return tenancy.PlanTrial
	}
}

// rolesOf maps request role strings onto core.Role, dropping anything the
// platform does not recognise rather than failing the whole request — an
// operator adding a role to a client before the server side knows about it
// should not be able to lock themselves out of every other role in the same
// call.
func rolesOf(ss []string) []core.Role {
	out := make([]core.Role, 0, len(ss))
	for _, s := range ss {
		r := core.Role(s)
		if r.Valid() && r != core.RoleSystem {
			out = append(out, r)
		}
	}
	return out
}

// roleScopeOf maps a request string onto cloud.RoleScope, defaulting to read.
func roleScopeOf(s string) cloud.RoleScope {
	switch cloud.RoleScope(s) {
	case cloud.ScopeRead, cloud.ScopeAnalyze, cloud.ScopePlan, cloud.ScopeExecute:
		return cloud.RoleScope(s)
	default:
		return cloud.ScopeRead
	}
}
