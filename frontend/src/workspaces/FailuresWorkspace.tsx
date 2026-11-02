import { useMemo, useState } from "react";
import { API_KEY, api, PROJECT_ID, isDemoKey } from "../api";
import type { Finding, SignatureDetail, Timeline } from "../types";
import {
  EmptyState,
  ErrorState,
  Loading,
  fmtDateTime,
  fmtNum,
  useAsync,
} from "../hooks";

const KIND_LABELS: Record<string, string> = {
  "tool-loop": "repeated tool loop",
  "state-oscillation": "state oscillation",
  "budget-exhaustion": "budget exhaustion",
  "failure-cluster": "failure cluster",
  "context-loss": "context loss",
};

export function FailuresWorkspace({
  onOpenRun,
}: {
  onOpenRun: (runId: string, spanId?: string, eventIds?: string[]) => void;
}) {
  const [sig, setSig] = useState<string | null>(null);
  const sigs = useAsync(() => api.signatures(), []);

  return (
    <div className="content">
      <header className="h1-row">
        <h1>Failures</h1>
        <span className="h1-sub">
          deterministic rule-engine signatures for project{" "}
          <code className="mono">{PROJECT_ID}</code>
          {isDemoKey(API_KEY) ? " · seeded sample data" : ""}
        </span>
      </header>

      {sigs[0].status === "loading" && <Loading label="Loading failure signatures…" />}
      {sigs[0].status === "error" && (
        <ErrorState message={sigs[0].error} onRetry={() => sigs[1]()} />
      )}
      {sigs[0].status === "ready" && sigs[0].data!.length === 0 && (
        <EmptyState
          title="No failure signatures yet"
          hint="The rule engine writes a signature for every detected failure. Ingest runs with failures (or run the server with --seed-demo) to see clusters here."
        />
      )}

      {sigs[0].status === "ready" && sigs[0].data!.length > 0 && (
        sig ? (
          <SignaturePanel sig={sig} onBack={() => setSig(null)} onOpenRun={onOpenRun} />
        ) : (
          <table className="tbl" aria-label="Failure clusters">
            <thead>
              <tr>
                <th>Signature</th>
                <th>Kind</th>
                <th>Tool</th>
                <th className="td-num">Occurrences</th>
                <th className="td-num">Runs</th>
                <th>First seen</th>
                <th>Last seen</th>
                <th>Agent versions</th>
              </tr>
            </thead>
            <tbody>
              {sigs[0].data!.map((s) => (
                <tr
                  key={s.signature}
                  className="clickable"
                  tabIndex={0}
                  onClick={() => setSig(s.signature)}
                  onKeyDown={(e) => {
                    if (e.key === "Enter") setSig(s.signature);
                  }}
                  aria-label={`Open signature ${s.signature}`}
                >
                  <td className="mono" title={s.signature}>
                    {short(s.signature)}
                  </td>
                  <td>
                    <span className={sevClass(s.kind)}>
                      {KIND_LABELS[s.kind || ""] || s.kind || "—"}
                    </span>
                  </td>
                  <td className="mono">{s.tool_name || "—"}</td>
                  <td className="td-num">{fmtNum(s.count)}</td>
                  <td className="td-num">{fmtNum(s.run_count)}</td>
                  <td>{fmtDateTime(s.first_seen)}</td>
                  <td>{fmtDateTime(s.last_seen)}</td>
                  <td className="mono" style={{ fontSize: 11 }}>
                    {s.agent_versions || "—"}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )
      )}
    </div>
  );
}

function sevClass(kind?: string) {
  if (kind === "failure-cluster" || kind === "budget-exhaustion") return "sev-error";
  if (kind === "context-loss") return "sev-info";
  return "sev-warning";
}

/* ---- signature detail: per-kind evidence panels ---- */

function SignaturePanel({
  sig,
  onBack,
  onOpenRun,
}: {
  sig: string;
  onBack: () => void;
  onOpenRun: (runId: string, spanId?: string, eventIds?: string[]) => void;
}) {
  const detail = useAsync<SignatureDetail | null>(
    () => api.signatureDetail(sig).catch(() => null),
    [sig],
  );
  const d = detail[0].status === "ready" ? detail[0].data! : null;
  const info = d?.info ?? null;
  const findings = d?.findings ?? [];

  // The first finding's run timeline resolves evidence event ids to spans,
  // so "open in Run Explorer" lands on the offending span.
  const firstRun = findings[0]?.run_id;
  const tl = useAsync<Timeline | null>(
    () =>
      firstRun
        ? api.timeline(firstRun).catch(() => null) // 404 is not fatal here
        : Promise.resolve(null),
    [firstRun],
  );

  const index = useMemo(
    () => buildEvidenceIndex(tl[0].status === "ready" ? tl[0].data : null),
    [tl],
  );

  if (detail[0].status === "loading") return <Loading label="Loading signature detail…" />;

  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 16 }}>
      <button className="btn ghost" onClick={onBack} style={{ alignSelf: "flex-start" }}>
        ← all signatures
      </button>

      <section aria-label="Signature summary">
        <div className="mono" style={{ fontSize: 13, wordBreak: "break-all" }}>
          {info?.signature ?? sig}
        </div>
        <dl className="insp-kv" style={{ marginTop: 8, gridTemplateColumns: "auto 1fr" }}>
          <dt>kind</dt>
          <dd>{KIND_LABELS[info?.kind || ""] || info?.kind || "—"}</dd>
          <dt>tool</dt>
          <dd className="mono">{info?.tool_name || "—"}</dd>
          <dt>occurrences / runs</dt>
          <dd className="num">
            {fmtNum(info?.count)} / {fmtNum(info?.run_count)}
          </dd>
          <dt>first → last</dt>
          <dd>
            {fmtDateTime(info?.first_seen)} → {fmtDateTime(info?.last_seen)}
          </dd>
        </dl>
      </section>

      {findings.length === 0 && (
        <EmptyState
          title="No findings recorded for this signature"
          hint="The cluster row exists but no individual findings link to it yet."
        />
      )}

      {findings.length > 0 && (
        <table className="tbl" aria-label="Findings for this signature">
          <thead>
            <tr>
              <th>Finding</th>
              <th>Severity</th>
              <th>Run</th>
              <th>Rule-engine evidence</th>
            </tr>
          </thead>
          <tbody>
            {findings.map((f) => (
              <FindingRow
                key={f.id}
                finding={f}
                timeline={tl[0].status === "ready" ? tl[0].data : null}
                index={index}
                onOpenRun={onOpenRun}
              />
            ))}
          </tbody>
        </table>
      )}
    </div>
  );
}

