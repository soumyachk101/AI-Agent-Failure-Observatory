/** Pure spring/momentum math for the version-comparison carousel.
 * Kept framework-free so it is unit-testable and the gesture handlers stay
 * thin. All functions are deterministic. */

/** Project the release position forward by the captured velocity
 * (items/sec × viewport px) with a short decay window, in px. */
export function projectMomentum(currentPx: number, velPxPerSec: number, decaySec = 0.18): number {
  return currentPx + velPxPerSec * decaySec;
}

/** From a projected px position, the snap index to land on. */
export function momentumLandingIndex(projectedPx: number, itemPx: number): number {
  if (itemPx <= 0) return 0;
  const i = Math.round(-projectedPx / itemPx);
  return i === 0 ? 0 : i; // normalize -0 to 0 (Object.is / toBe in tests)
}

/** Clamp an index into [min, max]. */
export function clampSpringTarget(i: number, min: number, max: number): number {
  return Math.max(min, Math.min(max, i));
}

/** Spring constants: critically damped default, light bounce on flicks.
 * `flick` is true when the release velocity exceeds the flick threshold. */
export function springParams(flick: boolean) {
  return {
    stiffness: 170,
    // 1.0 = critically damped; <1.0 adds a small overshoot
    dampingRatio: flick ? 0.72 : 1.0,
  };
}

/** Velocity (px/s) from two pointer samples. */
export function velocityFromSamples(dxPx: number, dtMs: number): number {
  const s = Math.max(1, dtMs) / 1000;
  return dxPx / s;
}
