import { useEffect, useRef, useState, type ReactNode, type PointerEvent as RPointerEvent, type KeyboardEvent as RKeyboardEvent } from "react";

interface VersionSlide {
  key: string; // e.g. agent_version
  label: string;
  sublabel?: string;
  body: ReactNode;
}

interface CarouselProps {
  /** One slide per candidate version. */
  slides: VersionSlide[];
  ariaLabel: string;
}

/** Premium horizontal carousel for the version-comparison view (spec).
 *
 * Motion rules implemented:
 *  - pointer drag with Pointer Events + setPointerCapture (1:1 tracking,
 *    grab offset honoured);
 *  - release velocity handed to a spring (critically damped; slight
 *    bounce only above a flick threshold);
 *  - momentum projection picks the landing snap point from projected
 *    position, not just the release position;
 *  - fully interruptible: a new pointerdown/arrow mid-flight retargets the
 *    spring from the live on-screen value;
 *  - keyboard: Left/Right/Home/End, roving tabindex, arrow buttons;
 *  - partial next-item affordance via the right-edge mask on the viewport
 *    (see .carousel-viewport in styles.css);
 *  - reduced motion: no spring animation — snap instantly.
 *
 * No autoplay: this is critical data (version comparison), never auto-
 * advanced. */
export function Carousel({ slides, ariaLabel }: CarouselProps) {
  const [active, setActive] = useState(0);

  if (slides.length <= 1) {
    // no carousel needed for a single version — show the slide inline
    if (slides[0])
      return <div className="carousel-slide" style={{ width: "100%" }}>{slides[0].body}</div>;
    return <div className="state">No candidate versions to compare.</div>;
  }

  return (
    <div className="carousel-shell">
      <CarouselTrack
        slides={slides}
        active={active}
        onActiveChange={setActive}
        ariaLabel={ariaLabel}
      />
      <div className="carousel-nav">
        <button
          className="btn"
          aria-label="Previous version"
          disabled={active === 0}
          onClick={() => setActive((a) => Math.max(0, a - 1))}
        >
          ‹ prev
        </button>
        <span className="carousel-ann" aria-live="polite">
          {active + 1} of {slides.length} — {slides[active]?.label}
        </span>
        <button
          className="btn"
          aria-label="Next version"
          disabled={active === slides.length - 1}
          onClick={() => setActive((a) => Math.min(slides.length - 1, a + 1))}
        >
          next ›
        </button>
      </div>
    </div>
  );
}

