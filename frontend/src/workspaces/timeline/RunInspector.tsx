import type { EventRow, SpanRow, Timeline } from "../../types";
import { UsageTag, fmtDateTime, fmtMs, fmtNum, fmtUSD } from "../../hooks";

interface Props {
  timeline: Timeline;
  selectedSpan: string | null;
  selectedEvent: string | null;
  onSelectEvent: (id: string) => void;
}

/** Contextual inspector (spec 11: opens only when an event is selected).
 * Shows the selected span's attributes and its raw event evidence, plus a
 * list of the span's events for click-to-inspect. Every datum is straight
 * from the timeline payload — nothing is invented. */
export function RunInspector({
  timeline,
  selectedSpan,
  selectedEvent,
  onSelectEvent,
}: Props) {
  const span: SpanRow | undefined = timeline.spans.find((s) => s.id === selectedSpan);
  const ev: EventRow | undefined = timeline.events.find((e) => e.id === selectedEvent);
  const spanEvents = span ? timeline.events.filter((e) => e.span_id === span.id) : [];

  return (
    <div className="inspector">
      {ev ? (
        <section aria-label="Selected event">
          <div className="insp-title">
            event <span className="mono" style={{ color: "var(--text-3)" }}>{ev.id}</span>
          </div>
          <dl className="insp-kv" style={{ marginTop: 8 }}>
            <dt>type</dt>
            <dd className="mono">{ev.type || "—"}</dd>
            <dt>sequence</dt>
            <dd className="num">{fmtNum(ev.seq)}</dd>
            <dt>time</dt>
            <dd>{fmtDateTime(ev.ts)}</dd>
            <dt>span</dt>
            <dd className="mono">{ev.span_id || "—"}</dd>
            <dt>agent</dt>
            <dd>{[ev.agent_name, ev.agent_version].filter(Boolean).join(" ") || "—"}</dd>
            {ev.payload_ref && (
              <>
                <dt>payload</dt>
                <dd className="mono" title={ev.payload_ref}>{ev.payload_ref}</dd>
              </>
            )}
          </dl>
          <details className="raw" open style={{ marginTop: 8 }}>
            <summary>raw attributes</summary>
            <pre className="raw-json">{JSON.stringify(ev.attributes ?? {}, null, 2)}</pre>
          </details>
        </section>
      ) : span ? (
        <section aria-label="Selected span">
          <div className="insp-title">
            {span.kind} <span className="mono" style={{ color: "var(--text-3)" }}>{span.id}</span>
          </div>
          <div style={{ marginTop: 6 }}>
            <span className={`status ${span.status || "open"}`}>{span.status || "open"}</span>
            {span.usage_source && <span style={{ marginLeft: 8 }}><UsageTag source={span.usage_source} /></span>}
          </div>
          <dl className="insp-kv" style={{ marginTop: 10 }}>
            <dt>name</dt>
            <dd>{span.name || "—"}</dd>
            <dt>kind</dt>
            <dd>{span.kind}</dd>
            <dt>started</dt>
            <dd>{fmtDateTime(span.started_at)}</dd>
            <dt>ended</dt>
            <dd>{fmtDateTime(span.ended_at)}</dd>
            <dt>duration</dt>
            <dd className="num">{fmtMs(span.duration_ms)}</dd>
            <dt>seq</dt>
            <dd className="num">
              {fmtNum(span.seq_start)}{span.seq_start !== undefined && span.seq_end !== undefined ? " → " : ""}
              {span.seq_end !== undefined ? fmtNum(span.seq_end) : "…"}
            </dd>
            {span.tool_name && (
              <>
                <dt>tool</dt>
                <dd className="mono">
                  {span.tool_name} {span.tool_version ? `(${span.tool_version})` : ""}
                </dd>
                {span.tool_schema_version && (
                  <>
                    <dt>schema</dt>
                    <dd className="mono">{span.tool_schema_version}</dd>
                  </>
                )}
                {span.tool_args_hash && (
                  <>
                    <dt>args hash</dt>
                    <dd className="mono" title={span.tool_args_hash}>{span.tool_args_hash}</dd>
                  </>
                )}
              </>
            )}
            {(span.model || span.provider) && (
              <>
                <dt>model</dt>
                <dd className="mono">{span.model || "—"} {span.provider ? `via ${span.provider}` : ""}</dd>
              </>
            )}
            <dt>tokens in/out</dt>
            <dd className="num">
              {fmtNum(span.tokens_in)} / {fmtNum(span.tokens_out)}
            </dd>
            {span.cost_usd !== undefined && (
              <>
                <dt>cost</dt>
                <dd className="num">
                  {fmtUSD(span.cost_usd)}
                  {span.cost_estimated ? " (estimated)" : ""}
                </dd>
              </>
            )}
            {span.error_class && (
              <>
                <dt>error</dt>
                <dd className="mono sev-error">{span.error_class}</dd>
              </>
            )}
          </dl>
        </section>
      ) : (
        <div style={{ color: "var(--text-3)", fontSize: 12 }}>
          Select a span on the timeline to see its attributes and events.
        </div>
      )}

      {span && spanEvents.length > 0 && (
        <section aria-label="Span events">
          <h4 style={{ margin: "0 0 6px", fontSize: 11, color: "var(--text-3)", fontWeight: 600, letterSpacing: "0.05em", textTransform: "uppercase" }}>
            events on this span ({spanEvents.length})
          </h4>
          <ul style={{ listStyle: "none", margin: 0, padding: 0, display: "flex", flexDirection: "column", gap: 4 }}>
            {spanEvents.map((e) => (
              <li key={e.id}>
                <button
                  className="btn ghost"
                  style={{ justifyContent: "flex-start", width: "100%" }}
                  aria-pressed={selectedEvent === e.id}
                  onClick={() => onSelectEvent(e.id)}
                >
                  <span className="mono" style={{ fontSize: 11 }}>{e.type}</span>
                  <span className="num" style={{ fontSize: 10, color: "var(--text-3)" }}>
                    seq {e.seq ?? "?"}
                  </span>
                </button>
              </li>
            ))}
          </ul>
        </section>
      )}

      {span && spanEvents.length === 0 && (
        <div style={{ fontSize: 11.5, color: "var(--text-3)" }}>
          No events recorded on this span (partial telemetry or open span).
        </div>
      )}
    </div>
  );
}
