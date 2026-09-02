package discovery

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sort"
	"sync"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// scanJob is one (service, region) unit of work.
type scanJob struct {
	region     core.Region
	discoverer ports.ResourceDiscoverer
}

// jobResult is one job's outcome: the raw discoverer output (empty on
// failure) plus the report entry that always exists either way.
type jobResult struct {
	job    scanJob
	out    ports.DiscoveryOutput
	result ports.ServiceScanResult
}

// executeAccounts scans every account in turn, isolating one account's
// assume-role or lock failure from the rest — the same failure-isolation
// principle scan applies per service, extended one level up so a single
// misconfigured account in a multi-account run cannot stop the others.
func (s *Service) executeAccounts(ctx context.Context, tenant core.TenantID, run *ports.DiscoveryRun, accounts []cloud.AWSAccount, in ports.DiscoveryRequest) {
	regionSet := map[core.Region]bool{}
	for _, account := range accounts {
		release, lockErr := s.acquireLock(ctx, tenant, account.AccountID)
		if lockErr != nil {
			run.Errors = append(run.Errors, fmt.Sprintf("account %s: %v", account.AccountID, lockErr))
			continue
		}
		s.scanAccount(ctx, tenant, run, account, in, regionSet)
		release()
	}
	run.Regions = sortedRegions(regionSet)

	// The run's overall State reflects both service-level outcomes (did the
	// jobs succeed) and account-level ones (did an account's assume-role or
	// lock even let jobs run at all) — a run where one account fails
	// entirely and another succeeds completely is "partial", not
	// "completed", even though every job that did run succeeded.
	succeeded, total := 0, 0
	for _, r := range run.ServiceResults {
		total++
		if r.Succeeded {
			succeeded++
		}
	}
	if total > 0 {
		run.Coverage = float64(succeeded) / float64(total)
	}
	switch {
	case total == 0:
		run.State = "failed"
	case succeeded == total && len(run.Errors) == 0:
		run.State = "completed"
	case succeeded == 0:
		run.State = "failed"
	default:
		run.State = "partial"
	}
}