function FindingRow({
  finding,
  timeline,
  index,
  onOpenRun,
}: {
  finding: Finding;
  timeline: Timeline | null;
  index: Map<string, { spanId: string; seq: number | undefined }>;
  onOpenRun: (runId: string, spanId?: string, eventIds?: string[]) => void;
}) {
  const evidence = finding.evidence ?? [];
  const resolvable = evidence.filter((id) => index.has(id));
  const targetSpan = resolvable.length ? index.get(resolvable[0])?.spanId : undefined;
  const keys = timeline ? toolCallKeysFor(resolvable, timeline, index) : [];

  return (
    <tr>
      <td style={{ maxWidth: 340 }}>
        <div style={{ display: "flex", flexDirection: "column", gap: 4 }}>
          <span style={{ fontSize: 12 }}>{finding.message || finding.kind}</span>
          {finding.kind === "tool-loop" && keys[0] && (
            <span
              className="chip"
              title="normalized tool-call key: tool | args-hash | schema-version"
            >
              {keys[0]}
            </span>
          )}
          <KindDetail finding={finding} timeline={timeline} index={index} />
        </div>
      </td>
      <td>
        <span className={`sev-${finding.severity || "info"}`}>
          {finding.severity || "info"}
        </span>
      </td>
      <td className="mono" style={{ fontSize: 11 }}>
        {finding.run_id}
      </td>
      <td>
        <div style={{ display: "flex", flexDirection: "column", gap: 4, maxWidth: 420 }}>
          {evidence.length === 0 ? (
            <span style={{ fontSize: 11, color: "var(--text-3)" }}>
              no event evidence recorded
            </span>
          ) : (
            evidence.map((id) => {
              const ev = timeline?.events.find((e) => e.id === id);
              return (
                <span key={id} className="chip" title={index.get(id)?.spanId ? `span ${index.get(id)!.spanId}` : "span not resolved"}>
                  evt {short(id)}
                  {ev ? ` · seq ${ev.seq ?? "?"} · ${ev.type}` : ""}
                </span>
              );
            })
          )}
          {timeline && targetSpan ? (
            <button
              className="btn"
              style={{ alignSelf: "flex-start" }}
              onClick={() => onOpenRun(finding.run_id, targetSpan, resolvable)}
            >
              open in Run Explorer →
            </button>
          ) : (
            <span style={{ fontSize: 11, color: "var(--text-3)" }}>
              run timeline unavailable in this project; evidence ids are the
              raw links.
            </span>
          )}
        </div>
      </td>
    </tr>
  );
}

