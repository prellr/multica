"use client";

import { useCallback, useMemo, useRef } from "react";
import type { Editor } from "@tiptap/core";
import { reanchor, type Anchor } from "@multica/core/drafts";
import { useUpdateDraftAnnotation } from "@multica/core/drafts";
import type { DraftAnnotation } from "@multica/core/types";
import { buildDocTextIndex } from "./text-position";
import type { AnnotationDecorationRange } from "./decoration-extension";

/**
 * How much surrounding text to capture / re-capture as an anchor's context.
 * Long enough to disambiguate a duplicated quote, short enough to stay cheap.
 */
const CONTEXT_LEN = 32;

/**
 * The re-anchored placement of a single annotation against the current document
 * text. `status` is the engine's classification; `range` is the flat-text
 * [from, to) span (absent when orphaned).
 */
export interface AnchoredAnnotation {
  annotation: DraftAnnotation;
  status: "matched" | "shifted" | "changed" | "orphaned";
  /** Flat-text range; undefined when orphaned. */
  range?: { from: number; to: number };
  /** True when the annotation is OPEN and its text materially changed. */
  flaggedChanged: boolean;
}

/**
 * The product of re-anchoring the whole annotation list against the editor's
 * current text: decoration ranges to paint, the per-annotation postures (for
 * pins / the changed flag), and the orphaned set (for the tray).
 */
export interface AnchoringResult {
  anchored: AnchoredAnnotation[];
  decorationRanges: AnnotationDecorationRange[];
  orphaned: DraftAnnotation[];
}

/**
 * Build an anchor (quote + context + posHint) from a flat-text [from, to) span.
 * Used at annotation-creation time to fingerprint the selection.
 */
export function buildAnchorFromSelection(text: string, from: number, to: number): Anchor {
  return {
    quote: text.slice(from, to),
    contextBefore: text.slice(Math.max(0, from - CONTEXT_LEN), from),
    contextAfter: text.slice(to, to + CONTEXT_LEN),
    posHint: from,
  };
}

/**
 * Re-anchoring controller for the annotation surface.
 *
 * Given the editor and the annotation list, this:
 *   1. Projects the doc to flat text (buildDocTextIndex).
 *   2. Re-anchors every annotation through the pure engine (reanchor).
 *   3. Applies the tiered posture:
 *        - matched / shifted → follow silently.
 *        - changed (OPEN annotation) → flag "text changed".
 *        - orphaned → route to the tray (never delete).
 *   4. Produces decoration ranges (text offsets converted to PM positions).
 *   5. Persists a moved anchor back to the server (PATCH) when the engine
 *      reports the quote/context/pos_hint changed, so the stored anchor tracks
 *      the document — but only for non-orphaned moves, and de-duped so we don't
 *      PATCH the same move repeatedly.
 *
 * recompute() is called on a debounce after edits and whenever the annotation
 * list changes; it is pure with respect to React state (returns the result) and
 * fires the persistence side effect internally.
 */
export function useAnnotationAnchoring(
  wsId: string,
  draftId: string,
  annotations: DraftAnnotation[],
) {
  const updateAnnotation = useUpdateDraftAnnotation(wsId, draftId);

  // Remember the last anchor we persisted per annotation so a stable doc
  // doesn't trigger repeated identical PATCHes.
  const lastPersistedRef = useRef<Map<string, string>>(new Map());

  const persistMove = useCallback(
    (annotation: DraftAnnotation, text: string, from: number, to: number) => {
      const nextQuote = text.slice(from, to);
      const nextBefore = text.slice(Math.max(0, from - CONTEXT_LEN), from);
      const nextAfter = text.slice(to, to + CONTEXT_LEN);
      const fingerprint = `${from}:${nextQuote}:${nextBefore}:${nextAfter}`;
      if (lastPersistedRef.current.get(annotation.id) === fingerprint) return;
      // Nothing actually moved relative to what the server already holds.
      if (
        annotation.pos_hint === from &&
        annotation.quote === nextQuote &&
        annotation.context_before === nextBefore &&
        annotation.context_after === nextAfter
      ) {
        lastPersistedRef.current.set(annotation.id, fingerprint);
        return;
      }
      lastPersistedRef.current.set(annotation.id, fingerprint);
      updateAnnotation.mutate({
        id: annotation.id,
        patch: {
          quote: nextQuote,
          context_before: nextBefore,
          context_after: nextAfter,
          pos_hint: from,
        },
      });
    },
    [updateAnnotation],
  );

  const recompute = useCallback(
    (editor: Editor): AnchoringResult => {
      const { text, offsetToPos } = buildDocTextIndex(editor.state.doc);

      const anchored: AnchoredAnnotation[] = [];
      const decorationRanges: AnnotationDecorationRange[] = [];
      const orphaned: DraftAnnotation[] = [];

      for (const annotation of annotations) {
        const anchor: Anchor = {
          quote: annotation.quote,
          contextBefore: annotation.context_before,
          contextAfter: annotation.context_after,
          posHint: annotation.pos_hint,
        };
        const result = reanchor(text, anchor);

        if (result.status === "orphaned") {
          anchored.push({ annotation, status: "orphaned", flaggedChanged: false });
          orphaned.push(annotation);
          continue;
        }

        const flaggedChanged = result.status === "changed" && annotation.state === "open";
        anchored.push({
          annotation,
          status: result.status,
          range: { from: result.from, to: result.to },
          flaggedChanged,
        });

        decorationRanges.push({
          from: offsetToPos(result.from),
          to: offsetToPos(result.to),
          id: annotation.id,
          type: annotation.type,
          state: annotation.state,
          changed: flaggedChanged,
        });

        // Persist the move so the stored anchor tracks the document. Skip
        // `matched` (it's already where the server thinks it is) to avoid
        // needless writes; `shifted`/`changed` get persisted.
        if (result.status === "shifted" || result.status === "changed") {
          persistMove(annotation, text, result.from, result.to);
        }
      }

      return { anchored, decorationRanges, orphaned };
    },
    [annotations, persistMove],
  );

  // A stable signature of the annotation list so callers can memoize a recompute
  // on list changes (anchors only — message/state changes don't move ranges,
  // but state flips the `changed` flag, so include state).
  const annotationsSignature = useMemo(
    () =>
      annotations
        .map((a) => `${a.id}:${a.pos_hint}:${a.quote}:${a.state}`)
        .join("|"),
    [annotations],
  );

  return { recompute, annotationsSignature };
}
