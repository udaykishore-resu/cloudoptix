package memstore

import (
	"context"
	"sync"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/audit"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/cost"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/econ"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/execute"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/govern"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/simulate"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/spec"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/tenancy"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// auditHead is the tenant's current chain position: the hash of the last
// sealed record and the sequence number to hand out next. Keeping it
// denormalised rather than reading the tail of AuditRecords on every Append
// is what keeps a busy tenant's audit writes O(1) instead of O(chain length).
type auditHead struct {
	Hash     string `json:"hash"`
	Sequence int64  `json:"sequence"`
}

// storeData is every piece of business state the store holds. It is kept as
// one struct, separate from the mutexes that guard it, so that Store.Snapshot
// and Store.Restore can deep-copy the whole thing in a single call rather
// than field by field — see clone.go for why a JSON round-trip is the chosen
// copy mechanism. Cache entries and distributed-lock state are deliberately
// NOT part of this struct: they are infrastructure, not tenant business data,
// and a rolled-back transaction has no more business rewinding the cache than
// a rolled-back Postgres transaction would have rewinding Redis.
type storeData struct {
	Tenants     map[core.TenantID]tenancy.Tenant
	TenantSlugs map[string]core.TenantID
	Orgs        map[core.TenantID]map[core.ID]tenancy.Organization

	Users          map[core.ID]tenancy.User
	UsersBySubject map[string]core.ID
	UsersByEmail   map[string]core.ID

	SpecVersions map[core.TenantID]map[core.ID]map[int]spec.Version
	SpecLatest   map[core.TenantID]map[core.ID]int

	AWSAccounts map[core.TenantID]map[core.ID]cloud.AWSAccount

	Resources     map[core.TenantID]map[core.ID]cloud.Resource
	ResourceByKey map[core.TenantID]map[string]core.ID
	Relationships map[core.TenantID][]cloud.Relationship

	Applications map[core.TenantID]map[core.ID]cloud.Application
	Workloads    map[core.TenantID]map[core.ID]cloud.Workload

	CostRecords map[core.TenantID][]cost.Record
	Anomalies   map[core.TenantID]map[core.ID]cost.Anomaly

	MetricSummaries map[core.TenantID]map[core.ID]ports.ResourceMetrics
	MetricSeries    map[core.TenantID][]ports.MetricSeries

	Recommendations map[core.TenantID]map[core.ID]optimize.Recommendation

	Policies     map[core.TenantID]map[core.ID]govern.Policy
	PolicyActive map[core.TenantID]core.ID
	Decisions    map[core.TenantID]map[core.ID]govern.Decision

	Approvals map[core.TenantID]map[core.ID]govern.Request

	Plans       map[core.TenantID]map[core.ID]execute.Plan
	Snapshots   map[core.TenantID]map[string]execute.Snapshot
	Validations map[core.TenantID]map[core.ID]execute.ValidationResult

	SavingsRecords map[core.TenantID]map[core.ID]execute.SavingsRecord
	Outcomes       map[core.TenantID][]execute.Outcome
	Calibrations   map[core.TenantID]map[optimize.RuleID]execute.RuleCalibration

	Footprints       map[core.TenantID][]econ.Footprint
	Transactions     map[core.TenantID]map[core.ID]econ.BusinessTransaction
	UnitEconomics    map[core.TenantID][]econ.UnitEconomics
	CostSLOs         map[core.TenantID]map[core.ID]econ.CostSLO
	BudgetStates     map[core.TenantID][]econ.EconomicErrorBudget
	EfficiencyScores map[core.TenantID][]econ.EfficiencyScore

	Simulations       map[core.TenantID]map[core.ID]simulate.Simulation
	Counterfactuals   map[core.TenantID]map[core.ID]simulate.Counterfactual
	Compilations      map[core.TenantID]map[core.ID]simulate.CompilationResult
	RegressionSuites  map[core.TenantID]map[string]simulate.RegressionSuite
	RegressionReports map[core.TenantID]map[core.ID]simulate.RegressionReport

	AuditRecords map[core.TenantID][]audit.Record
	AuditHead    map[core.TenantID]auditHead

	DiscoveryRuns map[core.TenantID]map[core.ID]ports.DiscoveryRun

	Conversations map[core.TenantID]map[core.ID]ports.Conversation

	Notifications map[core.TenantID]map[core.ID]ports.Notification

	// execLeases tracks which worker currently holds a claimed-but-unfinished
	// plan, keyed by plan id. It exists purely so ClaimDuePlans and
	// ClaimPlansAwaitingValidation can report who claimed what; the actual
	// single-claim guarantee comes from the plan's State transition (see
	// execution.go), not from this map, so its loss on Restore is harmless.
	ExecLeases map[core.ID]string
}

func newStoreData() storeData {
	return storeData{
		Tenants:     map[core.TenantID]tenancy.Tenant{},
		TenantSlugs: map[string]core.TenantID{},
		Orgs:        map[core.TenantID]map[core.ID]tenancy.Organization{},

		Users:          map[core.ID]tenancy.User{},
		UsersBySubject: map[string]core.ID{},
		UsersByEmail:   map[string]core.ID{},

		SpecVersions: map[core.TenantID]map[core.ID]map[int]spec.Version{},
		SpecLatest:   map[core.TenantID]map[core.ID]int{},

		AWSAccounts: map[core.TenantID]map[core.ID]cloud.AWSAccount{},

		Resources:     map[core.TenantID]map[core.ID]cloud.Resource{},
		ResourceByKey: map[core.TenantID]map[string]core.ID{},
		Relationships: map[core.TenantID][]cloud.Relationship{},

		Applications: map[core.TenantID]map[core.ID]cloud.Application{},
		Workloads:    map[core.TenantID]map[core.ID]cloud.Workload{},

		CostRecords: map[core.TenantID][]cost.Record{},
		Anomalies:   map[core.TenantID]map[core.ID]cost.Anomaly{},

		MetricSummaries: map[core.TenantID]map[core.ID]ports.ResourceMetrics{},
		MetricSeries:    map[core.TenantID][]ports.MetricSeries{},

		Recommendations: map[core.TenantID]map[core.ID]optimize.Recommendation{},

		Policies:     map[core.TenantID]map[core.ID]govern.Policy{},
		PolicyActive: map[core.TenantID]core.ID{},
		Decisions:    map[core.TenantID]map[core.ID]govern.Decision{},

		Approvals: map[core.TenantID]map[core.ID]govern.Request{},

		Plans:       map[core.TenantID]map[core.ID]execute.Plan{},
		Snapshots:   map[core.TenantID]map[string]execute.Snapshot{},
		Validations: map[core.TenantID]map[core.ID]execute.ValidationResult{},

		SavingsRecords: map[core.TenantID]map[core.ID]execute.SavingsRecord{},
		Outcomes:       map[core.TenantID][]execute.Outcome{},
		Calibrations:   map[core.TenantID]map[optimize.RuleID]execute.RuleCalibration{},

		Footprints:       map[core.TenantID][]econ.Footprint{},
		Transactions:     map[core.TenantID]map[core.ID]econ.BusinessTransaction{},
		UnitEconomics:    map[core.TenantID][]econ.UnitEconomics{},
		CostSLOs:         map[core.TenantID]map[core.ID]econ.CostSLO{},
		BudgetStates:     map[core.TenantID][]econ.EconomicErrorBudget{},
		EfficiencyScores: map[core.TenantID][]econ.EfficiencyScore{},

		Simulations:       map[core.TenantID]map[core.ID]simulate.Simulation{},
		Counterfactuals:   map[core.TenantID]map[core.ID]simulate.Counterfactual{},
		Compilations:      map[core.TenantID]map[core.ID]simulate.CompilationResult{},
		RegressionSuites:  map[core.TenantID]map[string]simulate.RegressionSuite{},
		RegressionReports: map[core.TenantID]map[core.ID]simulate.RegressionReport{},

		AuditRecords: map[core.TenantID][]audit.Record{},
		AuditHead:    map[core.TenantID]auditHead{},

		DiscoveryRuns: map[core.TenantID]map[core.ID]ports.DiscoveryRun{},

		Conversations: map[core.TenantID]map[core.ID]ports.Conversation{},

		Notifications: map[core.TenantID]map[core.ID]ports.Notification{},

		ExecLeases: map[core.ID]string{},
	}
}

// Store is the in-memory backing for every repository port. See the package
// doc for why one Store, and why one sync.RWMutex per aggregate rather than
// one per Store or one per field.
//
// Discipline enforced by every method in this package: never hold two of
// these mutexes at once. A handful of aggregates (cost, recommendations) need
// to read another aggregate's data (resources, to resolve an application id)
// to answer a query; they always fully acquire-and-release the foreign lock
// to copy out what they need before acquiring their own. That rule is what
// makes lock ordering a non-issue — there is never a nested pair of locks for
// two call sites to disagree about the order of — at the cost of an extra,
// short-lived read pass over the foreign aggregate on those queries.
type Store struct {
	tenantMu    sync.RWMutex
	userMu      sync.RWMutex
	specMu      sync.RWMutex
	awsMu       sync.RWMutex
	resourceMu  sync.RWMutex
	appMu       sync.RWMutex
	costMu      sync.RWMutex
	metricMu    sync.RWMutex
	recMu       sync.RWMutex
	policyMu    sync.RWMutex
	approvalMu  sync.RWMutex
	execMu      sync.RWMutex
	savingsMu   sync.RWMutex
	econMu      sync.RWMutex
	simMu       sync.RWMutex
	auditMu     sync.RWMutex
	discoveryMu sync.RWMutex
	convMu      sync.RWMutex
	notifMu     sync.RWMutex

	cacheMu sync.RWMutex
	cache   map[string]cacheEntry

	lockMu sync.Mutex
	locks  map[string]*lockState

	// txMu serialises UnitOfWork.Do calls; see the Do method's doc comment for
	// what that does and does not guarantee.
	txMu sync.Mutex

	data storeData
}

// New builds an empty Store, ready to use.
func New() *Store {
	return &Store{
		data:  newStoreData(),
		cache: map[string]cacheEntry{},
		locks: map[string]*lockState{},
	}
}

// Reset discards all state and starts over. Tests call this between cases
// instead of constructing a fresh Store so that anything holding a reference
// to the Store (a wired-up application service, say) keeps working against
// the same instance with a clean slate.
func (s *Store) Reset() {
	s.lockAllWrite()
	s.data = newStoreData()
	s.unlockAllWrite()

	s.cacheMu.Lock()
	s.cache = map[string]cacheEntry{}
	s.cacheMu.Unlock()

	s.lockMu.Lock()
	s.locks = map[string]*lockState{}
	s.lockMu.Unlock()
}

// Repositories builds the full ports.Repositories bundle backed by this
// Store. Every call returns lightweight wrapper values (each is just a
// pointer back to the Store), so constructing it repeatedly is cheap and
// callers are free to keep the result or call this again.
func (s *Store) Repositories() ports.Repositories {
	return ports.Repositories{
		Tenants:         &tenantRepo{s},
		Users:           &userRepo{s},
		Specs:           &specRepo{s},
		AWSAccounts:     &awsAccountRepo{s},
		Resources:       &resourceRepo{s},
		Applications:    &applicationRepo{s},
		Costs:           &costRepo{s},
		Metrics:         &metricRepo{s},
		Recommendations: &recommendationRepo{s},
		Policies:        &policyRepo{s},
		Approvals:       &approvalRepo{s},
		Executions:      &executionRepo{s},
		Savings:         &savingsRepo{s},
		Economics:       &economicsRepo{s},
		Simulations:     &simulationRepo{s},
		Audit:           &auditRepo{s},
		DiscoveryRuns:   &discoveryRunRepo{s},
		Conversations:   &conversationRepo{s},
		Notifications:   &notificationRepo{s},
	}
}

// lockAllWrite/unlockAllWrite take and release every aggregate mutex, used
// only by Snapshot, Restore and Reset to get a stop-the-world, internally
// consistent view of the whole store. Because no other code path in this
// package ever holds two of these mutexes at once (see the Store doc
// comment), the order used here cannot deadlock against anything else — there
// is no second call site holding a subset of these locks in a conflicting
// order to race against.
func (s *Store) lockAllWrite() {
	s.tenantMu.Lock()
	s.userMu.Lock()
	s.specMu.Lock()
	s.awsMu.Lock()
	s.resourceMu.Lock()
	s.appMu.Lock()
	s.costMu.Lock()
	s.metricMu.Lock()
	s.recMu.Lock()
	s.policyMu.Lock()
	s.approvalMu.Lock()
	s.execMu.Lock()
	s.savingsMu.Lock()
	s.econMu.Lock()
	s.simMu.Lock()
	s.auditMu.Lock()
	s.discoveryMu.Lock()
	s.convMu.Lock()
	s.notifMu.Lock()
}

func (s *Store) unlockAllWrite() {
	s.notifMu.Unlock()
	s.convMu.Unlock()
	s.discoveryMu.Unlock()
	s.auditMu.Unlock()
	s.simMu.Unlock()
	s.econMu.Unlock()
	s.savingsMu.Unlock()
	s.execMu.Unlock()
	s.approvalMu.Unlock()
	s.policyMu.Unlock()
	s.recMu.Unlock()
	s.metricMu.Unlock()
	s.costMu.Unlock()
	s.appMu.Unlock()
	s.resourceMu.Unlock()
	s.awsMu.Unlock()
	s.specMu.Unlock()
	s.userMu.Unlock()
	s.tenantMu.Unlock()
}

// StoreSnapshot is an opaque point-in-time copy of the store's business data,
// returned by Store.Snapshot and consumed by Store.Restore. Treat it as a
// token: its fields are unexported on purpose.
type StoreSnapshot struct {
	data storeData
}

// Snapshot captures every aggregate's current state in one atomic pass.
func (s *Store) Snapshot() *StoreSnapshot {
	s.lockAllWrite()
	defer s.unlockAllWrite()
	return &StoreSnapshot{data: deepCopy(s.data)}
}

// Restore replaces the store's business state with a previously captured
// snapshot. Cache entries and lock state are untouched — see the storeData
// doc comment for why.
func (s *Store) Restore(snap *StoreSnapshot) {
	s.lockAllWrite()
	defer s.unlockAllWrite()
	s.data = deepCopy(snap.data)
}

// Do implements ports.UnitOfWork.
//
// The approach is copy-on-write in the sense that every write inside fn goes
// straight through the normal repository methods against the live store —
// there is no separate staging area to merge back on success, which is what
// keeps this implementation small and obviously correct for the common case.
// What makes it transactional is the other half: Do captures a full
// Snapshot before calling fn, and — only if fn returns an error — Restores
// it, undoing every write fn made, whichever aggregates they touched.
//
// The cost of that simplicity: Snapshot/Restore act on the WHOLE store, not
// just the aggregates fn is about to touch, so every transaction pays for a
// deep copy proportional to total store size rather than to the size of the
// change — wrong for a production-scale store, fine for the demo tenant and
// a test suite this package exists to serve. txMu also serialises Do calls,
// so only one transaction runs at a time; it does NOT block non-transactional
// repository calls made directly against s.Repositories() while fn is
// running. A write that lands from outside the transaction during that
// window is included in the Snapshot's "before" state only if it happened
// before Do started, so it survives a rollback undisturbed if fn succeeds,
// but is also rolled back alongside fn's own writes if fn fails. CloudOptix's
// application layer does not interleave unrelated writes with an in-flight
// multi-aggregate transaction, so that edge case does not arise in practice;
// a Postgres-backed UnitOfWork gets true snapshot isolation from the
// database, which this in-memory one deliberately does not attempt to
// reproduce.
func (s *Store) Do(ctx context.Context, fn func(ctx context.Context, repos ports.Repositories) error) error {
	s.txMu.Lock()
	defer s.txMu.Unlock()

	before := s.Snapshot()
	if err := fn(ctx, s.Repositories()); err != nil {
		s.Restore(before)
		return err
	}
	return nil
}

var _ ports.UnitOfWork = (*Store)(nil)

// Cache returns the ports.Cache backed by this Store. It is separate from
// Repositories because ports.Cache is not part of the ports.Repositories
// bundle — it is a cross-cutting service, not a per-aggregate persistence
// port.
func (s *Store) Cache() ports.Cache { return &cacheRepo{s} }

// Locker returns the ports.Locker backed by this Store.
func (s *Store) Locker() ports.Locker { return &lockerRepo{s} }

var (
	_ ports.Cache  = (*cacheRepo)(nil)
	_ ports.Locker = (*lockerRepo)(nil)
)
