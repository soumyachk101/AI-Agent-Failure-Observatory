// Command observatory is the Go control plane. It opens the embedded dev
// store, starts the worker and serves the HTTP API.
//
// Dev-mode fallbacks (see docs/architecture.md):
//   - SQLite embedded store   (store.OpenSQLite)
//   - file-based durable queue (queue.NewLocalQueue)
//   - local object store       (blob.NewLocalObjectStore)
//
// Production targets (ClickHouse / Postgres / NATS JetStream / S3) are
// provided as migration code in infrastructure/ and behind build tags; they
// are optional and not required to run locally.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/ensembyte/observatory/internal/api"
	"github.com/ensembyte/observatory/internal/alertsvc"
	"github.com/ensembyte/observatory/internal/blob"
	"github.com/ensembyte/observatory/internal/logx"
	"github.com/ensembyte/observatory/internal/pipeline"
	"github.com/ensembyte/observatory/internal/queue"
	"github.com/ensembyte/observatory/internal/store"
)

func main() {
	var (
		addr       = flag.String("addr", envOr("OBS_ADDR", ":8080"), "listen address")
		dataDir    = flag.String("data-dir", envOr("OBS_DATA_DIR", "./data"), "embedded data directory")
		logLevel   = flag.String("log-level", envOr("OBS_LOG_LEVEL", "info"), "log level")
		seedDemo   = flag.Bool("seed-demo", envOr("OBS_SEED_DEMO", "true") == "true", "seed a demo project + sample run")
		webhookSec = flag.String("webhook-secret", envOr("OBS_WEBHOOK_SECRET", ""), "HMAC secret for signed webhooks (empty in dev)")
		workerOff  = flag.Bool("no-worker", false, "disable the background queue worker")
	)
	flag.Parse()

	logger := logx.NewLogger(os.Stderr, parseLevel(*logLevel), nil).
		With("component", "observatory")

	if err := os.MkdirAll(*dataDir, 0o755); err != nil {
		logger.Error("create data dir", "error", err)
		os.Exit(1)
	}

	ctx, cancel := signalContext()
	defer cancel()

	st, err := store.OpenSQLite(filepath.Join(*dataDir, "observatory.db"))
	if err != nil {
		logger.Error("open sqlite", "error", err)
		os.Exit(1)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		logger.Error("migrate schema", "error", err)
		os.Exit(1)
	}

	q, err := queue.NewLocalQueue(filepath.Join(*dataDir, "queue"))
	if err != nil {
		logger.Error("open queue", "error", err)
		os.Exit(1)
	}
	bs, err := blob.NewLocalObjectStore(filepath.Join(*dataDir, "blobs"))
	if err != nil {
		logger.Error("open blob store", "error", err)
		os.Exit(1)
	}

	app := api.New(st, q, bs, logger)
	app.SigningSecret = *webhookSec

	if *seedDemo {
		if err := seedDemo(ctx, st, logger); err != nil {
			logger.Warn("seed demo", "error", err)
		}
	}

	if !*workerOff {
		go app.Worker.Run(ctx)
		go runAlertScheduler(ctx, app)
	}

	srv := &http.Server{
		Addr:              *addr,
		Handler:           api.NewRouter(app).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()

	logger.Info("observatory listening", "addr", *addr, "data_dir", *dataDir)
	select {
	case <-ctx.Done():
		logger.Info("shutting down")
		shutdownCtx, scancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer scancel()
		_ = srv.Shutdown(shutdownCtx)
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server", "error", err)
			os.Exit(1)
		}
	}
}

// runAlertScheduler periodically evaluates alert rules for every project.
func runAlertScheduler(ctx context.Context, app *api.App) {
	t := time.NewTicker(60 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			svc := alertsvc.New(app.Store)
			svc.SigningSecret = app.SigningSecret
			projects, err := app.Store.ListProjects(ctx)
			if err != nil {
				continue
			}
			for _, p := range projects {
				if _, err := svc.Evaluate(ctx, p.ID, now.Add(-10*time.Minute)); err != nil {
					app.Log.Warn("alert evaluation", "project", p.ID, "error", err)
				}
			}
		}
	}
}

// seedDemo creates a demo project + key, a sample dataset and a sample run
// with a known tool loop so every workspace has real data on first launch.
// All seeded data is explicitly labelled sample (spec: demo data labelled).
func seedDemo(ctx context.Context, st *store.Store, log *slog.Logger) error {
	const project = "demo"
	if err := st.CreateProject(ctx, project, "Demo Agent Project", "dev"); err != nil {
		return err
	}
	if _, ok, _ := st.LookupProjectForKey(ctx, "demo-key"); !ok {
		if err := st.CreateAPIKey(ctx, project, "demo", "demo-key"); err != nil {
			return err
		}
	}

	// Sample dataset + cases (evaluation workspace has structure).
	_ = st.UpsertDataset(ctx, &store.Dataset{
		ID: "ds-demo", Project: project, Name: "Demo Tasks", Version: "v1",
		Description: "Sample evaluation dataset", CaseCount: 3,
	})
	_ = st.UpsertCase(ctx, project, "case-lookup", "ds-demo",
		"Look up an order by id", "order.lookup", true,
		`{"rubric":"correct tool, completion"}`)
	_ = st.UpsertCase(ctx, project, "case-retry", "ds-demo",
		"Retry a flaky call then succeed", "order.refund", true,
		`{"rubric":"no retry storm"}`)
	_ = st.UpsertCase(ctx, project, "case-ctx", "ds-demo",
		"Answer with retrieval evidence", "retrieval.search", true,
		`{"rubric":"no truncation"}`)

	// Sample run: ingested through the pipeline so dedup/assemble/detect
	// all run on real data.
	pp, err := st.GetProject(ctx, project)
	if err != nil {
		return err
	}
	norm := &pipeline.Normalizer{Store: st}
	sample := pipeline.SampleLoopRun(project)
	if _, err := norm.Run(ctx, pipeline.ProjectPolicy{ID: project.ID,
		PayloadCapture: pp.PayloadCapture,
		RedactFields:   pipeline.SplitFields(pp.RedactFields)},
		sample); err != nil {
		log.Warn("seed sample run", "error", err)
	}
	return nil
}

// signalContext returns a context cancelled on SIGINT/SIGTERM.
func signalContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		s := make(chan os.Signal, 1)
		signal.Notify(s, os.Interrupt, syscall.SIGTERM)
		<-s
		cancel()
	}()
	return ctx, cancel
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func parseLevel(s string) slog.Leveler {
	switch s {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
