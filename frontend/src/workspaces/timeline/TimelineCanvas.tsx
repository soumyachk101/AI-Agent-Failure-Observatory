import { useEffect, useMemo, useRef, useState } from "react";
import type { SpanRow, Timeline } from "../../types";

export interface TimelineCanvasProps {
  timeline: Timeline;
  selectedSpan: string | null;
  selectedEvent: string | null;
  highlightEvents: string[];
  /** Selecting a span bar calls this with the span and, when the span is a
   * finding's evidence, the evidence event ids to keep highlighted. */
  onSelectSpan: (spanId: string, evidenceEventIds?: string[]) => void;
}

const LABEL_W = 190; // px reserved for span labels on the left
const AXIS_H = 26;
const ROW_H = 30;
const PADDING_X = 12;

interface LaidSpan {
  span: SpanRow;
  depth: number;
  row: number;
}

/** Purpose-built SVG timeline: one row per span (indented by parent-child
 * depth), a shared time axis, and evidence markers. Rows beyond VISIBLE_MAX
 * are virtualised — only the window around the scroll position renders —
 * so a 500-span trace does not animate 500 DOM nodes. */
export function TimelineCanvas({
  timeline,
  selectedSpan,
  selectedEvent,
  highlightEvents,
  onSelectSpan,
}: TimelineCanvasProps) {
  const [scrollTop, setScrollTop] = useState(0);
  const [vpH, setVpH] = useState(420);
  const [width, setWidth] = useState(800);
  const scrollRef = useRef<HTMLDivElement>(null);
  const measure = () => {
    if (scrollRef.current) {
      setVpH(Math.max(300, scrollRef.current.clientHeight));
      setWidth(Math.max(560, scrollRef.current.clientWidth));
    }
  };
  useEffect(() => {
    measure();
    window.addEventListener("resize", measure);
    return () => window.removeEventListener("resize", measure);
  }, []);

  const { rows, durationMs, t0 } = useMemo(
    () => buildLayout(timeline.spans),
    [timeline.spans],
  );
  const bodyH = rows.length ? rows.length * ROW_H : 0;

  const from = Math.max(0, Math.floor(scrollTop / ROW_H) - 8);
  const to = Math.min(rows.length, Math.ceil((scrollTop + vpH) / ROW_H) + 8);

  const xOf = (ms: number) =>
    LABEL_W + PADDING_X + (durationMs > 0 ? (ms / durationMs) * (width - LABEL_W - PADDING_X * 2) : 0);

  const ticks = useMemo(() => axisTicks(t0, durationMs, width), [t0, durationMs, width]);

  const svgW = width;
  const svgH = AXIS_H + bodyH;

  if (rows.length === 0) return null;

  return (
    <div className="timeline-wrap">
      <div
        ref={scrollRef}
        style={{ overflowY: "auto", maxHeight: 560, position: "relative" }}
        onScroll={(e) => setScrollTop((e.target as HTMLDivElement).scrollTop)}
      >
        <svg
          className="timeline-svg"
          width={svgW}
          height={svgH}
          role="img"
          aria-label="Span timeline"
          style={{ minWidth: 560 }}
        >
          {/* time axis */}
          <g className="tl-axis">
            {ticks.map((tk, i) => (
              <g key={i}>
                <line
                  x1={tk.x}
                  x2={tk.x}
                  y1={AXIS_H - 4}
                  y2={AXIS_H + bodyH}
                  stroke="var(--border-subtle)"
                  strokeDasharray="2 4"
                />
                <text x={tk.x + 3} y={12}>{tk.label}</text>
              </g>
            ))}
            <line x1={LABEL_W} x2={svgW - PADDING_X} y1={AXIS_H} y2={AXIS_H} stroke="var(--border-strong)" />
          </g>

          {/* evidence / finding markers on the axis */}
          {highlightEvents.map((id) => {
            const ev = timeline.events.find((e) => e.id === id);
            if (!ev) return null;
            const off = offsetMs(ev.ts, t0);
            if (off === null) return null;
            return (
              <polygon
                key={id}
                className="tl-marker"
                points={`${xOf(off)},${AXIS_H} ${xOf(off) - 4},${AXIS_H + 7} ${xOf(off) + 4},${AXIS_H + 7}`}
              >
                <title>{`evidence event ${id} @ seq ${ev.seq ?? "—"}`}</title>
              </polygon>
            );
          })}

          {/* virtualised rows */}
          {rows.slice(from, to).map((l, i) => {
            const abs = from + i;
            const y = AXIS_H + abs * ROW_H;
            const s = l.span;
            const off = offsetMs(s.started_at, t0);
            const end = offsetMs(s.ended_at, t0);
            const dur =
              off !== null && end !== null && end > off ? end - off : s.duration_ms || 0;
            const x = off !== null ? xOf(off) : LABEL_W + PADDING_X;
            const w = Math.max(6, xOf(off !== null ? off + dur : 0) - x);
            const isErr = s.status === "failed" || !!s.error_class;
            const isSel = selectedSpan === s.id;
            const label = spanLabel(s, l.depth);
            return (
              <g key={s.id} transform={`translate(0,${y})`}>
                <title>{`${s.id}: ${s.name || s.kind} [${s.status || "?"}]`}</title>
                <line
                  x1={4}
                  x2={LABEL_W}
                  y1={ROW_H / 2}
                  y2={ROW_H / 2}
                  stroke="var(--border-subtle)"
                />
                <text
                  className="tl-row-label"
                  x={LABEL_W - 8 - l.depth * 14}
                  y={ROW_H / 2 - 1}
                  textAnchor="end"
                >
                  {label}
                </text>
                <text
                  className="tl-row-sub"
                  x={LABEL_W - 8}
                  y={ROW_H / 2 + 11}
                  textAnchor="end"
                >
                  {subLabel(s)}
                </text>
                <rect
                  className={`tl-bar${isErr ? " error" : ""}${isSel ? " selected" : ""}`}
                  x={x}
                  y={ROW_H / 2 - 6}
                  width={w}
                  height={12}
                  rx={3}
                  fill={kindFill(s)}
                  stroke={isErr ? "var(--danger)" : "var(--border-strong)"}
                  strokeWidth={1}
                  role="button"
                  aria-label={`Inspect span ${s.id}`}
                  tabIndex={0}
                  onClick={() =>
                    onSelectSpan(s.id, evidenceForSpan(timeline, s))
                  }
                  onKeyDown={(e) => {
                    if (e.key === "Enter" || e.key === " ") {
                      e.preventDefault();
                      onSelectSpan(s.id, evidenceForSpan(timeline, s));
                    }
                  }}
                />
                {isSel && selectedEvent && (
                  <circle cx={x} cy={ROW_H / 2 + 14} r={2.5} fill="var(--accent-highlight)" />
                )}
              </g>
            );
          })}
        </svg>
      </div>

      <div className="legend" aria-label="Timeline legend">
        <span><span className="dot" style={{ background: "var(--surface-2)" }} />agent / run</span>
        <span><span className="dot" style={{ background: "var(--accent)" }} />model call</span>
        <span><span className="dot" style={{ background: "var(--info)" }} />tool call</span>
        <span><span className="dot" style={{ background: "var(--warning)" }} />retrieval / state</span>
        <span><span className="dot" style={{ background: "var(--danger)" }} />failed span</span>
      </div>
    </div>
  );
}

