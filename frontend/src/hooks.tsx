/* Shared data-fetching + UI primitives. Async states are explicit: loading /
 * empty / error / success. Errors surface specific messages (from the API
 * body) and never fabricate data. */
import { useCallback, useEffect, useRef, useState, type ReactNode } from "react";

export type Async<T> =
  | { status: "loading"; data: T | null }
  | { status: "error"; error: string; data: T | null }
  | { status: "ready"; data: T };

/** Use an async load that is cancel-safe: stale responses never overwrite
 * fresh ones. `key` re-triggers the load when it changes. */
export function useAsync<T>(fn: () => Promise<T>, ...deps: unknown[]) {
  const [state, setState] = useState<Async<T>>({ status: "loading", data: null });
  const seq = useRef(0);
  const fnRef = useRef(fn);
  fnRef.current = fn;

  const reload = useCallback(() => {
    const id = ++seq.current;
    setState((s) => ({ status: "loading", data: s.data }));
    fnRef
      .current()
      .then((data) => {
        if (id === seq.current) setState({ status: "ready", data });
      })
      .catch((e: unknown) => {
        if (id === seq.current)
          setState({
            status: "error",
            error: e instanceof Error ? e.message : String(e),
            data: null,
          });
      });
  }, deps);

  useEffect(() => {
    reload();
  }, [reload]);
  return [state, reload] as const;
}

export function Loading({ label = "Loading…" }: { label?: string }) {
  return (
    <div className="state" role="status" aria-live="polite">
      <div className="spinner" aria-hidden="true" />
      {label}
    </div>
  );
}

export function ErrorState({
  message,
  onRetry,
}: {
  message: string;
  onRetry?: () => void;
}) {
  return (
    <div className="state error" role="alert">
      <strong>Request failed</strong>
      <span className="hint">{message}</span>
      {onRetry && (
        <button className="btn" onClick={onRetry}>
          Retry
        </button>
      )}
    </div>
  );
}

export function EmptyState({
  title,
  hint,
  action,
}: {
  title: string;
  hint?: string;
  action?: ReactNode;
}) {
  return (
    <div className="state">
      <strong>{title}</strong>
      {hint && <span className="hint">{hint}</span>}
      {action}
    </div>
  );
}

/** Small badge for provider-reported vs estimated usage (MVP DoD: usage is
 * always labelled). */
export function UsageTag({ source }: { source?: string }) {
  if (!source || source === "provider")
    return <span className="usage-src">usage: provider-reported</span>;
  if (source === "estimate")
    return <span className="usage-src est">usage: estimated</span>;
  return <span className="usage-src">usage: {source}</span>;
}

export function SampleTag({ label = "sample" }: { label?: string }) {
  return <span className="sample-tag">{label}</span>;
}

/* Number/date formatting lives in format.ts (unit-tested, dependency-free).
 * Re-exported here so workspaces keep a single import surface. */
export { fmtMs, fmtNum, fmtUSD, fmtTime, fmtDateTime, shortId } from "./format";
