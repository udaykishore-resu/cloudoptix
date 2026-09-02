package app

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/adapters/memstore"
	"github.com/udaykishore-resu/cloudoptix/internal/adapters/postgres"
	"github.com/udaykishore-resu/cloudoptix/internal/adapters/redisstore"
	"github.com/udaykishore-resu/cloudoptix/internal/infrastructure/config"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// storageSet is what buildStorage resolves to: the repository bundle, the
// unit of work, a readiness probe and a closer. The probe is carried here
// rather than reconstructed in health.go because only this function knows
// whether there is a database to ping at all.
type storageSet struct {
	repos ports.Repositories
	uow   ports.UnitOfWork
	// mem is non-nil on the memory path. Tests reach for it to snapshot and
	// reset state between cases.
	mem   *memstore.Store
	db    *postgres.DB
	probe func(ctx context.Context) error
	close func() error
}

// buildStorage selects the persistence backend. On the Postgres path it
// applies the embedded migrations before returning: a process that opened a
// pool against a schema it does not recognise will fail on its first query
// with a column-not-found error thousands of lines from the cause, so the
// schema check belongs at startup where the message can name the actual
// problem.
func buildStorage(ctx context.Context, cfg *config.Config, logger *slog.Logger) (*storageSet, error) {
	switch cfg.Storage {
	case config.StorageMemory:
		store := memstore.New()
		return &storageSet{
			repos: store.Repositories(),
			uow:   store,
			mem:   store,
			probe: func(context.Context) error { return nil },
			close: func() error { return nil },
		}, nil

	case config.StoragePostgres:
		db, err := postgres.Open(ctx, postgres.Config{
			DSN:             cfg.Database.DSN(),
			MaxConns:        int32(cfg.Database.MaxOpenConns),
			MinConns:        int32(cfg.Database.MaxIdleConns),
			MaxConnLifetime: cfg.Database.ConnMaxLifetime,
		}, logger)
		if err != nil {
			return nil, fmt.Errorf("app: opening postgres at %s:%d/%s: %w",
				cfg.Database.Host, cfg.Database.Port, cfg.Database.Name, err)
		}
		migrateCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		applied, err := postgres.NewMigrator(db).Up(migrateCtx)
		if err != nil {
			db.Close()
			return nil, fmt.Errorf("app: applying migrations: %w", err)
		}
		if len(applied) > 0 {
			logger.Info("applied database migrations",
				slog.Int("count", len(applied)), slog.Any("versions", applied))
		}
		return &storageSet{
			repos: postgres.New(db),
			uow:   postgres.NewUnitOfWork(db),
			db:    db,
			probe: db.HealthCheck,
			close: func() error { db.Close(); return nil },
		}, nil

	default:
		return nil, fmt.Errorf("app: unknown storage %q (want %q or %q)",
			cfg.Storage, config.StorageMemory, config.StoragePostgres)
	}
}

// cacheSet is the resolved ports.Cache / ports.Locker pair.
type cacheSet struct {
	cache  ports.Cache
	locker ports.Locker
	probe  func(ctx context.Context) error
	close  func() error
}

// buildCache selects the cache and distributed-lock backend. The two are
// resolved together, not independently, because they must agree: an
// in-process lock beside a shared cache would let two replicas each believe
// they hold the same execution lease while reading each other's cached
// plans, which is worse than either choice made consistently.
func buildCache(ctx context.Context, cfg *config.Config) (*cacheSet, error) {
	switch cfg.Cache {
	case config.CacheMemory:
		store := memstore.New()
		return &cacheSet{
			cache:  store.Cache(),
			locker: store.Locker(),
			probe:  func(context.Context) error { return nil },
			close:  func() error { return nil },
		}, nil

	case config.CacheRedis:
		client, err := redisstore.Open(redisstore.Config{
			Addrs:        cfg.Redis.Addrs,
			Password:     cfg.Redis.Password.Value(),
			DB:           cfg.Redis.DB,
			DialTimeout:  cfg.Redis.DialTimeout,
			ReadTimeout:  cfg.Redis.ReadTimeout,
			WriteTimeout: cfg.Redis.WriteTimeout,
			PoolSize:     cfg.Redis.PoolSize,
			TLSEnabled:   cfg.Redis.TLSEnabled,
			KeyPrefix:    "cloudoptix:" + cfg.Environment,
		})
		if err != nil {
			return nil, fmt.Errorf("app: configuring redis: %w", err)
		}
		pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if err := client.Ping(pingCtx); err != nil {
			_ = client.Close()
			return nil, fmt.Errorf("app: redis at %v is unreachable: %w", cfg.Redis.Addrs, err)
		}
		return &cacheSet{
			cache: client.Cache(), locker: client.Locker(),
			probe: client.Ping, close: client.Close,
		}, nil

	default:
		return nil, fmt.Errorf("app: unknown cache %q (want %q or %q)",
			cfg.Cache, config.CacheMemory, config.CacheRedis)
	}
}
