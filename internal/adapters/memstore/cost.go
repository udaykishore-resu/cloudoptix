package memstore

import (
	"context"
	"strings"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/cost"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// costRepo implements ports.CostRepository.
type costRepo struct{ s *Store }

func (r *costRepo) UpsertBatch(ctx context.Context, tenant core.TenantID, records []cost.Record) (int, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return 0, err
	}
	r.s.costMu.Lock()
	defer r.s.costMu.Unlock()

	existing := r.s.data.CostRecords[tenant]
	byID := make(map[core.ID]int, len(existing))
	for i, rec := range existing {
		if !rec.ID.IsZero() {
			byID[rec.ID] = i
		}
	}
	n := 0
	for _, rec := range records {
		if rec.TenantID != tenant {
			return n, core.NewError(core.ErrTenantMismatch, "tenant_mismatch",
				"cost record belongs to tenant %s, not %s", rec.TenantID, tenant)
		}
		if rec.ID.IsZero() {
			rec.ID = core.NewID("cst")
		}
		if idx, ok := byID[rec.ID]; ok {
			existing[idx] = deepCopy(rec)
		} else {
			byID[rec.ID] = len(existing)
			existing = append(existing, deepCopy(rec))
		}
		n++
	}
	r.s.data.CostRecords[tenant] = existing
	return n, nil
}

// resourceApplications returns a resourceID -> applicationID lookup for a
// tenant, read fresh from the resource aggregate under its own lock and
// fully released before the caller takes costMu. See the Store doc comment
// for why: never hold two aggregate mutexes at once.
func (r *costRepo) resourceApplications(tenant core.TenantID) map[core.ID]core.ID {
	r.s.resourceMu.RLock()
	defer r.s.resourceMu.RUnlock()
	out := make(map[core.ID]core.ID, len(r.s.data.Resources[tenant]))
	for id, res := range r.s.data.Resources[tenant] {
		if !res.ApplicationID.IsZero() {
			out[id] = res.ApplicationID
		}
	}
	return out
}

func (r *costRepo) resourceNames(tenant core.TenantID) map[core.ID]string {
	r.s.resourceMu.RLock()
	defer r.s.resourceMu.RUnlock()
	out := make(map[core.ID]string, len(r.s.data.Resources[tenant]))
	for id, res := range r.s.data.Resources[tenant] {
		out[id] = res.DisplayName()
	}
	return out
}

// resolvedBasis is the amortization basis a filter effectively selects.
// Defaulting the unset case to amortized — rather than "any basis" — is what
// keeps every aggregation exact: CUR-style ingestion can store one usage line
// under several bases (amortized, unblended, net_amortized), and summing
// across all of them for an unfiltered query would double- or triple-count
// the same underlying usage.
func resolvedBasis(f ports.CostFilter) cost.AmortizationBasis {
	if f.Basis != "" {
		return f.Basis
	}
	return cost.BasisAmortized
}

func matchesCostFilter(rec cost.Record, f ports.CostFilter, basis cost.AmortizationBasis, resourceApp map[core.ID]core.ID) bool {
	if rec.Basis != basis {
		return false
	}
	if !f.Period.IsZero() && !f.Period.Contains(rec.Period.Start) {
		return false
	}
	if len(f.AccountIDs) > 0 && !containsVal(f.AccountIDs, rec.AccountID) {
		return false
	}
	if len(f.Regions) > 0 && !containsVal(f.Regions, rec.Region) {
		return false
	}
	if len(f.Services) > 0 && !containsVal(f.Services, rec.Service) {
		return false
	}
	if len(f.Environments) > 0 && !containsVal(f.Environments, rec.Environment) {
		return false
	}
	if len(f.ResourceIDs) > 0 && !containsVal(f.ResourceIDs, rec.ResourceID) {
		return false
	}
	if len(f.ChargeTypes) > 0 && !containsVal(f.ChargeTypes, rec.ChargeType) {
		return false
	}
	if !f.ApplicationID.IsZero() {
		if resourceApp[rec.ResourceID] != f.ApplicationID {
			return false
		}
	}
	if f.TagKey != "" {
		v, ok := rec.Tags.Get(f.TagKey)
		if !ok {
			return false
		}
		if f.TagValue != "" && !strings.EqualFold(v, f.TagValue) {
			return false
		}
	}
	return true
}

