import { describe, it, expect } from "vitest";
import {
  reanchor,
  similarity,
  type Anchor,
  EXACT_POS_TOLERANCE,
  CONTEXT_WINDOW,
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

// Edge-case regression locks. These passed before but weren't covered; pin them
// so a future engine change can't silently break boundary/Unicode behavior. The
// engine works on JS string indices (UTF-16 code units), so a surrogate-pair
// emoji occupies two units — the assertions verify the returned [from, to)
// slices back out to the original quote regardless.

describe("reanchor — quote at document boundaries", () => {
  it("anchors a quote at the very start of the doc", () => {
    const body = "Heading goes first then the rest of the paragraph follows.";
    const anchor: Anchor = {
      quote: "Heading goes first",
      contextBefore: "", // nothing precedes a start-of-doc selection
      contextAfter: " then the rest",
      posHint: 0,
    };
    const result = reanchor(body, anchor);
    expect(result.status).toBe("matched");
    if (result.status !== "orphaned") {
      expect(result.from).toBe(0);
      expect(body.slice(result.from, result.to)).toBe("Heading goes first");
    }
  });

  it("anchors a quote at the very end of the doc", () => {
    const body = "Intro sentence, then the closing words.";
    const quote = "the closing words.";
    const pos = body.indexOf(quote);
    const anchor: Anchor = {
      quote,
      contextBefore: body.slice(Math.max(0, pos - 12), pos),
      contextAfter: "", // nothing follows an end-of-doc selection
      posHint: pos,
    };
    const result = reanchor(body, anchor);
    expect(result.status).toBe("matched");
    if (result.status !== "orphaned") {
      expect(result.to).toBe(body.length);
      expect(body.slice(result.from, result.to)).toBe(quote);
    }
  });
});

describe("reanchor — unicode / multibyte", () => {
  it("anchors a multibyte (accented / CJK) quote and round-trips the slice", () => {
    const body = "Café déjà vu — 这是中文内容 — and back to ascii.";
    const quote = "déjà vu — 这是中文内容";
    const pos = body.indexOf(quote);
    const anchor: Anchor = {
      quote,
      contextBefore: body.slice(Math.max(0, pos - 8), pos),
      contextAfter: body.slice(pos + quote.length, pos + quote.length + 8),
      posHint: pos,
    };
    const result = reanchor(body, anchor);
    expect(result.status === "matched" || result.status === "shifted").toBe(true);
    if (result.status !== "orphaned") {
      expect(body.slice(result.from, result.to)).toBe(quote);
    }
  });

  it("anchors a quote containing emoji (surrogate pairs) and round-trips the slice", () => {
    const body = "Ship it 🚀🚀 today, celebrate 🎉 tomorrow.";
    const quote = "Ship it 🚀🚀 today";
    const pos = body.indexOf(quote);
    const anchor: Anchor = {
      quote,
      contextBefore: "",
      contextAfter: body.slice(pos + quote.length, pos + quote.length + 8),
      posHint: pos,
    };
    const result = reanchor(body, anchor);
    expect(result.status === "matched" || result.status === "shifted").toBe(true);
    if (result.status !== "orphaned") {
      // The slice must reproduce the emoji exactly — no surrogate splitting.
      expect(body.slice(result.from, result.to)).toBe(quote);
    }
  });
});

describe("reanchor — adjacent identical duplicates", () => {
  it("disambiguates back-to-back identical occurrences by posHint", () => {
    // Two adjacent identical spans with identical context on both sides; only
    // posHint can break the tie.
    const body = "go go go";
    const first = body.indexOf("go");
    const second = body.indexOf("go", first + 1); // index 3
    const anchorSecond: Anchor = {
      quote: "go",
      contextBefore: " ",
      contextAfter: " ",
      posHint: second,
    };
    const r2 = reanchor(body, anchorSecond);
    expect(r2.status !== "orphaned").toBe(true);
    if (r2.status !== "orphaned") expect(r2.from).toBe(second);

    const anchorFirst: Anchor = { quote: "go", contextBefore: "", contextAfter: " ", posHint: first };
    const r1 = reanchor(body, anchorFirst);
    expect(r1.status !== "orphaned").toBe(true);
    if (r1.status !== "orphaned") expect(r1.from).toBe(first);
  });
});

describe("reanchor — body shorter than one context window", () => {
  it("anchors correctly when the whole body is shorter than CONTEXT_WINDOW", () => {
    const body = "short body";
    expect(body.length).toBeLessThan(CONTEXT_WINDOW);
    const anchor: Anchor = {
      quote: "short",
      contextBefore: "",
      contextAfter: " body",
      posHint: 0,
    };
    const result = reanchor(body, anchor);
    expect(result.status).toBe("matched");
    if (result.status !== "orphaned") {
      expect(body.slice(result.from, result.to)).toBe("short");
    }
  });

  it("fuzzy-matches a tiny edited body shorter than CONTEXT_WINDOW without out-of-range scans", () => {
    // No exact match → fuzzy path; the windowed scan must clamp to the tiny body.
    const body = "tiny edited body";
    const anchor: Anchor = {
      quote: "tiny exact body",
      contextBefore: "",
      contextAfter: "",
      posHint: 0,
    };
    const result = reanchor(body, anchor);
    // It either relocates (changed) or orphans — never throws on the clamp.
    expect(["changed", "shifted", "orphaned"]).toContain(result.status);
  });
});
