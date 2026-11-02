package config

import (
	"testing"
)

// TestLoadDevDefault: no production env vars -> every subsystem stays on
// its dev fallback; only the dev/identity fields are read.
func TestLoadDevDefault(t *testing.T) {
	c := Load()
	if c.HasPostgres() {
		t.Error("HasPostgres() = true with no OBS_DATABASE_URL")
	}
	if c.HasClickHouse() {
		t.Error("HasClickHouse() = true with no OBS_CLICKHOUSE_DSN")
	}
	if c.HasNATS() {
		t.Error("HasNATS() = true with no OBS_NATS_URL")
	}
	if c.HasS3() {
		t.Error("HasS3() = true with no OBS_S3_ENDPOINT")
	}
	if c.AnyProduction() {
		t.Error("AnyProduction() = true in dev mode")
	}
	if got := c.StoreName(); got != DevStore {
		t.Errorf("StoreName() = %q, want %q", got, DevStore)
	}
	if got := c.QueueName(); got != DevQueue {
		t.Errorf("QueueName() = %q, want %q", got, DevQueue)
	}
	if got := c.BlobName(); got != DevBlob {
		t.Errorf("BlobName() = %q, want %q", got, DevBlob)
	}
	if got := c.AnalyticsName(); got != DevAnalytics {
		t.Errorf("AnalyticsName() = %q, want %q", got, DevAnalytics)
	}
}

// TestLoadProdTargets: each reserved env var independently selects its
// production adapter.
func TestLoadProdTargets(t *testing.T) {
	t.Setenv("OBS_DATABASE_URL", "postgres://u:p@h:5432/observatory")
	t.Setenv("OBS_CLICKHOUSE_DSN", "clickhouse://u:p@h:8123/observatory")
	t.Setenv("OBS_NATS_URL", "nats://h:4222")
	t.Setenv("OBS_S3_ENDPOINT", "http://h:9000")
	t.Setenv("OBS_S3_BUCKET", "observatory")

	c := Load()
	if !c.HasPostgres() || !c.HasClickHouse() || !c.HasNATS() || !c.HasS3() {
		t.Errorf("prod selection failed: pg=%v ch=%v nats=%v s3=%v",
			c.HasPostgres(), c.HasClickHouse(), c.HasNATS(), c.HasS3())
	}
	if !c.AnyProduction() {
		t.Error("AnyProduction() = false with production env set")
	}
	if c.StoreName() != ProdStore || c.QueueName() != ProdQueue ||
		c.BlobName() != ProdBlob || c.AnalyticsName() != ProdAnalytics {
		t.Errorf("selected names wrong: %s/%s/%s/%s",
			c.StoreName(), c.QueueName(), c.BlobName(), c.AnalyticsName())
	}

	// Values are carried through verbatim (what the adapters consume).
	if c.PostgresDSN == "" || c.ClickHouseDSN == "" || c.NATSURL == "" {
		t.Error("DSN/URL values not carried into Config")
	}
	// Optional S3 knobs keep their documented defaults.
	if c.S3Region != "us-east-1" {
		t.Errorf("S3Region default = %q, want us-east-1", c.S3Region)
	}
	if c.NATSStream != "observatory.events" {
		t.Errorf("NATSStream default = %q, want observatory.events", c.NATSStream)
	}
	if c.CHBufferMaxRows <= 0 {
		t.Errorf("CHBufferMaxRows = %d, want > 0", c.CHBufferMaxRows)
	}
}

// TestLoadIndependentTargets: one target set -> only that subsystem flips.
func TestLoadIndependentTargets(t *testing.T) {
	t.Setenv("OBS_NATS_URL", "nats://h:4222")
	c := Load()
	if !c.HasNATS() {
		t.Fatal("HasNATS() = false with OBS_NATS_URL set")
	}
	if c.HasPostgres() || c.HasClickHouse() || c.HasS3() {
		t.Errorf("unrelated targets selected: pg=%v ch=%v s3=%v",
			c.HasPostgres(), c.HasClickHouse(), c.HasS3())
	}

	// S3 requires endpoint AND bucket to count as configured.
	t.Setenv("OBS_S3_ENDPOINT", "http://h:9000")
	if c2 := Load(); c2.HasS3() {
		t.Error("HasS3() = true without OBS_S3_BUCKET")
	}
}
