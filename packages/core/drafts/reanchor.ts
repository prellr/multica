/**
 * Re-anchoring engine for the draft annotation layer (Drafts slice 1).
 *
 * An annotation is anchored to a span of the draft body by a quote-plus-context
 * fingerprint. As the body is edited the absolute character offset drifts, the
 * surrounding text changes, and sometimes the quoted span is rewritten or
 * deleted outright. This module relocates an anchor in a (possibly edited) body
 * and classifies how well it survived, so the UI can decide whether to silently
 * follow the move, flag the annotation as stale, or move it to an orphaned tray.
 *
 * This module is PURE: no React, no DOM, no ProseMirror. It operates on the raw
 * markdown body string and char offsets only. That is what makes it cheap to
 * unit-test exhaustively (see reanchor.test.ts) — the gnarly classification
 * logic lives here, away from the editor wiring.
 *
 * Algorithm (cheapest path first):
 *   1. Exact match near posHint. Scan for `quote` and pick the occurrence
 *      closest to posHint. If it lands within EXACT_POS_TOLERANCE of posHint →
 *      `matched`. If an exact occurrence exists but moved further → `shifted`
 *      (same text, new location).
 *   2. Fuzzy match. Use diff-match-patch `match_main` (a bitap fuzzy search) to
 *      find the best candidate location near posHint, widening the search by
 *      relaxing the match threshold/distance. Take the best candidate window,
 *      extract the substring of the same length as `quote`, and score its
 *      similarity to `quote` via a normalized Levenshtein ratio.
 *        - similarity >= SHIFTED_SIMILARITY_THRESHOLD → `shifted` (the text is
 *          essentially intact; it just moved and the surrounds shifted).
 *        - similarity >= CHANGED_SIMILARITY_THRESHOLD → `changed` (the span was
 *          materially edited but is still recognizably the same anchor).
 *        - below that, or no candidate → `orphaned`.
 *
 * All tunables live in the named constants below — one place to adjust the
 * engine's sensitivity.
 */

import DiffMatchPatch from "diff-match-patch";

/**
 * The persisted anchor. `quote` is the exact selected text at creation/last-move
 * time; `contextBefore`/`contextAfter` are short windows of surrounding text
 * used to disambiguate when the same quote appears more than once; `posHint` is
 * the char offset of the quote in the body at that time.
 */
export interface Anchor {
  quote: string;
  contextBefore: string;
  contextAfter: string;
  posHint: number;
}

/**
 * The outcome of re-anchoring.
 *   - matched: exact quote, at (or very near) the expected position.
 *   - shifted: same text, moved — follow it silently.
 *   - changed: the span was materially edited — keep following it, but the UI
 *     flags an OPEN annotation as "text changed".
 *   - orphaned: no acceptable location — the UI moves it to an orphaned tray
 *     (the annotation is never deleted by re-anchoring).
 * On a non-orphaned result, [from, to) is the new char range in the body.
 */
export type ReanchorResult =
  | { status: "matched" | "shifted" | "changed"; from: number; to: number }
  | { status: "orphaned" };

// --- Tunables (one place to adjust the engine's sensitivity) ----------------

/**
 * How far an exact match may sit from posHint and still count as `matched`
 * rather than `shifted`. Small typical edits before the anchor (a few words)
 * keep it "matched"; a larger move downgrades to "shifted".
 */
export const EXACT_POS_TOLERANCE = 24;

/**
 * Half-width of the context window scanned around posHint for the fuzzy search,
 * and the amount the window widens on each retry. The fuzzy matcher is biased
 * toward posHint via diff-match-patch's `Match_Distance`; this constant sizes
 * how aggressively we relax that bias when the first attempt finds nothing.
 */
export const CONTEXT_WINDOW = 256;

/**
 * Normalized-similarity cutoffs (1 = identical, 0 = completely different).
 * A fuzzy candidate at or above SHIFTED is treated as the same text that merely
 * moved; at or above CHANGED it is a materially edited but still-recognizable
 * span; below CHANGED the anchor is orphaned.
 */
export const SHIFTED_SIMILARITY_THRESHOLD = 0.9;
export const CHANGED_SIMILARITY_THRESHOLD = 0.5;

/**
 * diff-match-patch Match_Threshold: 0 = exact only, 1 = match anything. We start
 * fairly permissive so a moved-and-lightly-edited quote is found, then rely on
 * our own Levenshtein scoring to classify — the bitap threshold only gates
 * whether a candidate location is returned at all.
 */
const DMP_MATCH_THRESHOLD = 0.6;

/**
 * diff-match-patch Match_Distance: how far (in chars) from `loc` a match may be
 * before the location penalty makes the bitap score fall below threshold. Wide
 * enough to find a span that drifted by a paragraph or two.
 */
const DMP_MATCH_DISTANCE = 1000;

// ---------------------------------------------------------------------------

/**
 * Normalized similarity between two strings: 1 - (levenshtein / maxLen).
 * Returns 1 for two empty strings (degenerate but well-defined).
 */
export function similarity(a: string, b: string): number {
  if (a === b) return 1;
  const maxLen = Math.max(a.length, b.length);
  if (maxLen === 0) return 1;
  const dmp = new DiffMatchPatch();
  const distance = dmp.diff_levenshtein(dmp.diff_main(a, b));
  return 1 - distance / maxLen;
}

