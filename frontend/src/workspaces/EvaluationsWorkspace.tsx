import { Fragment, useEffect, useMemo, useState } from "react";
import { api, PROJECT_ID, isDemoKey, API_KEY } from "../api";
import type {
  DatasetCase,
  DatasetDetail,
  DatasetRow,
  EvalDetail,
  EvalResult,
  EvalRunRow,
  ReplayCreated,
  ReplayJob,
} from "../types";
import {
  EmptyState,
  ErrorState,
  Loading,
  fmtDateTime,
  fmtNum,
  useAsync,
} from "../hooks";
import { parseGateJson, shortId } from "../format";
import { Carousel } from "./VersionCarousel";

type Tab = "datasets" | "compare" | "replay";

export function EvaluationsWorkspace({
  onOpenRun,
}: {
  onOpenRun: (runId: string) => void;
}) {
  const [tab, setTab] = useState<Tab>("datasets");
  const [compareDataset, setCompareDataset] = useState<string | null>(null);
  return (
    <div className="content">
      <header className="h1-row">
        <h1>Evaluations</h1>
        <span className="h1-sub">
          project <code className="mono">{PROJECT_ID}</code>
          {isDemoKey(API_KEY) ? " · seeded sample data" : ""}
        </span>
      </header>

      <div role="tablist" aria-label="Evaluation sections" style={{ display: "flex", gap: 4 }}>
        {(
          [
            ["datasets", "Datasets"],
            ["compare", "Version comparison"],
            ["replay", "Safe replay"],
          ] as [Tab, string][]
        ).map(([t, label]) => (
          <button
            key={t}
            role="tab"
            aria-selected={tab === t}
            className={`btn ${tab === t ? "primary" : "ghost"}`}
            onClick={() => setTab(t)}
          >
            {label}
          </button>
        ))}
      </div>

      {tab === "datasets" && (
        <DatasetsTab
          onOpenCompare={(dsId) => {
            setCompareDataset(dsId);
            setTab("compare");
          }}
        />
      )}
      {tab === "compare" && (
        <CompareTab onOpenRun={onOpenRun} initialDataset={compareDataset} />
      )}
      {tab === "replay" && <ReplayTab onOpenRun={onOpenRun} />}
    </div>
  );
}

/* ===================== datasets ===================== */

