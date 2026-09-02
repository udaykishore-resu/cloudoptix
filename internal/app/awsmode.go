package app

import (
	"context"
	"fmt"
	"log/slog"

	awscosting "github.com/udaykishore-resu/cloudoptix/internal/adapters/aws/costing"
	awsdiscovery "github.com/udaykishore-resu/cloudoptix/internal/adapters/aws/discovery"
	awsexecutor "github.com/udaykishore-resu/cloudoptix/internal/adapters/aws/executor"
	awsmetrics "github.com/udaykishore-resu/cloudoptix/internal/adapters/aws/metrics"
	"github.com/udaykishore-resu/cloudoptix/internal/adapters/aws/sts"
	"github.com/udaykishore-resu/cloudoptix/internal/adapters/awssim"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/infrastructure/config"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// awsSet is the complete AWS-facing adapter set for one deployment mode.
// Every field is a driven port; nothing above this file knows which mode
// produced them, which is exactly the point of resolving them all in one
// switch rather than six scattered ones.
type awsSet struct {
	broker      ports.AWSCredentialBroker
	discoverers []ports.ResourceDiscoverer
	ingestors   []ports.CostIngestor
	collectors  []ports.MetricCollector
	executors   map[optimize.ActionType]ports.Executor
	// estate is non-nil only in simulated mode.
	estate *awssim.Estate
}

// buildAWS resolves the AWS adapter set.
//
// The simulated set is seeded with awssim.BuildDemoEstate(): one estate per
// process, shared by the broker, the discoverer, the cost ingestor, the
// metric collector and every executor. Sharing one estate is what makes the
// simulation coherent — an executor that resizes an instance changes the
// same object the next discovery scan reads and the next cost ingestion
// prices, so "the estate's cost fell after the optimization ran" is a real
// observation rather than a scripted one. Giving each adapter its own copy
// would produce a simulation in which nothing anyone did ever mattered.
func buildAWS(ctx context.Context, cfg *config.Config, catalog ports.PricingCatalog, logger *slog.Logger) (*awsSet, error) {
	switch cfg.AWS.Mode {
	case config.AWSModeSimulated:
		estate := awssim.BuildDemoEstate()
		return &awsSet{
			broker: awssim.NewBroker(estate,
				cloud.ScopeRead, cloud.ScopeAnalyze, cloud.ScopePlan, cloud.ScopeExecute),
			discoverers: []ports.ResourceDiscoverer{awssim.NewDiscoverer()},
			ingestors:   []ports.CostIngestor{awssim.NewCostIngestor()},
			collectors:  []ports.MetricCollector{awssim.NewMetricCollector()},
			executors:   indexExecutors(awssim.NewExecutors()),
			estate:      estate,
		}, nil

	case config.AWSModeLive:
		base, err := sts.LoadBaseConfig(ctx)
		if err != nil {
			return nil, fmt.Errorf("app: resolving CloudOptix's own AWS identity: %w", err)
		}
		if cfg.AWS.Region != "" {
			base.Region = cfg.AWS.Region
		}
		broker := sts.NewBroker(base,
			sts.WithPrincipal("cloudoptix"),
			sts.WithSessionDuration(cfg.AWS.SessionDuration),
			sts.WithRefreshWindow(cfg.AWS.SessionDuration-cfg.AWS.SessionCacheTTL),
		)
		// set_log_retention has no working live executor; the registry
		// deliberately omits it and NewSetLogRetentionExecutor returns one
		// that fails closed with an explanation. Registering that explicitly
		// is better than leaving the action unmapped, because
		// automation.Service's "no executor registered" error says nothing
		// about *why* the action is unsupported.
		execs := indexExecutors(awsexecutor.NewExecutors())
		unsupported := awsexecutor.NewSetLogRetentionExecutor()
		execs[unsupported.Action()] = unsupported

		logger.Info("using live AWS adapters",
			slog.String("region", base.Region),
			slog.Int("discoverers", len(liveDiscoverers())),
			slog.Int("executors", len(execs)))

		return &awsSet{
			broker:      broker,
			discoverers: liveDiscoverers(),
			ingestors: []ports.CostIngestor{
				// Order matters: the orchestrator prefers the first
				// available source, and CUR is resource-level and hourly
				// where Cost Explorer is service-level and daily.
				awscosting.NewCURIngestor(),
				awscosting.NewCostExplorerIngestor(),
			},
			collectors: []ports.MetricCollector{awsmetrics.NewCollector()},
			executors:  execs,
		}, nil

	default:
		return nil, fmt.Errorf("app: unknown aws.mode %q (want %q or %q)",
			cfg.AWS.Mode, config.AWSModeLive, config.AWSModeSimulated)
	}
}

// liveDiscoverers lists one discoverer per AWS service. They are separate
// implementations rather than one multiplexed scanner so that one service
// throttling or denying a permission isolates to that service's
// ServiceScanResult instead of failing the whole scan — the partial-scan
// behaviour ports.DiscoveryRun is shaped around.
func liveDiscoverers() []ports.ResourceDiscoverer {
	return []ports.ResourceDiscoverer{
		awsdiscovery.NewEC2Discoverer(),
		awsdiscovery.NewASGDiscoverer(),
		awsdiscovery.NewRDSDiscoverer(),
		awsdiscovery.NewDynamoDBDiscoverer(),
		awsdiscovery.NewS3Discoverer(),
		awsdiscovery.NewLambdaDiscoverer(),
		awsdiscovery.NewECSDiscoverer(),
		awsdiscovery.NewEKSDiscoverer(),
		awsdiscovery.NewELBv2Discoverer(),
		awsdiscovery.NewCloudFrontDiscoverer(),
		awsdiscovery.NewAPIGatewayV2Discoverer(),
		awsdiscovery.NewElastiCacheDiscoverer(),
		awsdiscovery.NewSQSDiscoverer(),
		awsdiscovery.NewSNSDiscoverer(),
		awsdiscovery.NewEventBridgeDiscoverer(),
		awsdiscovery.NewKMSDiscoverer(),
		awsdiscovery.NewSecretsManagerDiscoverer(),
		awsdiscovery.NewResourceGroupsTaggingDiscoverer(),
	}
}

// indexExecutors keys executors by the action they perform, which is the
// shape automation.Deps.Executors requires. A duplicate action is a
// programming error in the registry, not a runtime condition, so the last
// entry simply wins rather than the function growing an error return nobody
// could act on.
func indexExecutors(list []ports.Executor) map[optimize.ActionType]ports.Executor {
	out := make(map[optimize.ActionType]ports.Executor, len(list))
	for _, e := range list {
		out[e.Action()] = e
	}
	return out
}
