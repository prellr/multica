import { describe, it, expect } from "vitest";
import {
  transcriptScrollReducer,
  isUpwardWheel,
  isNavigationIntentKey,
} from "@multica/ui/hooks/use-transcript-scroll";

// The DOM wiring of useTranscriptScroll (IntersectionObserver / ResizeObserver
// / scroll metrics) can't run in jsdom, so we test the pure intent rules that
// encode principles #1–3 ("never move against intent"). The engine lives in
// @multica/ui (no test runner of its own); this is the nearest package that
// can both import it and run vitest.

describe("transcriptScrollReducer", () => {
  it("any reader intent flips FOLLOWING → READING", () => {
    expect(transcriptScrollReducer("following", { type: "reader-intent" })).toBe(
      "reading",
    );
  });

  it("reaching the live edge re-enters FOLLOWING", () => {
    expect(
      transcriptScrollReducer("reading", { type: "reached-live-edge" }),
    ).toBe("following");
  });

  it("jump-to-latest re-enters FOLLOWING", () => {
    expect(transcriptScrollReducer("reading", { type: "jump-to-latest" })).toBe(
      "following",
    );
  });

  it("stays READING on further intent (no thrash back to follow)", () => {
    expect(transcriptScrollReducer("reading", { type: "reader-intent" })).toBe(
      "reading",
    );
  });

  it("FOLLOWING is idempotent under reached-live-edge", () => {
    expect(
      transcriptScrollReducer("following", { type: "reached-live-edge" }),
    ).toBe("following");
  });
});

describe("isUpwardWheel", () => {
  it("treats upward wheel (deltaY < 0) as intent", () => {
    expect(isUpwardWheel(-1)).toBe(true);
  });
  it("ignores downward / zero wheel (the sentinel re-enters follow)", () => {
    expect(isUpwardWheel(1)).toBe(false);
    expect(isUpwardWheel(0)).toBe(false);
  });
});

describe("isNavigationIntentKey", () => {
  it("up/page-up/home are intent", () => {
    expect(isNavigationIntentKey("ArrowUp", false)).toBe(true);
    expect(isNavigationIntentKey("PageUp", false)).toBe(true);
    expect(isNavigationIntentKey("Home", false)).toBe(true);
  });
  it("Shift+Space is intent (scrolls up); plain Space is not", () => {
    expect(isNavigationIntentKey(" ", true)).toBe(true);
    expect(isNavigationIntentKey(" ", false)).toBe(false);
  });
  it("down-direction keys are NOT intent (they walk toward the live edge)", () => {
    expect(isNavigationIntentKey("ArrowDown", false)).toBe(false);
    expect(isNavigationIntentKey("PageDown", false)).toBe(false);
    expect(isNavigationIntentKey("End", false)).toBe(false);
  });
});