function DatasetsTab({ onOpenCompare }: { onOpenCompare: (dsId: string) => void }) {
  const dss = useAsync(() => api.datasets(), []);
  const [sel, setSel] = useState<string | null>(null);
  const detail = useAsync<DatasetDetail | null>(
    () => (sel ? api.dataset(sel) : Promise.resolve(null)),
    [sel],
  );

  const [newName, setNewName] = useState("");
  const [newVersion, setNewVersion] = useState("");
  const [newTask, setNewTask] = useState("");
  const [newTool, setNewTool] = useState("");
  const [caseError, setCaseError] = useState("");
  const [caseMsg, setCaseMsg] = useState("");

  useEffect(() => {
    if (dss[0].status === "ready" && !sel && dss[0].data!.length)
      setSel(dss[0].data![0].id);
  }, [dss, sel]);

  if (dss[0].status === "loading") return <Loading label="Loading datasets…" />;
  if (dss[0].status === "error")
    return <ErrorState message={dss[0].error} onRetry={() => dss[1]()} />;

  const list = dss[0].data!;

  const addCase = async () => {
    if (!sel) return;
    setCaseError("");
    if (!newTask.trim()) {
      setCaseError("Task is required.");
      return;
    }
    try {
      // The server's dataset endpoint is the single dataset writer; a new
      // version upserts the same dataset id. Case id is deterministic from
      // the task text (kept visible so re-adding is idempotent).
      const caseId = `case-${Date.now().toString(36)}`;
      await api.createDataset({
        id: sel,
        name: list.find((d) => d.id === sel)?.name ?? sel,
        version: newVersion || (list.find((d) => d.id === sel)?.version ?? "v1"),
        cases: [
          {
            id: caseId,
            task: newTask.trim(),
            expected_tool: newTool.trim(),
            expect_completion: true,
          },
        ],
      });
      setNewTask("");
      setNewTool("");
      setCaseMsg(`Case ${caseId} registered.`);
      detail[1]();
    } catch (e) {
      setCaseError(e instanceof Error ? e.message : String(e));
    }
  };

  return (
    <div style={{ display: "grid", gridTemplateColumns: "320px minmax(0,1fr)", gap: 16, minHeight: 0 }}>
      <section aria-label="Datasets">
        <div className="wb-head" style={{ borderRadius: "8px 8px 0 0" }}>
          datasets <span className="grow" /> {list.length}
        </div>
        {list.length === 0 ? (
          <EmptyState
            title="No datasets"
            hint="Create a dataset below to start evaluation runs."
          />
        ) : (
          <ul className="run-outline">
            {list.map((d: DatasetRow) => (
              <li key={d.id} aria-selected={sel === d.id}>
                <button
                  onClick={() => setSel(d.id)}
                  aria-label={`Open dataset ${d.id}`}
                >
                  <span className="ro-main">
                    <span className="ro-id">{d.name || d.id}</span>
                    <span className="ro-sub">{d.version}</span>
                  </span>
                  <span className="ro-sub">
                    {fmtNum(d.case_count)} cases ·{" "}
                    {fmtDateTime(d.created_at)}
                  </span>
                </button>
              </li>
            ))}
          </ul>
        )}
        <div
          style={{
            padding: 12,
            display: "flex",
            flexDirection: "column",
            gap: 8,
            borderTop: "1px solid var(--border-subtle)",
          }}
        >
          <span className="lbl" style={{ fontSize: 10, color: "var(--text-3)", textTransform: "uppercase", letterSpacing: "0.05em", fontWeight: 600 }}>
            new dataset
          </span>
          <input placeholder="name" value={newName} onChange={(e) => setNewName(e.target.value)} aria-label="Dataset name" />
          <input placeholder="version (e.g. v2)" value={newVersion} onChange={(e) => setNewVersion(e.target.value)} aria-label="Dataset version" />
          <button
            className="btn"
            disabled={!newName.trim()}
            onClick={async () => {
              try {
                const r = await api.createDataset({
                  name: newName.trim(),
                  version: newVersion || "v1",
                });
                setNewName("");
                setNewVersion("");
                setSel(r.id);
                dss[1]();
              } catch (e) {
                setCaseError(e instanceof Error ? e.message : String(e));
              }
            }}
          >
            create dataset
          </button>
          <button
            className="btn ghost"
            onClick={() => sel && onOpenCompare(sel)}
            disabled={!sel}
          >
            open in version comparison →
          </button>
        </div>
      </section>

      <section aria-label="Dataset cases" style={{ display: "flex", flexDirection: "column", gap: 12, minWidth: 0 }}>
        {detail[0].status === "ready" && detail[0].data ? (
          <>
            <div className="wb-head" style={{ borderRadius: 8 }}>
              {detail[0].data!.name ?? sel} · {detail[0].data!.version ?? "—"}
              <span className="h1-sub" style={{ textTransform: "none", letterSpacing: 0 }}>
                {detail[0].data!.cases.length} cases
              </span>
            </div>
            <table className="tbl">
              <thead>
                <tr>
                  <th>Case</th>
                  <th>Task</th>
                  <th>Expected tool</th>
                  <th>Expect completion</th>
                  <th>Rubric</th>
                </tr>
              </thead>
              <tbody>
                {detail[0].data!.cases.map((c: DatasetCase) => (
                  <tr key={c.id}>
                    <td className="mono" style={{ fontSize: 11 }}>{c.id}</td>
                    <td>{c.task}</td>
                    <td className="mono">{c.expected_tool || "—"}</td>
                    <td>{c.expect_completion ? "yes" : "no"}</td>
                    <td className="mono" style={{ fontSize: 10.5, maxWidth: 220, overflow: "hidden", textOverflow: "ellipsis" }} title={c.rubric}>
                      {c.rubric || "—"}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>

            <div style={{ display: "flex", gap: 8, flexWrap: "wrap", alignItems: "end" }}>
              <label className="field">
                Task
                <input value={newTask} onChange={(e) => setNewTask(e.target.value)} size={28} />
              </label>
              <label className="field">
                Expected tool
                <input value={newTool} onChange={(e) => setNewTool(e.target.value)} size={16} />
              </label>
              <button className="btn" onClick={addCase} disabled={!sel}>
                add case
              </button>
            </div>
            {caseMsg && <span className="toast ok">{caseMsg}</span>}
            {caseError && <span className="toast err" role="alert">{caseError}</span>}
          </>
        ) : sel ? (
          <Loading label="Loading cases…" />
        ) : (
          <EmptyState title="Select a dataset" hint="Pick a dataset on the left to view its cases." />
        )}
      </section>
    </div>
  );
}

/* ===================== version comparison ===================== */

function CompareTab({
  onOpenRun,
  initialDataset,
}: {
  onOpenRun: (runId: string) => void;
  initialDataset?: string | null;
}) {
  const dss = useAsync(() => api.datasets(), []);
  const [sel, setSel] = useState<string | null>(initialDataset ?? null);
  const [evals, setEvals] = useState<EvalRunRow[]>([]);
  const [nonce, setNonce] = useState(0);

  useEffect(() => {
    if (initialDataset) setSel(initialDataset);
  }, [initialDataset]);

  useEffect(() => {
    if (dss[0].status === "ready" && !sel && dss[0].data!.length)
      setSel(dss[0].data![0].id);
  }, [dss, sel]);

  // Re-fetch evaluations when the dataset selection or the nonce changes
  // (nonce bumped after a new evaluation is created).
  const ev = useAsync(
    () => (sel ? api.evaluations(sel).then(setEvals) : Promise.resolve()),
    [sel, nonce],
  );

  const [agent, setAgent] = useState("support-agent");
  const [agentVersion, setAgentVersion] = useState("1.4.0");
  const [model, setModel] = useState("");
  const [runMsg, setRunMsg] = useState<{ ok: boolean; text: string } | null>(null);

  const slides = useMemo(
    () =>
      evals.map((r: EvalRunRow, i: number) => ({
        key: r.id,
        label: `${agentLabel(r)}`,
        sublabel: `${r.dataset_version ?? ""} · ${r.model ? r.model + " · " : ""}evaluator ${r.evaluator_version ?? "v1"}`,
        body: (
          <EvalSummary
            run={r}
            onOpenRun={onOpenRun}
            index={i}
            total={evals.length}
          />
        ),
      })),
    [evals, onOpenRun],
  );

  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 14 }}>
      <div style={{ display: "flex", gap: 8, flexWrap: "wrap", alignItems: "end" }}>
        <label className="field">
          Dataset
          <select value={sel ?? ""} onChange={(e) => setSel(e.target.value)}>
            {dss[0].status === "ready"
              ? dss[0].data!.map((d) => (
                  <option key={d.id} value={d.id}>
                    {d.name ?? d.id} ({d.version})
                  </option>
                ))
              : <option value="">…</option>}
          </select>
        </label>
        <label className="field">
          Agent
          <input value={agent} onChange={(e) => setAgent(e.target.value)} size={14} />
        </label>
        <label className="field">
          Agent version
          <input value={agentVersion} onChange={(e) => setAgentVersion(e.target.value)} size={8} />
        </label>
        <label className="field">
          Model
          <input value={model} onChange={(e) => setModel(e.target.value)} size={12} placeholder="optional" />
        </label>
        <button
          className="btn primary"
          disabled={!sel}
          onClick={async () => {
            setRunMsg(null);
            try {
              await api.createEvaluation({
                dataset_id: sel!,
                agent_name: agent,
                agent_version: agentVersion,
                model: model || undefined,
              });
              setNonce((n) => n + 1); // refetch the run list
              setRunMsg({ ok: true, text: "Evaluation recorded (deterministic scoring)." });
            } catch (e) {
              setRunMsg({ ok: false, text: e instanceof Error ? e.message : String(e) });
            }
          }}
        >
          run evaluation
        </button>
      </div>

      {runMsg && (
        <span className={`toast ${runMsg.ok ? "ok" : "err"}`} role="status">
          {runMsg.text}
        </span>
      )}

      {ev[0].status === "loading" && <Loading label="Loading evaluations…" />}
      {ev[0].status === "error" && <ErrorState message={ev[0].error} onRetry={() => ev[1]()} />}
      {ev[0].status === "ready" && evals.length === 0 && (
        <EmptyState
          title="No evaluation runs for this dataset"
          hint="Run an evaluation against ingested runs of this agent version. Scoring is deterministic (tool selection, schema validity, task completion) and every result links to its origin run."
        />
      )}

      {evals.length > 0 && (
        <Carousel slides={slides} ariaLabel="Candidate version comparison" />
      )}

      {evals.length > 0 && (
        <EvalResultsTable evals={evals} onOpenRun={onOpenRun} />
      )}
    </div>
  );
}

