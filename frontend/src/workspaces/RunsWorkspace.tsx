import { useEffect, useRef, useState } from "react";
import { api, ApiError, PROJECT_ID, API_KEY, isDemoKey } from "../api";
import type { Timeline } from "../types";
import {
  EmptyState,
  ErrorState,
  Loading,
  fmtMs,
  useAsync,
} from "../hooks";
import type { RunJump } from "../App";
import { TimelineCanvas } from "./timeline/TimelineCanvas";
import { RunInspector } from "./timeline/RunInspector";

const STATUS_OPTIONS = [
  { value: "", label: "all statuses" },
  { value: "completed", label: "completed" },
  { value: "failed", label: "failed" },
  { value: "cancelled", label: "cancelled" },
];

/** Run Explorer workbench (spec 11): left trace outline, centre timeline
 * canvas, right inspector. Filters: status, agent, agent version, time
 * range (client-side on the returned set; the API itself filters by
 * status/agent/version — time is a display filter on the list). */
export function RunsWorkspace({
  jump,
  onJumpHandled,
}: {
  jump: RunJump | null;
  onJumpHandled: () => void;
}) {
  const [status, setStatus] = useState("");
  const [agent, setAgent] = useState("");
  const [agentVersion, setAgentVersion] = useState("");
  const [fromIso, setFromIso] = useState("");
  const [toIso, setToIso] = useState("");
  const [activeRunId, setActiveRunId] = useState<string | null>(null);
  const [selectedSpan, setSelectedSpan] = useState<string | null>(null);
  const [selectedEvent, setSelectedEvent] = useState<string | null>(null);
  const [inspectorOpen, setInspectorOpen] = useState(false);
  const [highlightEvents, setHighlightEvents] = useState<string[]>([]);

  const filtered = useAsync(
    () =>
      api.listRuns({
        status: status || undefined,
        agent: agent || undefined,
        agentVersion: agentVersion || undefined,
        limit: 50,
      }),
    [status, agent, agentVersion],
  );

  const runs = (
    filtered[0].status === "ready"
      ? filtered[0].data
      : []
  ).filter((r) => {
    if (!fromIso && !toIso) return true;
    const t = r.started_at ? new Date(r.started_at).getTime() : 0;
    if (fromIso && t < new Date(fromIso).getTime()) return false;
    if (toIso && t > new Date(toIso).getTime()) return false;
    return true;
  });

  // Jump handoff from Failures / Evaluations: open the run, focus the span
  // and highlight the evidence events.
  const pendingJump = useRef<RunJump | null>(null);
  useEffect(() => {
    if (!jump) return;
    pendingJump.current = jump;
    setActiveRunId(jump.runId);
    setHighlightEvents(jump.eventIds ?? []);
    setInspectorOpen(true);
    onJumpHandled();
  }, [jump, onJumpHandled]);

  const timeline = useAsync<Timeline | null>(
    () =>
      activeRunId
        ? api.timeline(activeRunId).catch((e: unknown) => {
            if (e instanceof ApiError && e.status === 404) return null;
            throw e;
          })
        : Promise.resolve(null),
    [activeRunId],
  );

  // When the jumped-to run's timeline arrives, resolve the evidence events to
  // their owning spans and select the offending one.
  const tl = timeline[0];
  useEffect(() => {
    const p = pendingJump.current;
    if (!p || !p.eventIds?.length || tl.status !== "ready" || !tl.data) return;
    const ev = tl.data.events.find((e) => p.eventIds!.includes(e.id));
    if (ev?.span_id) {
      setSelectedSpan(ev.span_id);
      setSelectedEvent(ev.id);
    }
    pendingJump.current = null;
  }, [tl]);

  return (
    <div className="content">
      <header className="h1-row">
        <h1>Run Explorer</h1>
        <span className="h1-sub">
          project <code className="mono">{PROJECT_ID}</code> ·{" "}
          {isDemoKey(API_KEY) ? "seeded sample data" : "live"}
        </span>
      </header>

      {/* filters live next to the table they filter */}
      <div
        style={{
          display: "flex",
          gap: 8,
          flexWrap: "wrap",
          alignItems: "end",
        }}
        role="group"
        aria-label="Run filters"
      >
        <label className="field">
          Status
          <select value={status} onChange={(e) => setStatus(e.target.value)}>
            {STATUS_OPTIONS.map((o) => (
              <option key={o.value} value={o.value}>
                {o.label}
              </option>
            ))}
          </select>
        </label>
        <label className="field">
          Agent
          <input
            value={agent}
            onChange={(e) => setAgent(e.target.value)}
            placeholder="support-agent"
            size={14}
          />
        </label>
        <label className="field">
          Agent version
          <input
            value={agentVersion}
            onChange={(e) => setAgentVersion(e.target.value)}
            placeholder="1.4.0"
            size={10}
          />
        </label>
        <label className="field">
          From
          <input
            type="datetime-local"
            value={fromIso}
            onChange={(e) => setFromIso(e.target.value)}
          />
        </label>
        <label className="field">
          To
          <input
            type="datetime-local"
            value={toIso}
            onChange={(e) => setToIso(e.target.value)}
          />
        </label>
        {(fromIso || toIso) && (
          <button
            className="btn ghost"
            onClick={() => {
              setFromIso("");
              setToIso("");
            }}
          >
            clear range
          </button>
        )}
      </div>

      {filtered[0].status === "loading" && !runs.length && <Loading label="Loading runs…" />}
      {filtered[0].status === "error" && (
        <ErrorState
          message={filtered[0].error}
          onRetry={() => filtered[1]()}
        />
      )}
      {filtered[0].status === "ready" && runs.length === 0 && (
        <EmptyState
          title="No runs match these filters"
          hint={
            agent || agentVersion || status || fromIso || toIso
              ? "Loosen the filters — the API returned runs, but the local time/status filter excluded them, or the server has no runs for this project yet."
              : "This project has no ingested runs yet. Ingest events through the SDK, or run the server with --seed-demo."
          }
        />
      )}

      {runs.length > 0 && (
        <div className="workbench" style={{ height: "calc(100vh - 260px)" }}>
          {/* ---- left: trace outline (run list) ---- */}
          <section className="wb-col outline-col" aria-label="Run outline">
            <div className="wb-head">
              runs <span className="grow" />
              <span className="num">{runs.length}</span>
            </div>
            <div className="wb-body">
              <ul className="run-outline" role="listbox" aria-label="Runs">
                {runs.map((r) => (
                  <li
                    key={r.id}
                    aria-selected={activeRunId === r.id}
                  >
                    <button
                      onClick={() => {
                        setActiveRunId(r.id);
                        setSelectedSpan(null);
                        setSelectedEvent(null);
                        setHighlightEvents([]);
                      }}
                      aria-label={`Open run ${r.id}`}
                    >
                      <span className="ro-main">
                        <span className="ro-id">{shortId(r.id)}</span>
                        <StatusPill status={r.status} />
                      </span>
                      <span className="ro-sub">
                        {r.agent_name} {r.agent_version} · {fmtMs(r.duration_ms)}
                        {r.finding_count ? (
                          <span className={`sev-${r.worst_severity}`}>
                            {" "}
                            · {r.finding_count} finding
                            {r.finding_count > 1 ? "s" : ""} ({r.worst_severity})
                          </span>
                        ) : null}
                      </span>
                    </button>
                  </li>
                ))}
              </ul>
            </div>
          </section>

          {/* ---- centre: timeline ---- */}
          <section className="wb-col" aria-label="Run timeline">
            <div className="wb-head">
              timeline
              {activeRunId && (
                <span className="mono" style={{ textTransform: "none", letterSpacing: 0 }}>
                  {activeRunId}
                </span>
              )}
              <span className="grow" />
              {highlightEvents.length > 0 && (
                <button
                  className="btn ghost"
                  onClick={() => setHighlightEvents([])}
                >
                  clear evidence highlight
                </button>
              )}
            </div>
            <div className="wb-body">
              {!activeRunId && (
                <EmptyState
                  title="Select a run"
                  hint="Pick a run on the left to load its span hierarchy and events."
                />
              )}
              {activeRunId && timeline[0].status === "loading" && (
                <Loading label={`Loading timeline for ${activeRunId}…`} />
              )}
              {activeRunId && timeline[0].status === "error" && (
                <ErrorState
                  message={timeline[0].error}
                  onRetry={() => timeline[1]()}
                />
              )}
              {activeRunId &&
                timeline[0].status === "ready" &&
                timeline[0].data === null && (
                  <EmptyState
                    title="Run not found"
                    hint={`No timeline for ${activeRunId} in this project (404). It may belong to another tenant or not exist yet.`}
                  />
                )}
              {activeRunId &&
                timeline[0].status === "ready" &&
                timeline[0].data && (
                  <TimelineCanvas
                    timeline={timeline[0].data!}
                    selectedSpan={selectedSpan}
                    selectedEvent={selectedEvent}
                    highlightEvents={highlightEvents}
                    onSelectSpan={(id, evIds) => {
                      setSelectedSpan(id);
                      setSelectedEvent(null);
                      if (evIds) setHighlightEvents(evIds);
                      setInspectorOpen(true);
                    }}
                  />
                )}
            </div>
          </section>

          {/* ---- right: inspector ---- */}
          <section
            className={`wb-col inspector-col ${inspectorOpen ? "open" : ""}`}
            aria-label="Event inspector"
          >
            <div className="wb-head">
              inspector
              <span className="grow" />
              <button
                className="btn ghost"
                aria-label="Hide inspector"
                style={{ padding: "0 4px", minHeight: 0 }}
                onClick={() => setInspectorOpen(false)}
              >
                ✕
              </button>
            </div>
            <div className="wb-body">
              {timeline[0].status === "ready" && timeline[0].data ? (
                <RunInspector
                  timeline={timeline[0].data!}
                  selectedSpan={selectedSpan}
                  selectedEvent={selectedEvent}
                  onSelectEvent={(id) => setSelectedEvent(id)}
                />
              ) : (
                <EmptyState
                  title="Nothing selected"
                  hint="Click a span bar or an event to inspect its raw evidence and attributes."
                />
              )}
            </div>
          </section>
        </div>
      )}
    </div>
  );
}

function StatusPill({ status }: { status?: string }) {
  const s = status || "open";
  return (
    <span className={`status ${s}`} title={`status: ${s}`}>
      {s}
    </span>
  );
}

function shortId(id: string) {
  if (id.length <= 14) return id;
  return `${id.slice(0, 8)}…${id.slice(-4)}`;
}