// scanAccount runs every selected discoverer against every requested region
// of one account, persists what succeeded, and mutates run in place with
// this account's contribution to the totals.
func (s *Service) scanAccount(ctx context.Context, tenant core.TenantID, run *ports.DiscoveryRun, account cloud.AWSAccount, in ports.DiscoveryRequest, regionSet map[core.Region]bool) {
	session, err := s.Broker.Assume(ctx, account, cloud.ScopeRead)
	if err != nil {
		run.Errors = append(run.Errors, fmt.Sprintf("account %s: could not assume the read role: %v", account.AccountID, err))
		return
	}

	discoverers := s.selectedDiscoverers(in.Services)
	if len(discoverers) == 0 {
		run.Errors = append(run.Errors, fmt.Sprintf("account %s: no resource discoverers are registered", account.AccountID))
		return
	}

	regions := in.Regions
	if len(regions) == 0 {
		regions = account.Regions
	}
	for _, r := range regions {
		regionSet[r] = true
	}

	existing, _ := s.Repos.Resources.LoadInventory(ctx, tenant, ports.ResourceFilter{AccountIDs: []core.AccountID{account.AccountID}})
	existingKeys := map[string]bool{}
	if existing != nil {
		for _, r := range existing.All() {
			existingKeys[r.Key()] = true
		}
	}

	var jobs []scanJob
	for _, region := range regions {
		for _, d := range discoverers {
			jobs = append(jobs, scanJob{region: region, discoverer: d})
		}
	}
	results := s.runJobs(ctx, jobs, session, tenant, account, existing)

	type coverage struct {
		kinds    map[cloud.Kind]bool
		seenKeys map[string]bool
	}
	byRegion := map[core.Region]*coverage{}
	var discovered []cloud.Resource
	relsByRegion := map[core.Region][]cloud.Relationship{}
	now := s.clock().Now()

	for _, jr := range results {
		run.ServiceResults = append(run.ServiceResults, jr.result)
		if !jr.result.Succeeded {
			run.Errors = append(run.Errors, fmt.Sprintf("account %s %s/%s: %s",
				account.AccountID, jr.result.Service, jr.result.Region, jr.result.Error))
			continue
		}
		region := jr.job.region
		cv, ok := byRegion[region]
		if !ok {
			cv = &coverage{kinds: map[cloud.Kind]bool{}, seenKeys: map[string]bool{}}
			byRegion[region] = cv
		}
		for _, k := range jr.job.discoverer.Kinds() {
			cv.kinds[k] = true
		}
		for _, res := range jr.out.Resources {
			res.TenantID = tenant
			res.AccountID = account.AccountID
			res.Region = region
			res.DiscoveredBy = jr.job.discoverer.Service()
			res.LastSeenAt = now
			if res.FirstSeenAt.IsZero() {
				res.FirstSeenAt = now
			}
			cv.seenKeys[res.Key()] = true
			discovered = append(discovered, res)
		}
		relsByRegion[region] = append(relsByRegion[region], jr.out.Relationships...)
	}

	if attrCtx, aerr := s.loadAttributionContext(ctx, tenant, account); aerr == nil {
		for i := range discovered {
			applyAttribution(&discovered[i], attrCtx)
		}
	}

	newCount, updatedCount := 0, 0
	for _, r := range discovered {
		if existingKeys[r.Key()] {
			updatedCount++
		} else {
			newCount++
		}
	}
	if len(discovered) > 0 {
		if _, err := s.Repos.Resources.UpsertBatch(ctx, tenant, discovered); err != nil {
			run.Errors = append(run.Errors, fmt.Sprintf("account %s: persisting resources: %v", account.AccountID, err))
		}
	}
	relCount := 0
	for region, rels := range relsByRegion {
		if err := s.Repos.Resources.ReplaceRelationships(ctx, tenant, account.AccountID, region, rels); err != nil {
			run.Errors = append(run.Errors, fmt.Sprintf("account %s: persisting relationships for %s: %v", account.AccountID, region, err))
			continue
		}
		relCount += len(rels)
	}

	// The tombstone pass, scoped strictly to what this account's scan
	// actually covered: MarkAbsent is only ever called with the kinds a
	// region's successful discoverers reported and the keys they actually
	// saw this run — see the package doc for why that makes a partial scan
	// structurally unable to delete anything it did not re-observe.
	removed := 0
	for region, cv := range byRegion {
		if len(cv.kinds) == 0 {
			continue
		}
		kinds := make([]cloud.Kind, 0, len(cv.kinds))
		for k := range cv.kinds {
			kinds = append(kinds, k)
		}
		keys := make([]string, 0, len(cv.seenKeys))
		for k := range cv.seenKeys {
			keys = append(keys, k)
		}
		n, err := s.Repos.Resources.MarkAbsent(ctx, tenant, account.AccountID, region, kinds, keys, now)
		if err != nil {
			run.Errors = append(run.Errors, fmt.Sprintf("account %s: tombstone pass for %s: %v", account.AccountID, region, err))
			continue
		}
		removed += n
	}

	run.ResourcesDiscovered += newCount
	run.ResourcesUpdated += updatedCount
	run.ResourcesRemoved += removed
	run.RelationshipsFound += relCount

	if in.IncludeMetrics {
		run.MetricsCollected += s.collectMetrics(ctx, tenant, session, discovered)
	}
	if in.IncludeCost {
		s.collectCost(ctx, tenant, account, session)
	}
}

