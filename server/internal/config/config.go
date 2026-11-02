// Package config resolves every OBS_* env var the server reads, and is the
// single place that decides which backend family (dev vs production) to wire.
//
// Dev mode (default, no production env set): embedded SQLite + file queue +
// local blob dir. Production mode (any of the reserved OBS_ targets set):
// the real adapters (Postgres, ClickHouse, NATS JetStream, S3/MinIO).
package config

import (
	"os"
	"strconv"
	"time"
)

// Backend names, for log lines and the selection report.
const (
	DevStore     = "sqlite"
	ProdStore    = "postgres"
	DevQueue     = "file-queue"
	ProdQueue    = "nats-jetstream"
	DevBlob      = "local-dir"
	ProdBlob     = "s3"
	DevAnalytics = "none"
	ProdAnalytics = "clickhouse"
)

// Config is the resolved, immutable configuration for one process start.
type Config struct {
	// Dev / shared flags.
	Addr          string
	DataDir       string
	LogLevel      string
	SeedDemo      bool
	WebhookSecret string

	// Production targets; empty fields mean "not configured" -> dev adapter
	// is used for that subsystem.
	PostgresDSN     string
	ClickHouseDSN   string
	NATSURL         string
	NATSStream      string
	S3Endpoint      string
	S3Region        string
	S3Bucket        string
	S3AccessKey     string
	S3SecretKey     string
	S3PathStyle     bool

	// ClickHouse write-path tuning.
	CHFlushInterval time.Duration
	CHFlushMaxRows  int
	CHBufferMaxRows int
}

// Load reads the environment and flags into a Config.
func Load() Config {
	return Config{
		Addr:          envOr("OBS_ADDR", ":8080"),
		DataDir:       envOr("OBS_DATA_DIR", "./data"),
		LogLevel:      envOr("OBS_LOG_LEVEL", "info"),
		SeedDemo:      envOr("OBS_SEED_DEMO", "true") == "true",
		WebhookSecret: os.Getenv("OBS_WEBHOOK_SECRET"),

		PostgresDSN:  os.Getenv("OBS_DATABASE_URL"),
		ClickHouseDSN: os.Getenv("OBS_CLICKHOUSE_DSN"),
		NATSURL:      os.Getenv("OBS_NATS_URL"),
		NATSStream:   envOr("OBS_NATS_STREAM", "observatory.events"),
		S3Endpoint:   os.Getenv("OBS_S3_ENDPOINT"),
		S3Region:     envOr("OBS_S3_REGION", "us-east-1"),
		S3Bucket:     os.Getenv("OBS_S3_BUCKET"),
		S3AccessKey:  os.Getenv("OBS_S3_ACCESS_KEY"),
		S3SecretKey:  os.Getenv("OBS_S3_SECRET_KEY"),
		S3PathStyle:  envOr("OBS_S3_PATH_STYLE", "") == "true",

		CHFlushInterval: parseDuration(envOr("OBS_CH_FLUSH_INTERVAL", "2s")),
		CHFlushMaxRows:  parseInt(envOr("OBS_CH_FLUSH_MAX_ROWS", "500")),
		CHBufferMaxRows: parseInt(envOr("OBS_CH_BUFFER_MAX_ROWS", "100000")),
	}
}

// HasPostgres reports whether the Postgres metadata adapter is requested.
func (c Config) HasPostgres() bool { return c.PostgresDSN != "" }

// HasClickHouse reports whether the ClickHouse analytics adapter is requested.
func (c Config) HasClickHouse() bool { return c.ClickHouseDSN != "" }

// HasNATS reports whether the NATS JetStream queue is requested.
func (c Config) HasNATS() bool { return c.NATSURL != "" }

// HasS3 reports whether the S3 blob adapter is requested.
func (c Config) HasS3() bool { return c.S3Endpoint != "" && c.S3Bucket != "" }

// AnyProduction reports whether at least one production backend is configured.
func (c Config) AnyProduction() bool {
	return c.HasPostgres() || c.HasClickHouse() || c.HasNATS() || c.HasS3()
}

// StoreName returns the selected store name (for logging).
func (c Config) StoreName() string {
	if c.HasPostgres() {
		return ProdStore
	}
	return DevStore
}

// QueueName returns the selected queue name.
func (c Config) QueueName() string {
	if c.HasNATS() {
		return ProdQueue
	}
	return DevQueue
}

// BlobName returns the selected blob name.
func (c Config) BlobName() string {
	if c.HasS3() {
		return ProdBlob
	}
	return DevBlob
}

// AnalyticsName returns the selected analytics name.
func (c Config) AnalyticsName() string {
	if c.HasClickHouse() {
		return ProdAnalytics
	}
	return DevAnalytics
}

// envOr returns the value of key, or def when the value is empty.
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func parseDuration(s string) time.Duration {
	d, err := time.ParseDuration(s)
	if err != nil {
		return 2 * time.Second
	}
	return d
}

func parseInt(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return 500
	}
	return n
}