/** Per-kind detail blocks (tool-loop / oscillation / budget / cluster). All
 * values are read from recorded events — nothing is inferred or fabricated. */
function KindDetail({
  finding,
  timeline,
  index,
}: {
  finding: Finding;
  timeline: Timeline | null;
  index: Map<string, { spanId: string; seq: number | undefined }>;
}) {
  if (!timeline) return null;
  const evs = (finding.evidence ?? [])
    .map((id) => timeline.events.find((e) => e.id === id))
    .filter((e): e is NonNullable<typeof e> => !!e);

  switch (finding.kind) {
    case "tool-loop": {
      const keys = toolCallKeysFor(evs.map((e) => e.id), timeline, index);
      return (
        <span style={{ fontSize: 11, color: "var(--text-2)" }}>
          {evs.length} recorded tool-call events
          {keys[0] ? (
            <> sharing the normalized key <span className="chip">{keys[0]}</span></>
          ) : null}
        </span>
      );
    }
    case "state-oscillation": {
      const fps = evs
        .map((e) => String(e.attributes?.["state.fingerprint"] ?? "?"))
        .join(" → ");
      return (
        <span style={{ fontSize: 11, color: "var(--text-2)" }}>
          fingerprint sequence <span className="mono">{fps}</span>, no progress
          event in between.
        </span>
      );
    }
    case "budget-exhaustion":
      return (
        <span style={{ fontSize: 11, color: "var(--text-2)" }}>
          {evs.length ? evs.map((e) => e.type).join(", ") : "budget events recorded for this run"}.
        </span>
      );
    default:
      return (
        <span style={{ fontSize: 11, color: "var(--text-2)" }}>
          {finding.message || "see evidence event ids"}
        </span>
      );
  }
}

/* ---- helpers ---- */

function toolCallKeysFor(
  ids: string[],
  timeline: Timeline,
  index: Map<string, { spanId: string; seq: number | undefined }>,
): string[] {
  const out = new Set<string>();
  for (const id of ids) {
    const ev = timeline.events.find((e) => e.id === id);
    const a = ev?.attributes ?? {};
    const tool = (a["tool.name"] as string) || "";
    const schema = (a["tool.schema_version"] as string) || "";
    const argHash = (a["tool.args_hash"] as string) || "";
    if (tool && argHash) out.add(`${tool} | ${argHash} | ${schema}`);
    const span = timeline.spans.find((s) => s.id === index.get(id)?.spanId);
    if (span?.tool_args_hash && span.tool_name)
      out.add(
        `${span.tool_name} | ${span.tool_args_hash} | ${span.tool_schema_version ?? ""}`,
      );
  }
  return [...out];
}

function buildEvidenceIndex(
  tl: Timeline | null,
): Map<string, { spanId: string; seq: number | undefined }> {
  const m = new Map<string, { spanId: string; seq: number | undefined }>();
  if (!tl) return m;
  for (const e of tl.events) m.set(e.id, { spanId: e.span_id || "", seq: e.seq });
  return m;
}

function short(id: string) {
  if (!id) return "—";
  return id.length > 18 ? `${id.slice(0, 10)}…${id.slice(-6)}` : id;
}