func (r *costRepo) matched(tenant core.TenantID, f ports.CostFilter) []cost.Record {
	basis := resolvedBasis(f)
	var resourceApp map[core.ID]core.ID
	if !f.ApplicationID.IsZero() {
		resourceApp = r.resourceApplications(tenant)
	}
	r.s.costMu.RLock()
	defer r.s.costMu.RUnlock()
	out := make([]cost.Record, 0)
	for _, rec := range r.s.data.CostRecords[tenant] {
		if matchesCostFilter(rec, f, basis, resourceApp) {
			out = append(out, rec)
		}
	}
	return out
}

func bucketGranularity(g cost.Granularity) cost.Granularity {
	switch g {
	case cost.GranularityHourly, cost.GranularityMonthly:
		return g
	default:
		return cost.GranularityDaily
	}
}

func bucketStart(t time.Time, g cost.Granularity) time.Time {
	u := t.UTC()
	switch g {
	case cost.GranularityHourly:
		return time.Date(u.Year(), u.Month(), u.Day(), u.Hour(), 0, 0, 0, time.UTC)
	case cost.GranularityMonthly:
		return time.Date(u.Year(), u.Month(), 1, 0, 0, 0, 0, time.UTC)
	default:
		return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
	}
}

func bucketNext(t time.Time, g cost.Granularity) time.Time {
	switch g {
	case cost.GranularityHourly:
		return t.Add(time.Hour)
	case cost.GranularityMonthly:
		return t.AddDate(0, 1, 0)
	default:
		return t.AddDate(0, 0, 1)
	}
}

// Series implements ports.CostRepository. Bucket boundaries are computed by
// stepping through the filter's period, and every matched record's amount is
// added — with integer-micros core.Money arithmetic throughout, never a float
// — into the single bucket its Period.Start falls in, so the sum of every
// point's Amount always exactly equals the sum of the matched records'
// Amount: there is no rounding step in this aggregation for one to diverge
// from the other.
func (r *costRepo) Series(ctx context.Context, tenant core.TenantID, f ports.CostFilter) (cost.Series, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return cost.Series{}, err
	}
	matched := r.matched(tenant, f)
	g := bucketGranularity(f.Granularity)

	period := f.Period
	if period.IsZero() {
		period = spanOf(matched)
	}
	if period.IsZero() {
		return cost.Series{Granularity: g, Currency: core.USD}, nil
	}

	ccy := core.USD
	sums := map[time.Time]core.Money{}
	for _, rec := range matched {
		b := bucketStart(rec.Period.Start, g)
		ccy = rec.Amount.Currency()
		sums[b] = sums[b].MustAdd(rec.Amount)
	}

	var points []cost.Point
	for b := bucketStart(period.Start, g); b.Before(period.End); b = bucketNext(b, g) {
		amt, ok := sums[b]
		if !ok {
			amt = core.MoneyFromMicros(0, ccy)
		}
		points = append(points, cost.Point{Period: core.NewPeriod(b, bucketNext(b, g)), Amount: amt})
	}
	return cost.Series{Granularity: g, Points: points, Currency: ccy}, nil
}

func spanOf(records []cost.Record) core.Period {
	if len(records) == 0 {
		return core.Period{}
	}
	start, end := records[0].Period.Start, records[0].Period.End
	for _, rec := range records[1:] {
		if rec.Period.Start.Before(start) {
			start = rec.Period.Start
		}
		if rec.Period.End.After(end) {
			end = rec.Period.End
		}
	}
	return core.NewPeriod(start, end)
}

