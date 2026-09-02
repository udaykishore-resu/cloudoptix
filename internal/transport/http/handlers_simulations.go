package http

import (
	"io"
	"net/http"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/simulate"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// simulationsHandler serves three OpenAPI tags off one SimulationService:
// Simulations (architecture mutation and counterfactuals), Cost Compiler
// (pricing an IaC change set) and Cost Regression (checking a compilation
// against a suite of budget assertions). They are one port because the
// domain treats them as one engine: mutation, counterfactual and compiler
// all answer "what would this cost", differing only in what "this" is.
type simulationsHandler struct{ svc ports.SimulationService }

type mutateRequest struct {
	Scope         string   `json:"scope"`
	ScopeID       string   `json:"scope_id"`
	Name          string   `json:"name"`
	RiskTolerance string   `json:"risk_tolerance"`
	Patterns      []string `json:"patterns"`
	MaxCandidates int      `json:"max_candidates"`
}

func (h *simulationsHandler) Mutate(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermSimulationRun)
	if !ok {
		return
	}
	req, ok := decodeBody[mutateRequest](w, r)
	if !ok {
		return
	}
	v, err := h.svc.MutateArchitecture(r.Context(), p.TenantID, ports.MutationRequest{
		Scope: req.Scope, ScopeID: core.ID(req.ScopeID), Name: req.Name,
		RiskTolerance: req.RiskTolerance, Patterns: req.Patterns,
		MaxCandidates: req.MaxCandidates, RequestedBy: p.Describe(),
	})
	respond(w, r, http.StatusAccepted, v, err)
}

func (h *simulationsHandler) Counterfactual(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermSimulationRun)
	if !ok {
		return
	}
	s, ok := decodeBody[simulate.Scenario](w, r)
	if !ok {
		return
	}
	v, err := h.svc.Counterfactual(r.Context(), p.TenantID, s)
	respond(w, r, http.StatusOK, v, err)
}

func (h *simulationsHandler) GetSimulation(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermSimulationRun)
	if !ok {
		return
	}
	v, err := h.svc.GetSimulation(r.Context(), p.TenantID, PathID(r, "id"))
	respond(w, r, http.StatusOK, v, err)
}

func (h *simulationsHandler) ListSimulations(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermSimulationRun)
	if !ok {
		return
	}
	v, err := h.svc.ListSimulations(r.Context(), p.TenantID, ParseListOptions(r))
	respond(w, r, http.StatusOK, v, err)
}

// --- Cost Compiler tag -----------------------------------------------------

// Compile accepts the IaC artifact as a raw body (Terraform plan JSON,
// CloudFormation template, a Kubernetes manifest — whatever the source query
// parameter declares) rather than a JSON envelope, so a CI pipeline can pipe
// `terraform show -json plan.out` straight into the request body without an
// intermediate wrapping step.
func (h *simulationsHandler) Compile(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermCompilerRun)
	if !ok {
		return
	}
	content, err := io.ReadAll(io.LimitReader(r.Body, maxDecodeBytes))
	if err != nil {
		WriteProblem(w, r, core.Invalid("reading request body: %s", err.Error()))
		return
	}
	q := r.URL.Query()
	assumptions := map[string]float64{}
	for k := range q {
		const prefix = "assume_"
		if len(k) > len(prefix) && k[:len(prefix)] == prefix {
			if f := QueryFloat(r, k); f != 0 {
				assumptions[k[len(prefix):]] = f
			}
		}
	}
	v, err := h.svc.Compile(r.Context(), p.TenantID, ports.CompileRequest{
		Source:      simulate.SourceKind(orDefault(q.Get("source"), string(simulate.SourceTerraformPlan))),
		Label:       q.Get("label"),
		Content:     content,
		Region:      core.Region(q.Get("region")),
		AccountID:   core.AccountID(q.Get("account_id")),
		Environment: core.NormalizeEnvironment(q.Get("environment")),
		Assumptions: assumptions,
		RequestedBy: p.Describe(),
	})
	respond(w, r, http.StatusCreated, v, err)
}

func (h *simulationsHandler) GetCompilation(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermCompilerRun)
	if !ok {
		return
	}
	v, err := h.svc.GetCompilation(r.Context(), p.TenantID, PathID(r, "id"))
	respond(w, r, http.StatusOK, v, err)
}

// --- Cost Regression tag ----------------------------------------------------

func (h *simulationsHandler) RunRegression(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermCompilerRun)
	if !ok {
		return
	}
	suite := orDefault(r.URL.Query().Get("suite"), "default")
	v, err := h.svc.RunRegression(r.Context(), p.TenantID, PathID(r, "compilationID"), suite)
	respond(w, r, http.StatusOK, v, err)
}

func (h *simulationsHandler) UpsertRegressionSuite(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermCompilerRun)
	if !ok {
		return
	}
	s, ok := decodeBody[simulate.RegressionSuite](w, r)
	if !ok {
		return
	}
	s.TenantID = p.TenantID
	v, err := h.svc.UpsertRegressionSuite(r.Context(), s)
	respond(w, r, http.StatusOK, v, err)
}

func (h *simulationsHandler) ListRegressionSuites(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermCompilerRun)
	if !ok {
		return
	}
	v, err := h.svc.ListRegressionSuites(r.Context(), p.TenantID)
	respond(w, r, http.StatusOK, v, err)
}