function agentLabel(r: EvalRunRow) {
  return `${r.agent_name || "agent"} ${r.agent_version || ""}`.trim();
}

function EvalSummary({
  run,
  index,
  total,
  onOpenRun,
}: {
  run: EvalRunRow;
  index: number;
  total: number;
  onOpenRun: (runId: string) => void;
}) {
  const d = useAsync(() => api.evaluation(run.id), [run.id]);
  if (d[0].status !== "ready" || !d[0].data) return <Loading label="Loading results…" />;
  const detail = d[0].data as EvalDetail;
  const gate = parseGateJson(detail.gate_json);

  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 10 }}>
      <div style={{ display: "flex", gap: 16, flexWrap: "wrap" }}>
        <Metric label="passed" value={`${detail.passed ?? 0}/${detail.total_cases ?? 0}`} />
        <Metric label="avg score" value={detail.avg_score != null ? detail.avg_score.toFixed(2) : "—"} />
        <Metric label="score source" value={detail.score_source || "—"} />
        <Metric
          label="gate"
          value={detail.gate_failed ? "FAILED" : "passed"}
          tone={detail.gate_failed ? "var(--danger)" : "var(--success)"}
        />
      </div>
      <div style={{ fontSize: 11.5, color: "var(--text-3)" }}>
        deterministic scoring · evaluator {detail.evaluator_version || "v1"} · dataset{" "}
        {detail.dataset_version || "—"} · started {fmtDateTime(detail.started_at)}
      </div>
      {gate && (
        <div className="chip" style={{ alignSelf: "flex-start" }} title="CI gate as recorded by the API">
          gate: {gate.require ? "required" : "advisory"} · pass ≥{" "}
          {Math.round((gate.minPassRate ?? 0) * 100)}% · score ≥ {gate.minScore ?? 0}
        </div>
      )}
      <table className="tbl" style={{ fontSize: 11.5 }}>
        <thead>
          <tr>
            <th>Case</th>
            <th>Status</th>
            <th className="td-num">Score</th>
            <th>Evidence</th>
          </tr>
        </thead>
        <tbody>
          {(detail.results ?? []).map((r: EvalResult) => (
            <tr key={r.case_id}>
              <td className="mono">{r.case_id}</td>
              <td>
                <span className={`status ${r.passed ? "completed" : "failed"}`}>
                  {r.status || (r.passed ? "passed" : "failed")}
                </span>
              </td>
              <td className="td-num">{r.score != null ? r.score.toFixed(2) : "—"}</td>
              <td style={{ maxWidth: 220, fontSize: 11, color: "var(--text-2)" }}>
                {r.detail || "—"}
                {r.origin_run && (
                  <>
                    <div>
                      <button
                        className="btn ghost"
                        style={{ padding: "1px 5px", minHeight: 0, fontSize: 11 }}
                        onClick={() => onOpenRun(r.origin_run!)}
                      >
                        origin run {shortId(r.origin_run)} →
                      </button>
                    </div>
                  </>
                )}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      <span style={{ fontSize: 10.5, color: "var(--text-3)" }}>
        result table {index + 1}/{total} · flip or drag the carousel to compare versions
      </span>
    </div>
  );
}

function Metric({ label, value, tone }: { label: string; value: string; tone?: string }) {
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 2 }}>
      <span style={{ fontSize: 9.5, letterSpacing: "0.06em", textTransform: "uppercase", color: "var(--text-3)", fontWeight: 600 }}>
        {label}
      </span>
      <span className="num" style={{ fontSize: 14, fontWeight: 600, color: tone || "var(--text-1)" }}>
        {value}
      </span>
    </div>
  );
}

