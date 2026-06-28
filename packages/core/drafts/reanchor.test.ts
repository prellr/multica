import { describe, it, expect } from "vitest";
import {
  reanchor,
  similarity,
  type Anchor,
  EXACT_POS_TOLERANCE,
  SHIFTED_SIMILARITY_THRESHOLD,
  CHANGED_SIMILARITY_THRESHOLD,
} from "./reanchor";

/**
 * Re-anchoring engine tests. The engine is the novel, load-bearing piece of the
 * draft annotation layer, so these cover each classification branch explicitly:
 * unchanged, shifted-by-prepend, minor-edit-nearby, substantial-rewrite,
 * deleted-span, and duplicate-quote-disambiguated-by-context.
 */

// Helper: build an anchor whose posHint/context are derived from the position
// of `quote` in the ORIGINAL body, mirroring how the UI captures an anchor at
// creation time.
function anchorFor(body: string, quote: string, ctx = 16): Anchor {
  const pos = body.indexOf(quote);
  if (pos === -1) throw new Error(`test setup: quote not in original body: ${quote}`);
  return {
    quote,
    contextBefore: body.slice(Math.max(0, pos - ctx), pos),
    contextAfter: body.slice(pos + quote.length, pos + quote.length + ctx),
    posHint: pos,
  };
}

describe("similarity", () => {
  it("is 1 for identical strings and for two empty strings", () => {
    expect(similarity("hello", "hello")).toBe(1);
    expect(similarity("", "")).toBe(1);
  });

  it("decreases as strings diverge", () => {
    expect(similarity("quick brown fox", "quick brown fox")).toBe(1);
    expect(similarity("quick brown fox", "quick green fox")).toBeGreaterThan(0.5);
    expect(similarity("quick brown fox", "totally different")).toBeLessThan(0.5);
  });
});

describe("reanchor — unchanged body", () => {
  it("returns matched at the original range when the body is untouched", () => {
    const body = "The quick brown fox jumps over the lazy dog.";
    const anchor = anchorFor(body, "quick brown fox");
    const result = reanchor(body, anchor);
    expect(result.status).toBe("matched");
    if (result.status === "matched") {
      expect(body.slice(result.from, result.to)).toBe("quick brown fox");
    }
  });
});

describe("reanchor — shifted by prepend", () => {
  it("classifies a far move of identical text as shifted, not matched", () => {
    const original = "The quick brown fox jumps over the lazy dog.";
    const anchor = anchorFor(original, "quick brown fox");
    // Prepend a long paragraph so the quote moves well beyond EXACT_POS_TOLERANCE.
    const prefix =
      "An entirely new opening paragraph added before the original sentence, long enough to push the anchor far past the position tolerance. ";
    const body = prefix + original;
    const result = reanchor(body, anchor);
    expect(result.status).toBe("shifted");
    if (result.status === "shifted") {
      expect(body.slice(result.from, result.to)).toBe("quick brown fox");
      expect(result.from - anchor.posHint).toBeGreaterThan(EXACT_POS_TOLERANCE);
    }
  });

  it("keeps a tiny prepend (within tolerance) classified as matched", () => {
    const original = "The quick brown fox jumps over the lazy dog.";
    const anchor = anchorFor(original, "quick brown fox");
    const body = "Hi. " + original; // 4-char shift, within EXACT_POS_TOLERANCE
    const result = reanchor(body, anchor);
    expect(result.status).toBe("matched");
  });
});

describe("reanchor — minor edit nearby", () => {
  it("follows the quote when text just before it is lightly edited (still exact quote → shifted/matched)", () => {
    const original = "The quick brown fox jumps over the lazy dog.";
    const anchor = anchorFor(original, "brown fox");
    // Edit a word before the quote; the quote itself is unchanged, so it stays
    // an exact match — position barely moves → matched.
    const body = "The very quick brown fox jumps over the lazy dog.";
    const result = reanchor(body, anchor);
    expect(["matched", "shifted"]).toContain(result.status);
    if (result.status !== "orphaned") {
      expect(body.slice(result.from, result.to)).toBe("brown fox");
    }
  });

  it("classifies a small edit WITHIN the quoted span as changed", () => {
    const original = "Please review the quarterly revenue projection for accuracy.";
    const anchor = anchorFor(original, "quarterly revenue projection");
    // One word inside the quote is edited → no exact match, fuzzy finds it,
    // similarity is high-but-not-perfect.
    const body = "Please review the quarterly revenue forecast for accuracy.";
    const result = reanchor(body, anchor);
    expect(result.status).toBe("changed");
    if (result.status === "changed") {
      // The relocated window should overlap the edited span.
      const window = body.slice(result.from, result.to);
      expect(window).toContain("quarterly revenue");
    }
  });
});

