# observatory frontend

React + TypeScript (Vite) workbench for the AI Agent Failure Observatory.
Three workspaces — Run Explorer, Failures, Evaluations (datasets, version
comparison, safe replay) — over the Go API in `../server`.

Every screen calls a real endpoint from `server/internal/api/router.go`;
the client in `src/api.ts` is the single place that knows the paths, so a
server-side change shows up as a type error, not a broken page. Demo data
served by the seeded demo project is labelled **sample** in the UI — it is
never presented as real analytics.

## Setup

```sh
pnpm install          # or npm install
cp .env.example .env  # optional — defaults target the seeded demo project
```

The dev server proxies `/v1` to the Go API (`OBS_API_TARGET`, default
`http://127.0.0.1:8080`). Run the server first:

```sh
cd ../server
go run ./cmd/observatory   # seeds project "demo" / key "demo-key"
```

Then:

```sh
pnpm dev                  # http://localhost:5173
```

## Environment

| Var | Default | Meaning |
| --- | --- | --- |
| `VITE_API_BASE` | *(empty)* | API origin; empty = same-origin dev proxy |
| `VITE_API_KEY` | `demo-key` | Bearer project key (demo project's seeded key) |
| `VITE_PROJECT_ID` | `demo` | Project id used in `/v1/projects/{pid}/…` paths |
| `OBS_API_TARGET` | `http://127.0.0.1:8080` | dev-proxy target |

## Commands

```sh
pnpm dev          # dev server
pnpm build        # type-check (tsc -b) + production bundle
pnpm preview      # serve the production bundle locally
pnpm typecheck    # tsc only
pnpm test         # vitest (unit tests for the spring math + formatting)
```

## Layout

- `src/api.ts` — typed client, one function per endpoint, verified against
  `server/internal/api/`.
- `src/types.ts` — wire shapes mirrored from the Go serializers.
- `src/App.tsx` — shell: project selector + key display, navigation,
  sample-data banner, cross-workspace jump into the Run Explorer.
- `src/workspaces/RunsWorkspace.tsx` — three-region Run Explorer workbench
  (outline / timeline / inspector), run filters (status, agent, version,
  time range).
- `src/workspaces/timeline/TimelineCanvas.tsx` — purpose-built SVG
  timeline: parent-child span rows, time axis, kind colour coding, failed
  spans, evidence markers; row virtualisation for long traces.
- `src/workspaces/timeline/RunInspector.tsx` — span/event inspector with
  raw event evidence and attributes (provider-reported vs estimated usage
  labels).
- `src/workspaces/FailuresWorkspace.tsx` — failure clusters + per-kind
  finding detail (tool-loop keys, oscillation fingerprints, budget,
  clustering) with links into the Run Explorer at the offending span.
- `src/workspaces/EvaluationsWorkspace.tsx` — datasets, side-by-side
  version comparison, safe replay controls (mocked default, live
  visibly refused).
- `src/workspaces/VersionCarousel.tsx` — the one permitted carousel:
  pointer drag with capture, release-velocity spring, interruptible and
  reversible, momentum-projected snapping, keyboard + prev/next, partial
  next-item affordance, reduced-motion fallback.
- `src/styles.css` — Graphite & Violet tokens (dark-first, designed light
  theme), the sparse trace-path/event-node background layer, dense
  workbench layout.
- `src/hooks.tsx` — `useAsync` and loading/empty/error/success primitives.

## Design notes (spec §11 + design brief)

- **Fonts**: IBM Plex Sans / IBM Plex Mono are self-hosted through
  `@fontsource/ibm-plex-sans` and `@fontsource/ibm-plex-mono` (installed
  as packages, no binary font files vendored). `src/styles.css` declares
  system fallbacks (`system-ui` / `ui-monospace`) so the UI still renders
  correctly if the packages fail to install.
- **Background**: `.app-bg` is a static, very-low-opacity SVG trace-path /
  event-node constellation drawn once in CSS (data-URI in `styles.css`).
  It never animates and never sits behind dense data without a solid
  surface. `prefers-reduced-motion` disables the remaining transitions.
- **Density**: 4px spacing scale, 1440px content frame, no card-in-card,
  6–8px radii, no hero.
- **Usage labels**: `usage_source` (provider vs estimate) is shown on
  every run row and span, per the MVP definition of done.

## Limitations (documented)

- No auth flow: keys come from `VITE_API_KEY` (or the shell's key
  override input). The server admin endpoints (`POST /v1/projects`,
  `POST /v1/projects/{pid}/keys`) exist but are intentionally not wrapped
  in the UI — creating projects is an out-of-band operation.
- Alert rules are managed server-side; the Failures workspace shows the
  rule-engine findings/signatures but there is no alert-rule editor UI
  yet (the endpoints exist in the API).
- Virtualisation of the timeline is simple windowing (rows near the
  scroll position render); it is not a full virtual list library.
- The carousel reads viewport width at layout time; resizing the window
  mid-gesture re-measures on the next frame.
