package http

import (
	"net/http"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
)

// The Savings tag: a method on automationHandler. See the doc comment on
// automationHandler for why there is no dedicated SavingsService.
func (h *automationHandler) Funnel(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermExecutionRead)
	if !ok {
		return
	}
	period, err := ParsePeriod(r)
	if err != nil {
		WriteProblem(w, r, err)
		return
	}
	v, err := h.svc.Funnel(r.Context(), p.TenantID, period)
	respond(w, r, http.StatusOK, v, err)
}
