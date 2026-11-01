/* Types mirror the Go API JSON shapes (server/internal/api/helpers.go).
 * All fields optional where the server omits them; the UI renders blanks
 * honestly rather than inventing values. */

export type UsageSource = "provider" | "estimate" | string;

export interface RunRow {
  id: string;
  status?: string;
  agent_name?: string;
  agent_version?: string;
  model?: string;
  provider?: string;
  environment?: string;
  started_at?: string;
  ended_at?: string;
  duration_ms?: number;
  tokens_in?: number;
  tokens_out?: number;
  usage_source?: UsageSource;
  cost_usd?: number;
  cost_estimated?: boolean;
  retries?: number;
  event_count?: number;
  seq_max?: number;
  finding_count?: number;
  worst_severity?: string;
}

export interface SpanRow {
  id: string;
  run_id?: string;
  parent_span_id?: string;
  name?: string;
  kind?: string; // run | agent | model | tool | retrieval
  status?: string;
  agent_name?: string;
  agent_version?: string;
  tool_name?: string;
  tool_version?: string;
  tool_args_hash?: string;
  tool_schema_version?: string;
  model?: string;
  provider?: string;
  usage_source?: UsageSource;
  tokens_in?: number;
  tokens_out?: number;
  cost_usd?: number;
  cost_estimated?: boolean;
  retries?: number;
  error_class?: string;
  started_at?: string;
  ended_at?: string;
  duration_ms?: number;
  seq_start?: number;
  seq_end?: number;
}

export interface EventRow {
  id: string;
  run_id?: string;
  span_id?: string;
  parent_span_id?: string;
  seq?: number;
  type?: string;
  ts?: string;
  received_at?: string;
  agent_name?: string;
  agent_version?: string;
  model?: string;
  provider?: string;
  attributes?: Record<string, unknown>;
  payload_ref?: string;
}

export interface Finding {
  id: string;
  run_id: string;
  signature: string;
  kind: string; // tool-loop | state-oscillation | budget-exhaustion | failure-cluster | context-loss
  severity?: string;
  message?: string;
  evidence?: string[]; // event ids
  created_at?: string;
}

export interface Timeline {
  project: string;
  run: RunRow | null;
  spans: SpanRow[];
  events: EventRow[];
  findings: Finding[];
}

export interface SignatureRow {
  signature: string;
  kind?: string;
  tool_name?: string;
  count?: number;
  run_count?: number;
  first_seen?: string;
  last_seen?: string;
  agent_versions?: string;
  sample_event?: string;
  sample_run?: string;
}

export interface SignatureDetail {
  signature: string;
  info: SignatureRow | null;
  findings: Finding[];
}

export interface DatasetRow {
  id: string;
  name?: string;
  version?: string;
  description?: string;
  case_count?: number;
  created_at?: string;
}

export interface DatasetCase {
  id: string;
  task?: string;
  expected_tool?: string;
  expect_completion?: boolean;
  rubric?: string; // JSON string
}

export interface DatasetDetail {
  id: string;
  name?: string;
  version?: string;
  cases: DatasetCase[];
}

export interface EvalRunRow {
  id: string;
  dataset_id?: string;
  dataset_version?: string;
  agent_name?: string;
  agent_version?: string;
  model?: string;
  evaluator_id?: string;
  evaluator_version?: string;
  status?: string;
  total_cases?: number;
  passed?: number;
  avg_score?: number;
  score_source?: string;
  gate_failed?: boolean;
  gate_json?: string;
  started_at?: string;
  finished_at?: string;
}

export interface EvalResult {
  case_id: string;
  passed?: boolean;
  score?: number;
  status?: string;
  detail?: string;
  origin_run?: string;
}

export interface EvalDetail extends EvalRunRow {
  results: EvalResult[];
}

export interface ReplayCreateBody {
  origin_run: string;
  replay_run?: string;
  mode?: "mocked" | "live";
}

export interface ReplayCreated {
  id: string;
  origin_run?: string;
  replay_run?: string;
  status?: string;
  mode?: string;
  isolation?: string;
  step_count?: number;
  completed?: number;
  failed?: number;
  refused?: number;
  provenance?: unknown;
}

export interface ReplayJob {
  id: string;
  origin_run?: string;
  replay_run?: string;
  status?: string;
  mode?: string;
  step_count?: number;
  completed_steps?: number;
  failed_steps?: number;
  mock_ref?: string;
  created_at?: string;
}

export interface AlertRule {
  id: string;
  name?: string;
  detector: string;
  window_min?: number;
  threshold?: number;
  agent?: string;
  severity?: string;
  webhook_url?: string;
  enabled?: boolean;
  created_at?: string;
}

export interface UsageRow {
  project: string;
  run_count?: number;
  event_count?: number;
  queue_depth?: number;
  note?: string;
}
