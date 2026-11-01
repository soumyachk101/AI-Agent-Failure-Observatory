import { Component, useEffect, useState } from "react";
import { RunsWorkspace } from "./workspaces/RunsWorkspace";
import { FailuresWorkspace } from "./workspaces/FailuresWorkspace";
import { EvaluationsWorkspace } from "./workspaces/EvaluationsWorkspace";
import { ApiError, API_KEY, PROJECT_ID, isDemoKey, setApiKey } from "./api";
import { SampleTag } from "./hooks";

type Workspace = "runs" | "failures" | "evaluations";

/** Cross-workspace handoff: a finding can deep-link into the Run Explorer
 * timeline at the offending span/event. */
export interface RunJump {
  runId: string;
  spanId?: string;
  eventIds?: string[];
  token: number; // increments to force re-jump to the same target
}

export function App() {
  const [workspace, setWorkspace] = useState<Workspace>("runs");
  const [jumpState, setJumpState] = useState<RunJump | null>(null);
  const [keyOverride, setKeyOverride] = useState("");

  // Applying a key override re-authenticates every subsequent request and
  // remounts the active workspace (keyed below) so data refetches.
  useEffect(() => {
    const t = setTimeout(() => setApiKey(keyOverride), 350);
    return () => clearTimeout(t);
  }, [keyOverride]);

  const openRun = (runId: string, spanId?: string, eventIds?: string[]) => {
    setWorkspace("runs");
    setJumpState({ runId, spanId, eventIds, token: Date.now() });
  };

  const activeKey = keyOverride || API_KEY;
  const demo = isDemoKey(activeKey);

  return (
    <div className="app">
      <div className="app-bg" aria-hidden="true" />
      <div className="shell">
        <aside className="rail" aria-label="Primary navigation">
          <div className="brand">
            <b>Agent Failure Observatory</b>
            trace · failure · evaluation workbench
          </div>

          <nav className="nav" aria-label="Workspaces">
            {(
              [
                ["runs", "Run Explorer"],
                ["failures", "Failures"],
                ["evaluations", "Evaluations"],
              ] as [Workspace, string][]
            ).map(([ws, label], i) => (
              <button
                key={ws}
                aria-current={workspace === ws ? "page" : undefined}
                onClick={() => {
                  setWorkspace(ws);
                  setJumpState(null);
                }}
              >
                <span className="nav-idx" aria-hidden="true">
                  {String(i + 1).padStart(2, "0")}
                </span>
                {label}
              </button>
            ))}
          </nav>

          <div className="project-box" aria-label="Project context">
            <span className="lbl">Project</span>
            <div className="key-chip">
              <span className="key">{PROJECT_ID}</span>
              {demo && <SampleTag />}
            </div>
            <span className="lbl" style={{ marginTop: 4 }}>
              API key
            </span>
            <div className="key-chip">
              <span className="key">{keyOverride || API_KEY}</span>
              {keyOverride && (
                <button
                  className="btn ghost"
                  style={{ padding: "0 4px", minHeight: 0 }}
                  onClick={() => setKeyOverride("")}
                  aria-label="Reset key to default"
                >
                  reset
                </button>
              )}
            </div>
            <input
              aria-label="Override project API key"
              placeholder="paste project key…"
              value={keyOverride}
              onChange={(e) => setKeyOverride(e.target.value)}
              style={{ marginTop: 4 }}
            />
            {keyOverride && (
              <span style={{ fontSize: 11, color: "var(--text-3)" }}>
                Requests now authenticate with the key pasted above; the view
                remounts when it changes.
              </span>
            )}
          </div>

          {demo && (
            <div className="sample-banner">
              <SampleTag label="sample data" />
              <span>
                This project is seeded with sample runs. Nothing shown is
                production analytics.
              </span>
            </div>
          )}
        </aside>

        <main className="workspace">
          {/* keyed remount so a key override refetches with the new tenant */}
          <div key={activeKey} style={{ display: "contents" }}>
            {workspace === "runs" && (
              <RunsWorkspace jump={jumpState} onJumpHandled={() => setJumpState(null)} />
            )}
            {workspace === "failures" && (
              <FailuresWorkspace onOpenRun={openRun} />
            )}
            {workspace === "evaluations" && (
              <EvaluationsWorkspace onOpenRun={(runId) => openRun(runId)} />
            )}
          </div>
        </main>
      </div>
    </div>
  );
}

/** Catches render errors and shows a concrete, non-decorative message. */
export class ErrorBoundary extends Component<
  { children: React.ReactNode },
  { error: string | null }
> {
  state = { error: null as string | null };
  static getDerivedStateFromError(e: unknown) {
    return {
      error:
        e instanceof ApiError
          ? `API ${e.status}: ${e.message}`
          : e instanceof Error
            ? e.message
            : String(e),
    };
  }
  render() {
    if (this.state.error) {
      return (
        <div className="content" role="alert">
          <h1>Render error</h1>
          <div className="state error">
            <span>{this.state.error}</span>
            <button className="btn" onClick={() => this.setState({ error: null })}>
              Reload view
            </button>
          </div>
        </div>
      );
    }
    return this.props.children ?? null;
  }
}

export function AppRoot() {
  return (
    <ErrorBoundary>
      <App />
    </ErrorBoundary>
  );
}
