package copilot

import "github.com/udaykishore-resu/cloudoptix/internal/ports"

// BuildRegistry constructs the copilot's fixed, read-only tool set. store
// may be nil — search_knowledge then reports itself unavailable rather than
// panicking, so a deployment without RAG configured still gets the other
// fifteen tools.
func BuildRegistry(uow ports.UnitOfWork, store ports.KnowledgeStore) *Registry {
	r := NewRegistry()
	r.MustRegister(newCostSummaryTool(uow))
	r.MustRegister(newCostBreakdownTool(uow))
	r.MustRegister(newExplainCostChangeTool(uow))
	r.MustRegister(newListResourcesTool(uow))
	r.MustRegister(newGetResourceTool(uow))
	r.MustRegister(newEconomicFootprintTool(uow))
	r.MustRegister(newUnitEconomicsTool(uow))
	r.MustRegister(newListRecommendationsTool(uow))
	r.MustRegister(newGetRecommendationTool(uow))
	r.MustRegister(newEfficiencyScoreTool(uow))
	r.MustRegister(newCostSLOStatusTool(uow))
	r.MustRegister(newSavingsFunnelTool(uow))
	r.MustRegister(newQueryArchitectureGraphTool(uow))
	r.MustRegister(newBlastRadiusTool(uow))
	r.MustRegister(newRunCounterfactualTool(uow))
	r.MustRegister(newSearchKnowledgeTool(store))
	return r
}
