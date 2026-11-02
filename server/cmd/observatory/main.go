// Command observatory is the Go control plane. It opens the selected store,
// queue and blob backends, starts the worker and serves the HTTP API.
//
// Backend selection is env-driven (internal/config):
//
//	Dev mode (default, no production targets set):
//	  - SQLite embedded store   (store.OpenSQLite)
//	  - file-based durable queue (queue.NewLocalQueue)
//	  - local object store       (blob.NewLocalObjectStore)
//	  - no analytics sink
//
//	Production targets (when the reserved env vars are set — see
//	deploy/NOTES-AND-GAPS.md and docker-compose.yml prod profile):
//	  - OBS_DATABASE_URL      -> storepg.Postgres metadata store
//	  - OBS_CLICKHOUSE_DSN    -> analyticsch.ClickHouse analytics sink
//	  - OBS_NATS_URL          -> queuenats.JetStream durable queue
//	  - OBS_S3_ENDPOINT       -> blobs3.S3 object store
//
// Each production target is independent: set OBS_NATS_URL alone and you get
// the real queue with SQLite + local blobs; set them all and you get the
// full production shape. Unset targets keep their dev fallbacks, so a plain
// startup with no env vars behaves exactly as the dev mode always did.
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

	"github.com/ensembyte/observatory/internal/analyticsch"
	"github.com/ensembyte/observatory/internal/api"
	"github.com/ensembyte/observatory/internal/alertsvc"
	"github.com/ensembyte/observatory/internal/blob"
	"github.com/ensembyte/observatory/internal/blobs3"
	"github.com/ensembyte/observatory/internal/config"
	"github.com/ensembyte/observatory/internal/logx"
	"github.com/ensembyte/observatory/internal/pipeline"
	"github.com/ensembyte/observatory/internal/queue"
	"github.com/ensembyte/observatory/internal/queuenats"
	"github.com/ensembyte/observatory/internal/store"
	"github.com/ensembyte/observatory/internal/storepg"
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

	// ---- backend selection (dev fallbacks; production adapters when env is set)
	cfg := config.Load()

	// Metadata store: Postgres when OBS_DATABASE_URL is set, else SQLite.
	var st store.Engine
	var closeStore func() error
	if cfg.HasPostgres() {
		pg, err := storepg.New(ctx, cfg.PostgresDSN)
		if err != nil {
			logger.Error("open postgres", "error", err)
			os.Exit(1)
		}
		st = pg
		closeStore = pg.Close
	} else {
		dev, err := store.OpenSQLite(filepath.Join(*dataDir, "observatory.db"))
		if err != nil {
			logger.Error("open sqlite", "error", err)
			os.Exit(1)
		}
		st = dev
		closeStore = dev.Close
	}
	defer closeStore()
	if err := st.Migrate(ctx); err != nil {
		logger.Error("migrate schema", "error", err)
		os.Exit(1)
	}

	// Queue: NATS JetStream when OBS_NATS_URL is set, else the file queue.
	var q queue.Queue
	if cfg.HasNATS() {
		nq, err := queuenats.New(ctx, cfg.NATSURL, cfg.NATSStream, "obs-workers")
		if err != nil {
			logger.Error("open nats queue", "error", err)
			os.Exit(1)
		}
		q = nq
	} else {
		dev, err := queue.NewLocalQueue(filepath.Join(*dataDir, "queue"))
		if err != nil {
			logger.Error("open queue", "error", err)
			os.Exit(1)
		}
		q = dev
	}

	// Blob store: S3/MinIO when OBS_S3_ENDPOINT is set, else local dir.
	var bs blob.ObjectStore
	if cfg.HasS3() {
		s3, err := blobs3.New(ctx, blobs3.EndpointConfig{
			Endpoint:  cfg.S3Endpoint,
			AccessKey: cfg.S3AccessKey,
			SecretKey: cfg.S3SecretKey,
			Region:    cfg.S3Region,
			Bucket:    cfg.S3Bucket,
			PathStyle: cfg.S3PathStyle,
		})
		if err != nil {
			logger.Error("open s3 blob store", "error", err)
			os.Exit(1)
		}
		bs = s3
	} else {
		dev, err := blob.NewLocalObjectStore(filepath.Join(*dataDir, "blobs"))
		if err != nil {
			logger.Error("open blob store", "error", err)
			os.Exit(1)
		}
		bs = dev
	}

	// Analytics sink: ClickHouse when OBS_CLICKHOUSE_DSN is set. It mirrors
	// events/spans alongside the primary store (a composite wraps st).
	var chSink *analyticsch.ClickHouse
	if cfg.HasClickHouse() {
		chSink, err := analyticsch.New(ctx, cfg.ClickHouseDSN, logger, cfg.CHBufferMaxRows)
		if err != nil {
			logger.Error("open clickhouse", "error", err)
			os.Exit(1)
		}
		st = analyticsch.WithAnalytics(st, chSink)
		defer chSink.Close()
		if !*workerOff {
			go chSink.RunFlushLoop(ctx, cfg.CHFlushInterval)
		}
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

	logger.Info("observatory listening", "addr", *addr, "data_dir", *dataDir,
		"store", cfg.StoreName(), "queue", cfg.QueueName(),
		"blob", cfg.BlobName(), "analytics", cfg.AnalyticsName())
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
func seedDemo(ctx context.Context, st store.Engine, log *slog.Logger) error {
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
	if _, err := norm.Run(ctx, pipeline.ProjectPolicy{ID: project,
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
