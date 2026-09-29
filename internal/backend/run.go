package backend

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/saidovux/remna-quota/internal/aggregate"
	"github.com/saidovux/remna-quota/internal/bundle"
	"github.com/saidovux/remna-quota/internal/bundlestore"
	"github.com/saidovux/remna-quota/internal/httpapi"
	"github.com/saidovux/remna-quota/internal/providers"
)

func Run(ctx context.Context, cfg Config) error {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	var repo bundle.Repository
	var provider bundle.Provider
	var aggregationRepo aggregate.Repository
	var reader aggregate.Provider
	if cfg.Provider == "demo" {
		repo = bundle.NewMemoryRepository()
		provider = providers.NewDemo()
		aggregationRepo = aggregate.NewMemoryRepository()
		reader = providers.NewDemoReferences()
		slog.Warn("demo_mode", "storage", "ephemeral", "connections", "non_functional_examples")
	} else {
		var err error
		provider, err = providers.NewRemnawave(cfg.providerConfig())
		if err != nil {
			return errors.New("invalid Remnawave provider configuration")
		}
		startCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
		db, err := bundlestore.Open(startCtx, cfg.DatabaseURL)
		cancel()
		if err != nil {
			return err
		}
		defer db.Close()
		repo = db
		aggregationRepo = db.Aggregates()
		reader, err = providers.NewRemnawaveReferences(cfg.providerConfig())
		if err != nil {
			return errors.New("invalid subscription reader configuration")
		}
	}
	var managed *bundle.Service
	var managedHTTP httpapi.Service
	if cfg.EnableManagedAccounts {
		managed = bundle.NewService(repo, map[string]bundle.Provider{cfg.Provider: provider})
		managed.Configure(cfg.SyncInterval)
		defer managed.Close()
		managedHTTP = managed
	}
	aggregation := aggregate.NewService(aggregationRepo, map[string]aggregate.Provider{cfg.Provider: reader})
	handler, err := httpapi.New(managedHTTP, aggregationRepo, httpapi.Config{Aggregation: aggregation, APIKey: cfg.APIKey, PublicURL: cfg.PublicURL, StaleAfter: max(2*cfg.SyncInterval, 2*time.Minute)})
	if err != nil {
		return err
	}
	server := &http.Server{Addr: cfg.ListenAddr, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 120 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	workerCtx, cancelWorker := context.WithCancel(ctx)
	defer cancelWorker()
	workerDone := make(chan struct{})
	var workers sync.WaitGroup
	if managed != nil {
		workers.Add(1)
		go func() {
			defer workers.Done()
			ticker := time.NewTicker(time.Second)
			defer ticker.Stop()
			for {
				if err := managed.ScheduleDue(workerCtx); err != nil && workerCtx.Err() == nil {
					slog.Warn("reconcile_schedule_failed")
				}
				select {
				case <-workerCtx.Done():
					return
				case <-ticker.C:
				}
			}
		}()
	}
	workers.Add(1)
	go func() {
		defer workers.Done()
		ticker := time.NewTicker(cfg.SyncInterval)
		defer ticker.Stop()
		for {
			if err := aggregation.RefreshAll(workerCtx); err != nil && workerCtx.Err() == nil {
				slog.Warn("subscription_refresh_incomplete")
			}
			select {
			case <-workerCtx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	go func() { workers.Wait(); close(workerDone) }()
	errCh := make(chan error, 1)
	go func() {
		slog.Info("backend_started", "listen_addr", cfg.ListenAddr, "provider", cfg.Provider)
		errCh <- server.ListenAndServe()
	}()
	var serveErr error
	select {
	case <-ctx.Done():
	case serveErr = <-errCh:
	}
	cancelWorker()
	if managed != nil {
		managed.Cancel()
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		_ = server.Close()
	}
	select {
	case <-workerDone:
	case <-shutdownCtx.Done():
	}
	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		return errors.New("HTTP server failed")
	}
	return nil
}