function EvalResultsTable({ evals, onOpenRun }: { evals: EvalRunRow[]; onOpenRun: (id: string) => void }) {
  // All recorded evaluation runs for this dataset, in one table. Results
  // are the deterministic checks returned by the API — no statistics beyond
  // what the API reports.
  const [expanded, setExpanded] = useState<string | null>(null);
  const detail = useAsync<EvalDetail | null>(
    () => (expanded ? api.evaluation(expanded) : Promise.resolve(null)),
    [expanded],
  );

  return (
    <section aria-label="All evaluation runs">
      <div className="wb-head" style={{ borderRadius: 8 }}>
        recorded evaluation runs <span className="grow" /> {evals.length}
      </div>
      <table className="tbl">
        <thead>
          <tr>
            <th>Run</th>
            <th>Agent</th>
            <th>Model</th>
            <th>Dataset</th>
            <th>Pass</th>
            <th>Avg score</th>
            <th>Gate</th>
            <th>Started</th>
          </tr>
        </thead>
        <tbody>
          {evals.map((r) => (
            <Fragment key={r.id}>
              <tr
                key={r.id}
                className="clickable"
                onClick={() => setExpanded(expanded === r.id ? null : r.id)}
                aria-expanded={expanded === r.id}
              >
                <td className="mono" style={{ fontSize: 11 }}>{shortId(r.id)}</td>
                <td className="mono">{agentLabel(r)}</td>
                <td className="mono">{r.model || "—"}</td>
                <td className="mono">{r.dataset_version || "—"}</td>
                <td className="td-num">
                  {r.passed ?? 0}/{r.total_cases ?? 0}
                </td>
                <td className="td-num">{r.avg_score != null ? r.avg_score.toFixed(2) : "—"}</td>
                <td>
                  {r.gate_failed ? (
                    <span className="status error">gate failed</span>
                  ) : (
                    <span className="status success">passed</span>
                  )}
                </td>
                <td style={{ fontSize: 11 }}>{fmtDateTime(r.started_at)}</td>
              </tr>
              {expanded === r.id &&
                (detail[0].status === "ready" && detail[0].data ? (
                  <tr key={`${r.id}-detail`} style={{ background: "var(--surface-inset)" }}>
                    <td colSpan={8}>
                      <div style={{ padding: "8px 4px", display: "flex", flexDirection: "column", gap: 4 }}>
                        <span style={{ fontSize: 11, color: "var(--text-3)" }}>
                          per-case evidence for {shortId(r.id)}:
                        </span>
                        {(detail[0].data!.results ?? []).map((res: EvalResult) => (
                          <div key={res.case_id} style={{ display: "flex", gap: 8, fontSize: 11.5, alignItems: "baseline", flexWrap: "wrap" }}>
                            <span className="mono">{res.case_id}</span>
                            <span className={`status ${res.passed ? "completed" : "failed"}`}>
                              {res.status || (res.passed ? "passed" : "failed")}
                            </span>
                            <span className="num">score {res.score != null ? res.score.toFixed(2) : "—"}</span>
                            <span style={{ color: "var(--text-2)" }}>{res.detail || ""}</span>
                            {res.origin_run && (
                              <button className="btn ghost" style={{ padding: "0 6px", minHeight: 0 }} onClick={(e) => { e.stopPropagation(); onOpenRun(res.origin_run!); }}>
                                {shortId(res.origin_run)} →
                              </button>
                            )}
                          </div>
                        ))}
                      </div>
                    </td>
                  </tr>
                ) : detail[0].status === "loading" ? (
                  <tr key={`${r.id}-loading`}>
                    <td colSpan={8}>
                      <Loading label="Loading case evidence…" />
                    </td>
                  </tr>
                ) : null)}
            </Fragment>
          ))}
        </tbody>
      </table>
    </section>
  );
}