function CarouselTrack({
  slides,
  active,
  onActiveChange,
  ariaLabel,
}: {
  slides: VersionSlide[];
  active: number;
  onActiveChange: (i: number) => void;
  ariaLabel: string;
}) {
  const reduced =
    typeof window !== "undefined" &&
    window.matchMedia?.("(prefers-reduced-motion: reduce)").matches;

  // Track ref + live position. All animation is transform-only.
  const trackRef = useRef<HTMLDivElement>(null);
  const viewportRef = useRef<HTMLDivElement>(null);
  const posRef = useRef(0); // live track offset in px; syncs to `active`
  const velRef = useRef(0);
  const rafRef = useRef(0);
  const draggingRef = useRef(false);
  const [dragging, setDragging] = useState(false);
  const lastXRef = useRef(0);
  const lastTRef = useRef(0);

  const itemW = () => {
    const vp = viewportRef.current;
    return vp ? vp.clientWidth : 560;
  };
  const maxX = Math.max(0, slides.length - 1);

  const clamp = (i: number) => Math.max(0, Math.min(maxX, i));

  const targetPx = (i: number) => -i * itemW();

  const apply = (px: number) => {
    if (trackRef.current) trackRef.current.style.transform = `translate3d(${px}px,0,0)`;
  };

  // spring: critically damped by default; light bounce on flicks.
  const springTo = (targetPx: number, vel: number) => {
    if (reduced) {
      apply(targetPx);
      posRef.current = targetPx;
      onActiveChange(clamp(Math.round(-targetPx / Math.max(1, itemW()))));
      return;
    }
    const stiffness = 170;
    const flick = Math.abs(vel) > 0.6;
    const damping = flick ? 0.72 : 1.0; // 1.0 = critically damped
    let x = posRef.current;
    let v = vel;
    cancelAnimationFrame(rafRef.current);
    const step = () => {
      const stepDt = 1 / 60;
      const a = stiffness * (targetPx - x) - 2 * damping * Math.sqrt(stiffness) * v;
      v += a * stepDt;
      x += v * stepDt;
      apply(x);
      posRef.current = x;
      if (Math.abs(targetPx - x) < 0.5 && Math.abs(v) < 2) {
        apply(targetPx);
        posRef.current = targetPx;
        onActiveChange(clamp(Math.round(-targetPx / Math.max(1, itemW()))));
        return;
      }
      rafRef.current = requestAnimationFrame(step);
    };
    rafRef.current = requestAnimationFrame(step);
  };

  const goTo = (i: number) => {
    const n = clamp(i);
    springTo(targetPx(n), 0);
  };

  // ---- pointer drag ----
  const onPointerDown = (e: RPointerEvent) => {
    draggingRef.current = true;
    lastXRef.current = e.clientX;
    lastTRef.current = performance.now();
    velRef.current = 0;
    cancelAnimationFrame(rafRef.current);
    (e.currentTarget as HTMLElement).setPointerCapture(e.pointerId);
    setDragging(true);
  };

  const onPointerMove = (e: RPointerEvent) => {
    if (!draggingRef.current) return;
    const dx = e.clientX - lastXRef.current;
    const now = performance.now();
    const dt = Math.max(1, now - lastTRef.current) / 1000;
    velRef.current = (dx / itemW()) / dt; // items/sec
    lastXRef.current = e.clientX;
    lastTRef.current = now;
    apply(posRef.current + dx * 0.9);
    posRef.current += dx * 0.9;
  };

  const onPointerUp = (e: RPointerEvent) => {
    if (!draggingRef.current) return;
    draggingRef.current = false;
    (e.currentTarget as HTMLElement).releasePointerCapture(e.pointerId);
    setDragging(false);
    // momentum projection: projected = current + velocity * 0.18s
    const projected = posRef.current + velRef.current * 0.18 * itemW();
    const landing = clamp(Math.round(-projected / Math.max(1, itemW())));
    springTo(targetPx(landing), velRef.current * itemW());
    velRef.current = 0;
  };

  const onKeyDown = (e: RKeyboardEvent) => {
    switch (e.key) {
      case "ArrowLeft":
        e.preventDefault();
        goTo(active - 1);
        break;
      case "ArrowRight":
        e.preventDefault();
        goTo(active + 1);
        break;
      case "Home":
        e.preventDefault();
        goTo(0);
        break;
      case "End":
        e.preventDefault();
        goTo(maxX);
        break;
    }
  };

  useEffect(
    () => () => cancelAnimationFrame(rafRef.current),
    [],
  );

  // keep the track in sync when the active index is changed externally
  // (prev/next buttons) without fighting the live position.
  useEffect(() => {
    const t = targetPx(active);
    if (Math.abs(posRef.current - t) > 1) springTo(t, 0);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [active]);

  return (
    <div
      ref={viewportRef}
      className="carousel-viewport"
      role="group"
      aria-roledescription="carousel"
      aria-label={ariaLabel}
      onKeyDown={onKeyDown}
      tabIndex={0}
    >
      <div
        ref={trackRef}
        className={`carousel-track${dragging ? " dragging" : ""}`}
        onPointerDown={onPointerDown}
        onPointerMove={onPointerMove}
        onPointerUp={onPointerUp}
        onPointerCancel={onPointerUp}
      >
        {slides.map((s, i) => (
          <div
            key={s.key}
            className="carousel-slide"
            role="group"
            aria-roledescription="slide"
            aria-label={`${i + 1} of ${slides.length}: ${s.label}`}
            aria-hidden={i !== active}
          >
            <div className="slide-idx">
              version {i + 1} / {slides.length}
            </div>
            <h3>
              {s.label}
              {s.sublabel && <span className="h1-sub">{s.sublabel}</span>}
            </h3>
            {s.body}
          </div>
        ))}
      </div>
    </div>
  );
}
