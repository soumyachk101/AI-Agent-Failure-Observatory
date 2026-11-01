/* Minimal typed client for the Observatory Go API. Paths verified against
 * server/internal/api/router.go. Auth: Bearer project key on every request. */
import type {
  AlertRule,
  DatasetCase,
  DatasetDetail,
  DatasetRow,
  EvalDetail,
  EvalRunRow,
  Finding,
  ReplayCreated,
  ReplayCreateBody,
  ReplayJob,
  RunRow,
  SignatureDetail,
  SignatureRow,
  Timeline,
  UsageRow,
} from "./types";

const API_BASE = (import.meta.env.VITE_API_BASE as string | undefined) ?? "";
const defaultKey: string =
  (import.meta.env.VITE_API_KEY as string | undefined) ?? "demo-key";
export let API_KEY: string = defaultKey;
/** Runtime override used by the project-key field in the shell rail.
 * The server authenticates with whatever key was sent on the request, so
 * switching keys here changes which project all subsequent calls target. */
export function setApiKey(key: string) {
  API_KEY = key || defaultKey;
}
export const PROJECT_ID: string =
  (import.meta.env.VITE_PROJECT_ID as string | undefined) ?? "demo";

/** A key the UI recognises as the seeded demo project. Seeded demo data is
 * sample data and must be labelled as such in every view. */
export function isDemoKey(key: string): boolean {
  return key === "demo-key" || key === "obs-demo";
}

export class ApiError extends Error {
  status: number;
  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

async function req<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(`${API_BASE}${path}`, {
    ...init,
    headers: {
      Authorization: `Bearer ${API_KEY}`,
      "Content-Type": "application/json",
      ...(init?.headers ?? {}),
    },
  });
  if (!res.ok) {
    let msg = `${res.status} ${res.statusText}`;
    try {
      const body = await res.json();
      if (body && typeof body.error === "string") msg = body.error;
    } catch {
      /* body not JSON */
    }
    throw new ApiError(res.status, msg);
  }
  return res.json() as Promise<T>;
}

function post<T>(path: string, body: unknown): Promise<T> {
  return req<T>(path, {
    method: "POST",
    body: JSON.stringify(body),
  });
}

export interface ListRunsFilters {
  status?: string;
  agent?: string;
  agentVersion?: string;
  model?: string;
  environment?: string;
  limit?: number;
  offset?: number;
}

export const api = {
  // ---- runs ----
  listRuns: (f: ListRunsFilters = {}): Promise<RunRow[]> => {
    const q = new URLSearchParams();
    if (f.status) q.set("status", f.status);
    if (f.agent) q.set("agent", f.agent);
    if (f.agentVersion) q.set("agent_version", f.agentVersion);
    if (f.model) q.set("model", f.model);
    if (f.environment) q.set("environment", f.environment);
    if (f.limit !== undefined) q.set("limit", String(f.limit));
    if (f.offset !== undefined) q.set("offset", String(f.offset));
    const s = q.toString();
    return req<RunRow[]>(`/v1/projects/${PROJECT_ID}/runs${s ? `?${s}` : ""}`);
  },
  getRun: (runId: string): Promise<{ run: RunRow; findings: Finding[] }> =>
    req(`/v1/runs/${encodeURIComponent(runId)}`),
  timeline: (runId: string): Promise<Timeline> =>
    req(`/v1/runs/${encodeURIComponent(runId)}/timeline`),

  // ---- failures ----
  signatures: (): Promise<SignatureRow[]> =>
    req(`/v1/projects/${PROJECT_ID}/failures`),
  signatureDetail: (sig: string): Promise<SignatureDetail> =>
    req(`/v1/projects/${PROJECT_ID}/failures/${encodeURIComponent(sig)}`),

  // ---- evaluation ----
  datasets: (): Promise<DatasetRow[]> =>
    req(`/v1/projects/${PROJECT_ID}/datasets`),
  dataset: (id: string): Promise<DatasetDetail> =>
    req(`/v1/projects/${PROJECT_ID}/datasets/${encodeURIComponent(id)}`),
  createDataset: (body: {
    id?: string;
    name: string;
    version?: string;
    description?: string;
    cases?: DatasetCase[];
  }): Promise<{ id: string; version: string; case_count: number }> =>
    post(`/v1/projects/${PROJECT_ID}/datasets`, body),
  evaluations: (datasetId?: string): Promise<EvalRunRow[]> =>
    req(
      `/v1/projects/${PROJECT_ID}/evaluations${
        datasetId ? `?dataset_id=${encodeURIComponent(datasetId)}` : ""
      }`,
    ),
  evaluation: (id: string): Promise<EvalDetail> =>
    req(`/v1/projects/${PROJECT_ID}/evaluations/${encodeURIComponent(id)}`),
  createEvaluation: (body: {
    dataset_id: string;
    dataset_version?: string;
    agent_name: string;
    agent_version: string;
    model?: string;
    evaluator_id?: string;
    evaluator_version?: string;
  }): Promise<EvalRunRow> => post(`/v1/projects/${PROJECT_ID}/evaluations`, body),

  // ---- replay (safe: mocked tools only; live is refused by the server) ----
  createReplay: (body: ReplayCreateBody): Promise<ReplayCreated> =>
    post(`/v1/projects/${PROJECT_ID}/replays`, body),
  replay: (id: string): Promise<ReplayJob> =>
    req(`/v1/projects/${PROJECT_ID}/replays/${encodeURIComponent(id)}`),

  // ---- alerts / usage (project context) ----
  alertRules: (): Promise<AlertRule[]> =>
    req(`/v1/projects/${PROJECT_ID}/alerts`),
  usage: (): Promise<UsageRow> =>
    req(`/v1/projects/${PROJECT_ID}/usage`),
};