/* ---- layout helpers ---- */

function buildLayout(spans: SpanRow[]) {
  const byId = new Map<string, SpanRow>();
  spans.forEach((s) => byId.set(s.id, s));
  const children = new Map<string, SpanRow[]>();
  const roots: SpanRow[] = [];
  for (const s of spans) {
    const p = s.parent_span_id;
    if (p && byId.has(p)) {
      const arr = children.get(p) || [];
      arr.push(s);
      children.set(p, arr);
    } else {
      roots.push(s);
    }
  }
  // DFS ordering of spans, roots first (by start time), children by start time.
  const order: SpanRow[] = [];
  const place = (s: SpanRow) => {
    order.push(s);
    const kids = (children.get(s.id) || []).slice().sort(
      (a, b) => startMs(a.started_at) - startMs(b.started_at),
    );
    kids.forEach(place);
  };
  roots
    .slice()
    .sort((a, b) => startMs(a.started_at) - startMs(b.started_at))
    .forEach(place);

  // depth per span
  const depthOf = new Map<string, number>();
  const runDepth = (s: SpanRow, d: number) => {
    depthOf.set(s.id, d);
    (children.get(s.id) || []).forEach((k) => runDepth(k, d + 1));
  };
  roots.forEach((r) => runDepth(r, 0));

  let t0ms = Infinity;
  let maxEnd = 0;
  for (const s of spans) {
    const a = startMs(s.started_at);
    const b = s.ended_at ? startMs(s.ended_at) : a + (s.duration_ms || 0);
    if (a > 0 && a < t0ms) t0ms = a;
    if (b > maxEnd) maxEnd = b;
  }
  if (t0ms === Infinity) t0ms = 0;
  const laid: LaidSpan[] = order.map((s, i) => ({
    span: s,
    depth: depthOf.get(s.id) ?? 0,
    row: i,
  }));
  return { rows: laid, durationMs: Math.max(1, maxEnd - t0ms), t0: new Date(t0ms).toISOString() };
}

