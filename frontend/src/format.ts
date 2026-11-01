/** Display formatters + tolerant JSON parsing. Missing values render as a
 * dash — never a fabricated zero. Kept dependency-free for unit tests. */

export const fmtMs = (ms?: number) =>
  ms === undefined || ms === null ? "—" : `${ms} ms`;

export const fmtNum = (n?: number) =>
  n === undefined || n === null ? "—" : String(n);

export const fmtUSD = (n?: number) =>
  n === undefined || n === null ? "—" : `$${n.toFixed(4)}`;

export function fmtTime(iso?: string) {
  if (!iso) return "—";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return d.toLocaleTimeString([], { hour12: false });
}

export function fmtDateTime(iso?: string) {
  if (!iso) return "—";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return d.toLocaleString([], {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hour12: false,
  });
}

/** Short id display (stable prefix/suffix) for dense tables. */
export function shortId(id?: string, n = 18) {
  if (!id) return "—";
  return id.length > n ? `${id.slice(0, n - 10)}…${id.slice(-6)}` : id;
}

/** Tolerant parse of the gate_json blob the API stores. The Go struct has
 * no json tags, so fields are PascalCase on the wire. */
export function parseGateJson(
  raw?: string,
): { require?: boolean; minPassRate?: number; minScore?: number } | null {
  if (!raw) return null;
  try {
    const g = JSON.parse(raw) as Record<string, unknown>;
    return {
      require: typeof g.Require === "boolean" ? g.Require : undefined,
      minPassRate:
        typeof g.MinPassRate === "number" ? g.MinPassRate : undefined,
      minScore: typeof g.MinScore === "number" ? g.MinScore : undefined,
    };
  } catch {
    return null;
  }
}