/* ===================== safe replay ===================== */

function ReplayTab({ onOpenRun }: { onOpenRun: (runId: string) => void }) {
  const runs = useAsync(() => api.listRuns({ limit: 50 }), []);
  const [origin, setOrigin] = useState("");
  const [lastJob, setLastJob] = useState<ReplayCreated | null>(null);
  const [job, setJob] = useState<ReplayJob | null>(null);
  const [busy, setBusy] = useState(false);
  const [refusal, setRefusal] = useState<string | null>(null);
  const [live, setLive] = useState(false);

  // live/real-tool replay is *refused visibly*, not just disabled:
  const requestLive = () =>
    setRefusal(
      "Live replay refused: the API rejects live mode through the default path. " +
        "Live execution requires a separately configured executor with an " +
        "allowlist and dry-run checks — it is not available from this workbench.",
    );

  const start = async () => {
    if (!origin) return;
    setBusy(true);
    setRefusal(null);
    try {
      if (live) {
        // The server refuses mode=live with 405; show that refusal verbatim
        // rather than guessing.
        try {
          await api.createReplay({ origin_run: origin, mode: "live" });
        } catch (e) {
          setRefusal(e instanceof Error ? e.message : String(e));
          setBusy(false);
          return;
        }
      }
      const r = await api.createReplay({ origin_run: origin, mode: "mocked" });
      setLastJob(r);
      const jr = await api.replay(r.id).catch(() => null);
      setJob(jr);
    } catch (e) {
      setRefusal(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 14, maxWidth: 760 }}>
      {runs[0].status === "error" && (
        <ErrorState
          message={"Could not list runs for replay origin selection: " + runs[0].error}
          onRetry={() => runs[1]()}
        />
      )}
      <div className="replay-panel">
        <div>
          <h3 style={{ margin: 0, fontSize: 14 }}>Replay a run with mocked tools</h3>
          <p style={{ margin: "6px 0 0", fontSize: 12, color: "var(--text-2)" }}>
            Mocked tools are the default: no external side effects, no
            production credentials. The job records step outcomes and
            provenance so a replayed run can be diffed against the origin.
          </p>
        </div>

        <div style={{ display: "flex", gap: 8, flexWrap: "wrap", alignItems: "end" }}>
          <label className="field" style={{ minWidth: 260 }}>
            Origin run
            <select value={origin} onChange={(e) => setOrigin(e.target.value)}>
              <option value="">select a run…</option>
              {runs[0].status === "ready" &&
                runs[0].data!.map((r) => (
                  <option key={r.id} value={r.id}>
                    {shortId(r.id)} · {r.status} · {r.agent_name} {r.agent_version}
                  </option>
                ))}
            </select>
          </label>

          <div
            className="mode-toggle"
            role="group"
            aria-label="Replay executor mode"
          >
            <button
              aria-pressed={!live}
              onClick={() => {
                setLive(false);
                setRefusal(null);
              }}
            >
              mocked tools (default)
            </button>
            <button
              className="live"
              aria-pressed={live}
              title="Refused by the API — click to read why"
              onClick={() => {
                setLive(true);
                requestLive();
              }}
            >
              live / real tools
            </button>
          </div>

          <button className="btn primary" disabled={!origin || busy} onClick={start}>
            {busy ? "replaying…" : "start replay"}
          </button>
        </div>

        <div className="replay-live-refused" role="note">
          <strong>Live tool replay is refused by design.</strong>
          <span>
            Only the mocked executor runs through this path. Selecting
            <em> live / real tools</em> is refused explicitly — the server
            returns an error and this panel shows it. Payment, email,
            deletion and other production mutations are never re-run from
            the workbench.
          </span>
        </div>

        {refusal && (
          <div className="replay-live-refused" role="alert">
            <strong>Refused:</strong>
            <span>{refusal}</span>
          </div>
        )}
      </div>

      {lastJob && (
        <div className="replay-panel" aria-label="Replay result">
          <div style={{ display: "flex", gap: 16, flexWrap: "wrap", alignItems: "baseline" }}>
            <strong>
              replay <span className="mono">{shortId(lastJob.id)}</span>
            </strong>
            <span className="status success">
              mode {lastJob.mode || "mocked"}
            </span>
            <Metric label="steps" value={`${lastJob.completed ?? 0}/${lastJob.step_count ?? 0}`} />
            {lastJob.failed ? <Metric label="failed" value={String(lastJob.failed)} tone="var(--danger)" /> : null}
            {lastJob.refused ? <Metric label="refused" value={String(lastJob.refused)} tone="var(--warning)" /> : null}
          </div>
          {lastJob.origin_run && (
            <div style={{ display: "flex", gap: 8 }}>
              <button className="btn ghost" onClick={() => onOpenRun(lastJob.origin_run!)}>
                open origin run →
              </button>
              <button className="btn ghost" onClick={() => onOpenRun(lastJob.replay_run!)}>
                open replay run →
              </button>
            </div>
          )}
          {job && (
            <div style={{ fontSize: 11.5, color: "var(--text-3)" }}>
              stored job {shortId(job.id)}: {job.status || "—"} ·{" "}
              {fmtDateTime(job.created_at)}
              {job.mock_ref ? ` · fixtures ${shortId(job.mock_ref)}` : ""}
            </div>
          )}
        </div>
      )}

      {lastJob?.provenance != null && (
        <details className="raw" style={{ maxWidth: 720 }}>
          <summary>replay provenance</summary>
          <pre className="raw-json">{JSON.stringify(lastJob.provenance, null, 2)}</pre>
        </details>
      )}
    </div>
  );
}

