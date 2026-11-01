import { describe, expect, it } from "vitest";
import {
  clampSpringTarget,
  momentumLandingIndex,
  projectMomentum,
  springParams,
  velocityFromSamples,
} from "./spring";
import { fmtMs, fmtNum, fmtUSD, parseGateJson, shortId } from "./format";

describe("spring math", () => {
  it("projects momentum px with a decay window", () => {
    expect(projectMomentum(10, 100, 0.18)).toBeCloseTo(28);
    expect(projectMomentum(0, 0, 0.18)).toBe(0);
  });

  it("computes a landing index from projected px", () => {
    expect(momentumLandingIndex(0, 100)).toBe(0);
    expect(momentumLandingIndex(-240, 100)).toBe(2);
    expect(momentumLandingIndex(-149, 100)).toBe(1);
  });

  it("clamps landing into the available range", () => {
    expect(clampSpringTarget(-5, 0, 3)).toBe(0);
    expect(clampSpringTarget(9, 0, 3)).toBe(3);
    expect(clampSpringTarget(2, 0, 3)).toBe(2);
  });

  it("is critically damped by default and bounces on flicks", () => {
    expect(springParams(false).dampingRatio).toBe(1.0);
    expect(springParams(true).dampingRatio).toBeLessThan(1.0);
  });

  it("derives velocity from pointer samples", () => {
    expect(velocityFromSamples(100, 100)).toBe(1000);
    expect(velocityFromSamples(10, 0)).toBeGreaterThan(0); // dt clamped to 1ms
  });
});

describe("formatters", () => {
  it("renders dashes for missing values, never fabricated zeros", () => {
    expect(fmtMs(undefined)).toBe("—");
    expect(fmtMs(42)).toBe("42 ms");
    expect(fmtNum(undefined)).toBe("—");
    expect(fmtNum(0)).toBe("0"); // a real zero is shown
    expect(fmtUSD(0.12345)).toBe("$0.1235");
    expect(fmtUSD(undefined)).toBe("—");
    expect(shortId(undefined)).toBe("—");
    expect(shortId("abcdef")).toBe("abcdef");
  });

  it("parses gate json tolerantly (PascalCase fields)", () => {
    expect(parseGateJson("not json")).toBeNull();
    expect(parseGateJson(undefined)).toBeNull();
    expect(parseGateJson('{"Require":true,"MinPassRate":0.9}')).toMatchObject({
      require: true,
      minPassRate: 0.9,
    });
  });
});
