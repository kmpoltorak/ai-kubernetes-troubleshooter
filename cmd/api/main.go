// Command api runs the AI Kubernetes Troubleshooter HTTP server.
//
// Usage:
//
//	api                 start the server (applies pending migrations first)
//	api migrate up      apply pending migrations
//	api migrate down    roll back the latest migration
//	api purge --before RFC3339 [--limit 100] [--confirm TOKEN]
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/api"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/config"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/incidents"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/investigation"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/jobs"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/kube"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/llm"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/observability"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/redact"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/simulation"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/storage"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", redact.Text(err.Error()))
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) > 0 && args[0] == "purge" {
		return purgeCommand(args[1:], os.Getenv("DATABASE_URL"), os.Stdout)
	}
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}
	log := observability.NewLogger(os.Stdout, cfg.LogLevel)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	connectCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	store, err := storage.Connect(connectCtx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer store.Close()

	if len(args) > 0 {
		return migrateCommand(ctx, store, args, log)
	}
	n, err := store.MigrateUp(ctx)
	if err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	log.Info("database ready", "migrations_applied", n)

	checks := map[string]func(context.Context) error{"database": store.Ping}
	if cfg.Auth.Enabled {
		checks["authentication"] = func(ctx context.Context) error { return api.AuthenticationReady(ctx, cfg.Auth) }
	}
	var clusters investigation.Clusters
	var live api.Clusters
	if cfg.SimulationEnabled {
		sim, err := simulation.NewClusters(cfg.SimulationScenario)
		if err != nil {
			return fmt.Errorf("SIMULATION_SCENARIO: %w", err)
		}
		sim.CustomResources = kube.CustomResourceMap(cfg.CustomResources)
		clusters = sim
	} else {
		connected := kube.Live{}
		for _, c := range cfg.Clusters {
			cluster, err := kube.Connect(c.Kubeconfig, c.Context, cfg.CustomResources)
			if err != nil {
				return fmt.Errorf("cluster %s: %w", c.Name, err)
			}
			connected[c.Name] = cluster
		}
		clusters, live = connected, connected
		checks["kubernetes"] = connected.Ready
	}

	runtime := api.RuntimeFrom(cfg)
	policy := runtime.Policy()
	engine := investigation.NewEngine(store, clusters, llm.FromConfig(cfg), log, investigation.Config{
		Timeout:       cfg.InvestigationTimeout,
		MaxConcurrent: cfg.MaxConcurrentInvestigations,
		Policy:        policy,
	})

	scheduler := jobs.New(store, engine, log, jobs.Config{MaxConcurrent: cfg.MaxConcurrentInvestigations, Capacity: cfg.JobQueueCapacity, Timeout: cfg.JobTimeout})
	workerCtx, stopWorkers := context.WithCancel(ctx)
	workersDone := make(chan struct{})
	go func() { defer close(workersDone); scheduler.Run(workerCtx) }()
	defer func() {
		stopWorkers()
		select {
		case <-workersDone:
		case <-time.After(15 * time.Second):
			log.Error("worker shutdown timed out; leases will expire")
		}
	}()

	srv := &http.Server{
		Addr:              ":" + strconv.Itoa(cfg.HTTPPort),
		Handler:           api.New(incidents.NewService(store, policy), scheduler, checks, log, cfg.RateLimitRPS, cfg.Auth, runtime, live),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		// The compatibility endpoint waits through queueing and execution.
		WriteTimeout:   cfg.JobTimeout + 15*time.Second,
		IdleTimeout:    60 * time.Second,
		MaxHeaderBytes: 64 << 10,
		ErrorLog:       slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("server starting", "addr", srv.Addr, "config", cfg.String())
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	if err := <-errCh; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	log.Info("server stopped")
	return nil
}

func migrateCommand(ctx context.Context, store *storage.Store, args []string, log *slog.Logger) error {
	if len(args) != 2 || args[0] != "migrate" {
		return errors.New("usage: api [migrate up|down]")
	}
	switch args[1] {
	case "up":
		n, err := store.MigrateUp(ctx)
		if err == nil {
			log.Info("migrations applied", "count", n)
		}
		return err
	case "down":
		err := store.MigrateDown(ctx)
		if err == nil {
			log.Info("rolled back latest migration")
		}
		return err
	}
	return errors.New("usage: api [migrate up|down]")
}
