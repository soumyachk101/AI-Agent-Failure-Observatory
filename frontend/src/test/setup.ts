// Vitest setup: rAF for the carousel spring, matchMedia for reduced-motion.
let reduce = false;

// Simple rAF shim so the spring loop can be exercised deterministically.
(globalThis as unknown as { requestAnimationFrame?: unknown }).requestAnimationFrame =
  (cb: FrameRequestCallback): unknown => setTimeout(() => cb(performance.now()), 16);
(globalThis as unknown as { cancelAnimationFrame?: unknown }).cancelAnimationFrame =
  (id: unknown): void => clearTimeout(id as ReturnType<typeof setTimeout>);

// matchMedia may be missing in jsdom depending on version.
const g = globalThis as unknown as {
  window?: { matchMedia?: (q: string) => { matches: boolean; media: string } };
};
if (g.window && !g.window.matchMedia) {
  g.window.matchMedia = (q: string) => ({
    matches: q.includes("reduce") ? reduce : false,
    media: q,
  });
}

// toggle from a spec, e.g. (globalThis as any).__setReduceMotion(true)
(globalThis as unknown as { __setReduceMotion?: (b: boolean) => void })
  .__setReduceMotion = (b: boolean) => {
    reduce = b;
  };