// runJobs dispatches every (service, region) job across a bounded worker
// pool, so a run against a large estate never opens hundreds of concurrent
// AWS connections at once regardless of how many discoverers and regions are
// in play.
func (s *Service) runJobs(ctx context.Context, jobs []scanJob, session ports.AWSSession, tenant core.TenantID, account cloud.AWSAccount, existing *cloud.Inventory) []jobResult {
	concurrency := s.MaxConcurrency
	if concurrency <= 0 {
		concurrency = 8
	}
	sem := make(chan struct{}, concurrency)
	results := make([]jobResult, len(jobs))
	var wg sync.WaitGroup
	for i, job := range jobs {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, job scanJob) {
			defer wg.Done()
			defer func() { <-sem }()
			in := ports.DiscoveryInput{
				TenantID: tenant, Session: session, AccountID: account.AccountID, Region: job.region, Existing: existing,
			}
			out, res := s.discoverWithRetry(ctx, job.discoverer, job.region, in)
			results[i] = jobResult{job: job, out: out, result: res}
		}(i, job)
	}
	wg.Wait()
	return results
}

// discoverWithRetry runs one job, retrying only errors core.Retryable
// classifies as transient, with exponential backoff and full jitter between
// attempts. A permission error is never retried — see the package doc.
func (s *Service) discoverWithRetry(ctx context.Context, d ports.ResourceDiscoverer, region core.Region, in ports.DiscoveryInput) (ports.DiscoveryOutput, ports.ServiceScanResult) {
	began := time.Now()
	result := ports.ServiceScanResult{Service: d.Service(), Region: string(region)}
	maxAttempts := s.MaxRetries
	if maxAttempts <= 0 {
		maxAttempts = 1
	}
	var lastErr error

retryLoop:
	for attempt := 0; attempt < maxAttempts; attempt++ {
		out, err := d.Discover(ctx, in)
		if err == nil {
			result.Succeeded = true
			result.Count = len(out.Resources)
			result.APICallCount = out.APICalls
			result.Throttled += out.Throttled
			result.DurationMS = time.Since(began).Milliseconds()
			return out, result
		}
		lastErr = err
		result.Throttled += out.Throttled
		if !core.Retryable(err) || attempt == maxAttempts-1 {
			break retryLoop
		}
		wait := backoffWithJitter(s.BaseBackoff, s.MaxBackoff, attempt)
		select {
		case <-ctx.Done():
			lastErr = ctx.Err()
			break retryLoop
		case <-time.After(wait):
		}
	}

	result.DurationMS = time.Since(began).Milliseconds()
	result.Error = lastErr.Error()
	if action, ok := missingPermissionAction(lastErr, d); ok {
		result.MissingPermission = action
	}
	return ports.DiscoveryOutput{}, result
}

// backoffWithJitter is exponential backoff with full jitter (AWS's own
// recommended strategy): the delay is a uniformly random duration between
// zero and the exponentially-growing cap, which spreads retries across a
// large fleet instead of every worker retrying in lockstep and re-triggering
// the same throttle it just backed off from.
func backoffWithJitter(base, max time.Duration, attempt int) time.Duration {
	if base <= 0 {
		base = 250 * time.Millisecond
	}
	if max <= 0 {
		max = 15 * time.Second
	}
	capDelay := base * time.Duration(1<<uint(attempt))
	if capDelay <= 0 || capDelay > max {
		capDelay = max
	}
	return time.Duration(rand.Int63n(int64(capDelay) + 1))
}

// missingPermissionAction extracts the denied IAM action from a Forbidden
// error, so a permission failure reports exactly what to add to the read
// role's policy rather than an opaque "access denied". A discoverer that
// wraps *core.Error with a "action" detail (the convention this package's
// own tests use, and the one production discoverer adapters should follow)
// yields it directly; one that does not still gets an actionable, if less
// precise, answer — the discoverer's own first required action — rather
// than nothing at all.
func missingPermissionAction(err error, d ports.ResourceDiscoverer) (string, bool) {
	if err == nil || !errors.Is(err, core.ErrForbidden) {
		return "", false
	}
	var ce *core.Error
	if errors.As(err, &ce) {
		if action, ok := ce.Details["action"].(string); ok && action != "" {
			return action, true
		}
	}
	if actions := d.RequiredActions(); len(actions) > 0 {
		return actions[0], true
	}
	return "unknown permission", true
}

func sortedRegions(set map[core.Region]bool) []core.Region {
	out := make([]core.Region, 0, len(set))
	for r := range set {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
