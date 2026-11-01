# Known Limitations

Stated plainly so reviewers do not mistake a documented approximation for a
bug, and so the follow-up work is findable.

## 1. Evaluation runner pairs dataset cases with the *newest ingested
completed runs* — a documented approximation

`api.createEvaluation` (`server/internal/api/evaluations.go`) does **not**
execute a fresh agent run per dataset case. It lists the project's most
recent completed runs for the requested `agent_name`/`agent_version`/`model`
and positions them against the dataset's cases (case *i* ↔ run *i*), then
scores each with the deterministic checks.

- **What this means:** the "evaluation" scores *already-ingested* runs, not a
  live execution. It is a comparison harness over past telemetry.
- **Approximation:** the case↔run pairing is by list position, not by an
  explicit `case_id` link on the run. If a dataset has more cases than
  ingested runs, the missing cases are scored as `"error"` (no matching run)
  — the runner does not guess. When there are more runs than cases, the extra
  runs are ignored.
- **Why it's acceptable for MVP:** it satisfies the DoD item "an evaluation
  dataset can compare two agent versions" and keeps scoring reproducible. It
  is not a substitute for a real eval-execute loop.
- **Follow-up:** emit an explicit case link from the instrumented run
  (`eval.case_id` attribute, already in the attribute namespace) and have the
  runner join on it; add a worker that actually executes the case's task.

## 2. Replay isolation is *documented, not provisioned*

`evalsvc.MockTool` is the default and only executor the replay path calls.
Its isolation boundary (no outbound sockets, no production credentials,
destructive deny-list) is enforced in code and documented. But:

- There is **no sandbox/container isolation** (Firecracker / isolated
  container) provisioned. "Isolation" here means *mocked executor*, not an
  OS-level boundary. A live (non-mock) executor would need explicit
  configuration, an allowlist, and dry-run checks (spec FR-06) — that wiring
  is not present.
- The destructive deny-list is a fixed default
  (`payment.*`, `email.send`, `file.delete`, `db.write/delete`,
  `external.mutate`). New destructive tools added to a deployment are not
  automatically covered; operators must add them to `MockTool.Destructive` or
  rely on the generic refusal for unknown side effects.
- **Follow-up:** provision an actual sandbox and extend the deny-list policy
  to be project-configurable.

## 3. Demo key and seeded sample data are labelled *sample*, never real analytics

`cmd/observatory` seeds, on first launch (`OBS_SEED_DEMO=true`), a `demo`
project with the key `demo-key`, a sample dataset, and a synthetic
`SampleLoopRun` run. Every seeded event carries `sample: "true"` in
`attributes` (spec: demo data labelled as sample).

- `demo-key` is a **development placeholder, not a credential.** It is
  hardcoded, unrotated, and only valid in the embedded dev store. Do not use
  it against any real deployment.
- The seeded run and dataset exist so every UI workspace has structure on
  first launch. They must never be shown as real production analytics; the
  `sample` attribute is the marker the UI and any export must respect.
- **Follow-up:** gate the demo key behind an explicit flag, and add a
  "this is sample data" surfacing rule in the frontend + any analytics
  export.

## 4. Default binary is dev-path only (production backends not wired in)

`cmd/observatory` unconditionally uses embedded SQLite, the file queue, and
the local blob dir. The Postgres/ClickHouse/NATS/S3 targets exist as
`infrastructure/` DDL + build-tagged reference code (`internal/queue/nats.go`
is `//go:build nats`) and a `store.Engine` interface, but the running binary
does not switch to them from env yet. The `PRODUCTION_OBS_*` compose vars are
documented placeholders. See `deploy/NOTES-AND-GAPS.md` (G2, G5) and
`docs/ARCHITECTURE.md` (env selection).

## 5. `server/go.sum` is absent from the tree

`server/go.mod` requires `modernc.org/sqlite` but there is no `go.sum`.
`go mod tidy` (which belongs to the head owning `server/go.mod`/`go.sum`)
must be run before the `server/Dockerfile` (`go mod download`) smoke test
works on a clean checkout.

## 6. Detectors are single-run, not cross-run, in the default path

The rule engine evaluates each run's own rows. Cross-run failure
*clustering* (spec 8.4) is achieved by persisting a per-finding
`failure_signature` into `failure_signatures` and aggregating there, but the
detectors themselves do not yet do a rolling-window cross-run sweep. Budget
absolutes (`DetectorConfig.Budget*`) default to 0 (rely on SDK budget events)
and are not wired to per-project overrides.