describe("reanchor — substantial rewrite", () => {
  it("orphans the anchor when the span is rewritten beyond recognition", () => {
    const original = "The annual budget must be approved by the finance committee.";
    const anchor = anchorFor(original, "approved by the finance committee");
    // The whole sentence is replaced with unrelated text.
    const body = "Marketing will launch the new campaign next spring across all regions.";
    const result = reanchor(body, anchor);
    expect(result.status).toBe("orphaned");
  });

  it("keeps a partially-rewritten span as changed when still recognizable", () => {
    const original = "We should ship the feature before the end of the quarter.";
    const anchor = anchorFor(original, "ship the feature before the end of the quarter");
    // Several words changed but the backbone is intact → changed, not orphaned.
    const body = "We should ship the feature before the close of the fiscal quarter.";
    const result = reanchor(body, anchor);
    expect(result.status).toBe("changed");
  });
});

describe("reanchor — deleted span", () => {
  it("orphans when the quoted text is removed entirely", () => {
    const original = "Intro paragraph. The deprecated section explains the old flow. Outro.";
    const anchor = anchorFor(original, "The deprecated section explains the old flow");
    const body = "Intro paragraph. Outro.";
    const result = reanchor(body, anchor);
    expect(result.status).toBe("orphaned");
  });

  it("orphans an empty-quote anchor (degenerate)", () => {
    const result = reanchor("any body text", {
      quote: "",
      contextBefore: "",
      contextAfter: "",
      posHint: 0,
    });
    expect(result.status).toBe("orphaned");
  });
});

describe("reanchor — duplicate quote disambiguated by context", () => {
  it("re-anchors to the occurrence whose context matches, not merely the nearest", () => {
    // "status update" appears twice. The anchor was created on the SECOND one,
    // distinguished by its surrounding context ("weekly ... to the team").
    const body =
      "Daily status update for the standby crew. " +
      "Then later: the weekly status update goes to the team every Friday.";
    const firstIdx = body.indexOf("status update");
    const secondIdx = body.indexOf("status update", firstIdx + 1);
    expect(secondIdx).toBeGreaterThan(firstIdx);

    const anchor: Anchor = {
      quote: "status update",
      contextBefore: body.slice(secondIdx - 12, secondIdx), // "the weekly "
      contextAfter: body.slice(secondIdx + "status update".length, secondIdx + "status update".length + 16),
      // posHint deliberately points NEARER the first occurrence to prove context
      // wins over raw proximity.
      posHint: firstIdx,
    };

    const result = reanchor(body, anchor);
    expect(result.status === "matched" || result.status === "shifted").toBe(true);
    if (result.status !== "orphaned") {
      expect(result.from).toBe(secondIdx);
      expect(body.slice(result.from, result.to)).toBe("status update");
    }
  });

  it("falls back to position when context cannot disambiguate (identical surrounds)", () => {
    // Two identical occurrences with identical context — disambiguation must
    // then pick the one nearest posHint.
    const body = "x status update y -- x status update y";
    const firstIdx = body.indexOf("status update");
    const secondIdx = body.indexOf("status update", firstIdx + 1);
    const anchor: Anchor = {
      quote: "status update",
      contextBefore: "x ",
      contextAfter: " y",
      posHint: secondIdx, // nearest the second
    };
    const result = reanchor(body, anchor);
    expect(result.status !== "orphaned").toBe(true);
    if (result.status !== "orphaned") {
      expect(result.from).toBe(secondIdx);
    }
  });
});

describe("reanchor — threshold constants are sane", () => {
  it("orders the similarity thresholds correctly", () => {
    expect(SHIFTED_SIMILARITY_THRESHOLD).toBeGreaterThan(CHANGED_SIMILARITY_THRESHOLD);
    expect(CHANGED_SIMILARITY_THRESHOLD).toBeGreaterThan(0);
    expect(SHIFTED_SIMILARITY_THRESHOLD).toBeLessThanOrEqual(1);
  });
});