// Breakdown implements ports.CostRepository, grouping matched records by the
// named dimension and delegating the share/sort computation to
// cost.NewBreakdown so this adapter and the Postgres one can never disagree
// about how a share is computed.
func (r *costRepo) Breakdown(ctx context.Context, tenant core.TenantID, f ports.CostFilter, dimension string) (cost.Breakdown, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return cost.Breakdown{}, err
	}
	matched := r.matched(tenant, f)

	var resourceApp map[core.ID]core.ID
	var resourceLabel map[core.ID]string
	if dimension == "application" {
		resourceApp = r.resourceApplications(tenant)
	}
	if dimension == "resource" {
		resourceLabel = r.resourceNames(tenant)
	}

	amounts := map[string]core.Money{}
	labels := map[string]string{}
	for _, rec := range matched {
		var key string
		switch dimension {
		case "service":
			key = rec.Service
		case "account":
			key = string(rec.AccountID)
		case "region":
			key = string(rec.Region)
		case "environment":
			key = string(rec.Environment)
		case "usage_type":
			key = rec.UsageType
		case "application":
			appID := resourceApp[rec.ResourceID]
			if appID.IsZero() {
				key = "__unattributed__"
			} else {
				key = appID.String()
			}
		case "resource":
			if rec.ResourceID.IsZero() {
				continue
			}
			key = rec.ResourceID.String()
			labels[key] = resourceLabel[rec.ResourceID]
		default:
			return cost.Breakdown{}, core.Invalid("cost breakdown: unknown dimension %q", dimension)
		}
		if key == "" {
			key = "__unknown__"
		}
		amounts[key] = amounts[key].MustAdd(rec.Amount)
	}

	period := f.Period
	if period.IsZero() {
		period = spanOf(matched)
	}
	b := cost.NewBreakdown(dimension, period, amounts)
	for i := range b.Items {
		b.Items[i].Label = labels[b.Items[i].Key]
	}
	return b, nil
}

func (r *costRepo) Total(ctx context.Context, tenant core.TenantID, f ports.CostFilter) (core.Money, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return core.Money{}, err
	}
	total := core.ZeroUSD()
	for _, rec := range r.matched(tenant, f) {
		total = total.MustAdd(rec.Amount)
	}
	return total, nil
}

func (r *costRepo) ByResource(ctx context.Context, tenant core.TenantID, f ports.CostFilter) (map[core.ID]core.Money, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return nil, err
	}
	out := map[core.ID]core.Money{}
	for _, rec := range r.matched(tenant, f) {
		if rec.ResourceID.IsZero() {
			continue
		}
		out[rec.ResourceID] = out[rec.ResourceID].MustAdd(rec.Amount)
	}
	return out, nil
}

func (r *costRepo) LatestIngestedAt(ctx context.Context, tenant core.TenantID) (time.Time, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return time.Time{}, err
	}
	r.s.costMu.RLock()
	defer r.s.costMu.RUnlock()
	var latest time.Time
	for _, rec := range r.s.data.CostRecords[tenant] {
		if rec.IngestedAt.After(latest) {
			latest = rec.IngestedAt
		}
	}
	return latest, nil
}

func (r *costRepo) SaveAnomalies(ctx context.Context, tenant core.TenantID, anomalies []cost.Anomaly) error {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return err
	}
	r.s.costMu.Lock()
	defer r.s.costMu.Unlock()
	if r.s.data.Anomalies[tenant] == nil {
		r.s.data.Anomalies[tenant] = map[core.ID]cost.Anomaly{}
	}
	for _, a := range anomalies {
		if a.ID.IsZero() {
			a.ID = core.NewID("anm")
		}
		r.s.data.Anomalies[tenant][a.ID] = deepCopy(a)
	}
	return nil
}

func (r *costRepo) ListAnomalies(ctx context.Context, tenant core.TenantID, from, to time.Time, opts ports.ListOptions) (ports.Page[cost.Anomaly], error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return ports.Page[cost.Anomaly]{}, err
	}
	r.s.costMu.RLock()
	items := make([]cost.Anomaly, 0)
	for _, a := range r.s.data.Anomalies[tenant] {
		if !from.IsZero() && a.DetectedAt.Before(from) {
			continue
		}
		if !to.IsZero() && !a.DetectedAt.Before(to) {
			continue
		}
		items = append(items, deepCopy(a))
	}
	r.s.costMu.RUnlock()

	keyOf := func(a cost.Anomaly) (string, string) { return a.DetectedAt.Format(sortTimeLayout), a.ID.String() }
	sortByCreatedThenID(items, keyOf)
	return paginate(items, opts, keyOf), nil
}

func (r *costRepo) AcknowledgeAnomaly(ctx context.Context, tenant core.TenantID, id core.ID, by string) error {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return err
	}
	r.s.costMu.Lock()
	defer r.s.costMu.Unlock()
	a, ok := r.s.data.Anomalies[tenant][id]
	if !ok {
		return core.NotFound("anomaly", id)
	}
	a.Acknowledged = true
	_ = by // acknowledger identity belongs in the audit trail, not the anomaly
	r.s.data.Anomalies[tenant][id] = a
	return nil
}
