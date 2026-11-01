module github.com/ensembyte/observatory

go 1.27

// The only external dependency: pure-Go SQLite driver for embedded dev mode.
// Production drivers (ClickHouse/Postgres/NATS) are optional and live behind
// build tags with their own module requirements; see internal/store/docs.
require modernc.org/sqlite v1.29.10