/**
 * Find every start index of `needle` in `haystack`. Empty needle yields no
 * matches (an empty quote has no anchorable span).
 */
function allIndexesOf(haystack: string, needle: string): number[] {
  if (!needle) return [];
  const out: number[] = [];
  let i = haystack.indexOf(needle);
  while (i !== -1) {
    out.push(i);
    i = haystack.indexOf(needle, i + 1);
  }
  return out;
}

/**
 * Score how well an exact occurrence at `index` matches the anchor's context.
 * Higher is better. Used to disambiguate when the same quote appears more than
 * once: the occurrence whose surrounding text best matches contextBefore /
 * contextAfter wins, even if another occurrence is closer to posHint.
 */
function contextScore(body: string, index: number, anchor: Anchor): number {
  const { quote, contextBefore, contextAfter } = anchor;
  const beforeActual = body.slice(Math.max(0, index - contextBefore.length), index);
  const afterActual = body.slice(index + quote.length, index + quote.length + contextAfter.length);
  // Average the before/after context similarity. When the anchor has no stored
  // context (e.g. a selection at the very start of the doc), that side scores a
  // neutral 1 so it doesn't drag the disambiguation.
  const beforeScore = contextBefore ? similarity(contextBefore, beforeActual) : 1;
  const afterScore = contextAfter ? similarity(contextAfter, afterActual) : 1;
  return (beforeScore + afterScore) / 2;
}

/**
 * Pick the best exact occurrence of the quote. Disambiguation is context-first,
 * then position: among occurrences whose context score ties (within a small
 * epsilon), the one nearest posHint wins. This is what makes a duplicate quote
 * re-anchor to the right instance — context disambiguates; position breaks ties.
 */
function bestExactOccurrence(
  body: string,
  anchor: Anchor,
  occurrences: number[],
): number {
  let best = occurrences[0] ?? 0;
  let bestCtx = contextScore(body, best, anchor);
  let bestDist = Math.abs(best - anchor.posHint);
  for (let k = 1; k < occurrences.length; k++) {
    const idx = occurrences[k];
    if (idx === undefined) continue;
    const ctx = contextScore(body, idx, anchor);
    const dist = Math.abs(idx - anchor.posHint);
    // Context dominates; position is the tie-breaker within a small epsilon.
    if (ctx > bestCtx + 0.001 || (Math.abs(ctx - bestCtx) <= 0.001 && dist < bestDist)) {
      best = idx;
      bestCtx = ctx;
      bestDist = dist;
    }
  }
  return best;
}

/**
 * Relocate an anchor in `body` and classify the result.
 */
export function reanchor(body: string, anchor: Anchor): ReanchorResult {
  const { quote, posHint } = anchor;

  // A degenerate anchor (no quote) can never be relocated.
  if (!quote) return { status: "orphaned" };

  // 1. Exact match path. Cheap and the common case.
  const exactOccurrences = allIndexesOf(body, quote);
  if (exactOccurrences.length > 0) {
    const idx = bestExactOccurrence(body, anchor, exactOccurrences);
    const status =
      Math.abs(idx - posHint) <= EXACT_POS_TOLERANCE ? "matched" : "shifted";
    return { status, from: idx, to: idx + quote.length };
  }

  // 2. Fuzzy match path. The quote no longer appears verbatim — either it moved
  // far and the surrounds changed, or the span itself was edited.
  const dmp = new DiffMatchPatch();
  dmp.Match_Threshold = DMP_MATCH_THRESHOLD;
  dmp.Match_Distance = DMP_MATCH_DISTANCE;

  // diff-match-patch caps patterns at Match_MaxBits (typically 32) chars for the
  // bitap search. For a long quote, match on a representative head slice to find
  // the candidate location, then score the full-length window there.
  const probe = quote.length > dmp.Match_MaxBits ? quote.slice(0, dmp.Match_MaxBits) : quote;
  const loc = Math.min(Math.max(0, posHint), body.length);
  const matchIndex = dmp.match_main(body, probe, loc);

  if (matchIndex === -1) {
    return { status: "orphaned" };
  }

  // Score the candidate by comparing the same-length window at matchIndex
  // against the full quote. Also probe a few nearby alignments, because the
  // bitap location can land a character or two off when the leading context was
  // edited; the best-scoring alignment within CONTEXT_WINDOW wins.
  let bestFrom = matchIndex;
  let bestSim = -1;
  const lo = Math.max(0, matchIndex - CONTEXT_WINDOW);
  const hi = Math.min(body.length, matchIndex + CONTEXT_WINDOW);
  for (let from = lo; from <= hi; from++) {
    const window = body.slice(from, from + quote.length);
    if (!window) continue;
    const sim = similarity(quote, window);
    if (sim > bestSim) {
      bestSim = sim;
      bestFrom = from;
      if (sim === 1) break; // can't do better
    }
  }

  const to = bestFrom + quote.length;
  if (bestSim >= SHIFTED_SIMILARITY_THRESHOLD) {
    return { status: "shifted", from: bestFrom, to };
  }
  if (bestSim >= CHANGED_SIMILARITY_THRESHOLD) {
    return { status: "changed", from: bestFrom, to };
  }
  return { status: "orphaned" };
}
