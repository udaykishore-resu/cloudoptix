package onboarding

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/spec"
)

func TestApplyExtraction_SetsValuesAndProvenance(t *testing.T) {
	draft := newDraftSpec()
	applyExtraction(&draft, map[string]any{
		"organization_name": "Meridian Retail",
		"industry":          "e-commerce",
		"application_name":  "Checkout Platform",
		"aws_account_ids":   []string{"111111111111"},
		"aws_regions":       []string{"us-east-1"},
		"environments":      []string{"production"},
		"risk_tolerance":    "medium",
	})

	assert.Equal(t, "Meridian Retail", draft.Organization.Name)
	assert.Equal(t, core.ProvenanceConfirmed, draft.Provenance["organization.name"])
	assert.Equal(t, "Checkout Platform", draft.Application.Name)
	require.Len(t, draft.AWS.Accounts, 1)
	assert.Equal(t, "111111111111", draft.AWS.Accounts[0].ID)
	assert.Equal(t, "production", draft.AWS.Accounts[0].Environment)
	assert.Equal(t, "assume_role", draft.Security.AWSAccessMode)
	assert.Equal(t, core.ProvenanceInferred, draft.Provenance["security.awsAccessMode"])
	assert.Equal(t, "medium", draft.Optimization.RiskTolerance)
}

func TestApplyExtraction_NeverDowngradesConfirmed(t *testing.T) {
	draft := newDraftSpec()
	applyExtraction(&draft, map[string]any{"organization_name": "Acme"})
	require.Equal(t, core.ProvenanceConfirmed, draft.Provenance["organization.name"])

	// A later extraction pass that finds nothing for this field must not
	// touch its provenance or value.
	applyExtraction(&draft, map[string]any{})
	assert.Equal(t, "Acme", draft.Organization.Name)
	assert.Equal(t, core.ProvenanceConfirmed, draft.Provenance["organization.name"])
}

func TestApplyExtraction_ToleratesJSONDecodedTypes(t *testing.T) {
	// A real model's structured tool-use response decodes through
	// encoding/json into []any and float64, not the deterministic
	// provider's native []string/float64 — applyExtraction must handle both.
	draft := newDraftSpec()
	applyExtraction(&draft, map[string]any{
		"compute_platforms": []any{"eks", "lambda"},
		"monthly_budget":    float64(50000),
		"business_transactions": []any{
			map[string]any{"name": "checkout", "monthly_volume": float64(40000)},
		},
	})
	assert.ElementsMatch(t, []string{"eks", "lambda"}, draft.Application.Architecture.ComputePlatforms)
	assert.Equal(t, float64(50000), draft.Objectives.MonthlyBudget)
	require.Len(t, draft.Business.Transactions, 1)
	assert.Equal(t, "checkout", draft.Business.Transactions[0].Name)
	assert.Equal(t, float64(40000), draft.Business.Transactions[0].MonthlyVolume)
}

func TestAssembleAccounts_PositionalPairing(t *testing.T) {
	accounts := assembleAccounts(nil,
		[]string{"111111111111", "222222222222"},
		[]string{"us-east-1"},
		[]string{"production", "staging"},
	)
	require.Len(t, accounts, 2)
	assert.Equal(t, "production", accounts[0].Environment)
	assert.Equal(t, "staging", accounts[1].Environment)
	assert.Equal(t, []string{"us-east-1"}, accounts[0].Regions, "a single region group applies to every account")
	assert.Equal(t, []string{"us-east-1"}, accounts[1].Regions)
}

func TestAssembleAccounts_SingleAccountDefaultsToProduction(t *testing.T) {
	accounts := assembleAccounts(nil, []string{"111111111111"}, []string{"us-east-1"}, nil)
	require.Len(t, accounts, 1)
	assert.Equal(t, "production", accounts[0].Environment)
	assert.True(t, accounts[0].Production)
}

func TestRunInference_AvailabilityDefaultByIndustry(t *testing.T) {
	draft := newDraftSpec()
	draft.Organization.Industry = "financial_services"
	runInference(&draft)
	assert.Equal(t, 0.9995, draft.Objectives.AvailabilityTarget)
	assert.Equal(t, core.ProvenanceInferred, draft.Provenance["objectives.availabilityTarget"])
	assert.Equal(t, "high", draft.Application.Criticality)
}

func TestRunInference_NeverOverwritesConfirmed(t *testing.T) {
	draft := newDraftSpec()
	draft.Objectives.AvailabilityTarget = 0.99
	setProvenance(&draft, "objectives.availabilityTarget", core.ProvenanceConfirmed)
	runInference(&draft)
	assert.Equal(t, 0.99, draft.Objectives.AvailabilityTarget, "a user-confirmed value must never be replaced by an inferred default")
}

func TestRunInference_DomainFromIndustry(t *testing.T) {
	draft := newDraftSpec()
	draft.Organization.Industry = "insurance"
	runInference(&draft)
	assert.Equal(t, "claims", draft.Application.Domain)
	assert.Equal(t, core.ProvenanceInferred, draft.Provenance["application.domain"])
}

func TestRunInference_ProductionGovernanceDefault(t *testing.T) {
	draft := newDraftSpec()
	draft.AWS.Accounts = []spec.Account{{ID: "111111111111", Environment: "production", Production: true}}
	runInference(&draft)
	assert.True(t, draft.Governance.ProductionChangesRequireApproval)
	assert.Equal(t, core.ProvenanceInferred, draft.Provenance["governance.productionChangesRequireApproval"])
}

func TestIsDontKnow(t *testing.T) {
	cases := map[string]bool{
		"I don't know": true,
		"not sure":     true,
		"idk":          true,
		"I don't know the exact number but it's around 50k requests a month": false,
		"we run on EKS": false,
		"":              false,
	}
	for msg, want := range cases {
		assert.Equal(t, want, isDontKnow(msg), "message: %q", msg)
	}
}

func TestIsShowSummaryRequest(t *testing.T) {
	assert.True(t, isShowSummaryRequest("show me what you know"))
	assert.True(t, isShowSummaryRequest("What do you know so far?"))
	assert.False(t, isShowSummaryRequest("we use postgres"))
}

func TestApplyDirectEdit_AvailabilityTarget(t *testing.T) {
	draft := newDraftSpec()
	draft.Objectives.AvailabilityTarget = 0.999
	change, ok := applyDirectEdit(&draft, "Change production SLO to 99.99%")
	require.True(t, ok)
	assert.Equal(t, "objectives.availabilityTarget", change.Path)
	assert.InDelta(t, 0.9999, draft.Objectives.AvailabilityTarget, 1e-9)
	assert.Equal(t, core.ProvenanceConfirmed, draft.Provenance["objectives.availabilityTarget"])
}

func TestApplyDirectEdit_NoMatch(t *testing.T) {
	draft := newDraftSpec()
	_, ok := applyDirectEdit(&draft, "we use kubernetes and postgres")
	assert.False(t, ok)
}

func TestApplyDirectEdit_RiskTolerance(t *testing.T) {
	draft := newDraftSpec()
	change, ok := applyDirectEdit(&draft, "Set the risk tolerance to low")
	require.True(t, ok)
	assert.Equal(t, "low", draft.Optimization.RiskTolerance)
	assert.Equal(t, "optimization.riskTolerance", change.Path)
}
