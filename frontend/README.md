# AI Agent Failure Observatory — frontend

React + TypeScript (Vite) workbench for the AI Agent Failure Observatory.
Three workspaces — **Run Explorer**, **Failures**, **Evaluations** (datasets,
version comparison, safe replay) — over the Go API in `../server`.

Every screen calls a real endpoint that exists in
`server/internal/api/router.go`; the client in `src/api.ts` is the single
place that knows the paths, verified field-by-field against
`server/internal/api/helpers.go` and the Go serializers. The seeded demo
project (key `demo-key`) is explicitly sample data and the UI labels it as
such — it is never presented as real analytics.

## Setup

```sh
cd frontend
pnpm install            # or npm install
cp .env.example .env    # optional — defaults already target the demo project
```

Run the Go server first (it seeds the demo project, key, dataset and a
sample run when started with `--seed-demo`, which is the default):

```sh
cd ../server
go run ./cmd/observatory          # listens on :8080
```

## Dev server / build / test

```sh
cd frontend
pnpm dev          # http://localhost:5173, /v1 proxied to OBS_API_TARGET
pnpm build        # tsc -b + vite production bundle
pnpm preview      # serve the production bundle
pnpm typecheck    # type-check only
pnpm test         # vitest (spring math + formatters)
```

## Environment (`.env.example`)

| Var | Default | Meaning |
| --- | --- | --- |
| `VITE_API_BASE` | *(empty)* | API origin; empty = same-origin dev proxy |
| `VITE_API_KEY` | `demo-key` | Bearer project key |
| `VITE_PROJECT_ID` | `demo` | Project id in `/v1/projects/{pid}/…` |
| `OBS_API_TARGET` | `http://127.0.0.1:8080` | dev-proxy target |

## What lives where

- `src/api.ts` — typed client, one function per endpoint (paths from
  `server/internal/api/router.go`), runtime key override.
- `src/types.ts` — wire shapes mirrored from the Go serializers.
- `src/App.tsx` — shell: project + key display, workspace navigation,
  sample-data banner, cross-workspace "jump to run" handoff, error
  boundary.
- `src/workspaces/RunsWorkspace.tsx` — three-region Run Explorer workbench
  (outline / timeline / inspector) with status, agent, version and
  time-range filters.
- `src/workspaces/timeline/TimelineCanvas.tsx` — purpose-built SVG
  timeline: parent-child span hierarchy, time axis, kind colour coding,
  failed-span styling, evidence markers, windowed row virtualisation.
- `src/workspaces/timeline/RunInspector.tsx` — span/event inspector with
  raw event evidence, attributes and provider-reported vs estimated usage
  labels.
- `src/workspaces/FailuresWorkspace.tsx` — failure cluster table +
  per-signature finding detail (tool-loop keys, oscillation sequences,
  budget, clustering) with links into the Run Explorer at the offending
  span.
- `src/workspaces/EvaluationsWorkspace.tsx` — dataset management,
  evaluation runs (deterministic scoring, evidence-linked results, no
  fabricated statistics) and safe replay: mocked tools are the default and
  live replay is visibly *refused* (both in the UI and via the server's
  405 refusal, which is shown verbatim).
- `src/workspaces/VersionCarousel.tsx` + `src/spring.ts` — the one
  permitted carousel for version comparison: pointer capture drag,
  release-velocity spring (critically damped; small bounce only on
  flicks), momentum-projected snap selection, fully interruptible and
  reversible, keyboard + prev/next, partial next-item affordance,
  reduced-motion instant snap.
- `src/styles.css` — Graphite & Violet design tokens (dark-first,
  designed light theme), the sparse trace-path/event-node SVG background
  layer, dense workbench layout.

## Design notes (spec §11 + visual brief)

- **Fonts**: IBM Plex Sans/Mono are self-hosted through the
  `@fontsource/ibm-plex-sans` / `@fontsource/ibm-plex-mono` packages
  (imported in `src/main.tsx`). No binary font files are vendored in the
  repo; `src/styles.css` declares system fallbacks (`system-ui`,
  `ui-monospace`) so the UI still renders if the packages are missing.
  Tabular numerals are used for timings/counts/versions/costs.
- **Background**: `.app-bg` is a static, very low-opacity SVG
  trace-path/event-node constellation (data-URI in `styles.css`), drawn
  once, never animated, and always covered by a solid surface behind
  dense data. `prefers-reduced-motion` disables transitions.
- **Density**: 4px spacing scale, 1440px content frame, no card-in-card,
  restrained radii, no hero.
- **Usage labels**: `usage_source` (provider vs estimate) is shown on
  spans and the inspector, per the MVP definition of done.

## Documented limitations

- No login/RBAC flow: the key comes from `VITE_API_KEY` or the shell's
  key-override input (which re-authenticates live). The admin endpoints
  (`POST /v1/projects`, `POST /v1/projects/{pid}/keys`) are intentionally
  not wrapped in the UI.
- No alert-rule editor UI: alert rules and their evaluation are managed
  server-side; the Failures workspace shows the rule-engine
  findings/signatures only (the alert endpoints exist in the API but are
  not surfaced here).
- Timeline virtualisation is simple windowing around the scroll position,
  not a virtual-list library.
- The carousel re-measures viewport width on window resize; a resize
  mid-gesture settles on the next frame.