function startMs(iso?: string): number {
  if (!iso) return 0;
  const t = new Date(iso).getTime();
  return Number.isNaN(t) ? 0 : t;
}

function offsetMs(iso?: string, t0Iso?: string): number | null {
  if (!iso) return null;
  const a = startMs(iso);
  if (a === 0) return null;
  const t0 = startMs(t0Iso || new Date(0).toISOString());
  return a - t0;
}

function evidenceForSpan(tl: Timeline, s: SpanRow): string[] | undefined {
  const f = tl.findings.find(
    (f) =>
      f.run_id === s.run_id &&
      (f.evidence || []).some(
        (evId) => tl.events.find((e) => e.id === evId)?.span_id === s.id,
      ),
  );
  return f ? f.evidence : undefined;
}

function spanLabel(s: SpanRow, depth: number): string {
  const prefix = depth > 0 ? "└ " : "";
  const name =
    s.name ||
    (s.kind === "tool" ? s.tool_name : s.kind === "model" ? s.model : s.kind);
  return `${prefix}${s.kind}·${name}`;
}

function subLabel(s: SpanRow): string {
  const bits: string[] = [];
  if (s.tool_name) bits.push(s.tool_name);
  if (s.usage_source) bits.push(s.usage_source === "estimate" ? "est." : "provider");
  if (s.tokens_in) bits.push(`${s.tokens_in}tok`);
  if (s.duration_ms !== undefined) bits.push(`${s.duration_ms}ms`);
  return bits.slice(0, 3).join(" · ");
}

function kindFill(s: SpanRow): string {
  switch (s.kind) {
    case "model":
      return "var(--accent)";
    case "tool":
      return "var(--info)";
    case "retrieval":
    case "state":
      return "var(--warning)";
    default:
      return "var(--surface-2)";
  }
}

function axisTicks(t0: string, durationMs: number, width: number) {
  const t0ms = startMs(t0);
  const n = 5;
  const out: { x: number; label: string }[] = [];
  for (let i = 0; i <= n; i++) {
    const ms = (durationMs / n) * i;
    const d = new Date(t0ms + ms);
    out.push({
      x: LABEL_W + PADDING_X + (ms / Math.max(1, durationMs)) * (width - LABEL_W - PADDING_X * 2),
      label: d.toLocaleTimeString([], { hour12: false }),
    });
  }
  return out;
}
