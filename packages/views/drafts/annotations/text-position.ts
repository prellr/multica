import type { Node as PMNode } from "@tiptap/pm/model";

/**
 * Bridge between the draft annotation engine (which works on a flat text string
 * and char offsets — see @multica/core/drafts reanchor) and ProseMirror (which
 * addresses content by document positions).
 *
 * The engine never sees ProseMirror. The editor surface projects the document
 * to a flat text string with one "\n" per text-block boundary, captures /
 * re-anchors annotation spans against THAT string, and uses the index built
 * here to turn a [from, to) text range back into ProseMirror positions for
 * decoration rendering.
 *
 * Working in the editor's plain-text projection (rather than the persisted
 * markdown source) keeps anchoring consistent: the same projection is used at
 * capture time and at re-anchor time, so markdown syntax characters (#, *, etc.)
 * never perturb offsets.
 */

export interface DocTextIndex {
  /** The flat text projection of the document. */
  text: string;
  /**
   * Map a char offset in {@link text} to a ProseMirror position. Clamps to the
   * valid range. A text offset that falls on an inter-block "\n" maps to the
   * end of the preceding block's text.
   */
  offsetToPos: (offset: number) => number;
  /**
   * Inverse of {@link offsetToPos}: map a ProseMirror position to a char offset
   * in {@link text}. This is what capture time MUST use to slice the selected
   * span out of the projection — deriving the selection via
   * `doc.textBetween(from, to, sep)` uses a DIFFERENT block-separator
   * convention (it inserts a separator at every block-leaf boundary, including
   * list items) and so produces a string that does not exist verbatim in this
   * projection. Going pos → offset → `text.slice` keeps capture and re-anchor on
   * one projection.
   */
  posToOffset: (pos: number) => number;
}

interface Segment {
  /** Inclusive start offset of this text node within the flat text. */
  textStart: number;
  /** Length of this text node's string. */
  length: number;
  /** ProseMirror position of the first character of this text node. */
  pos: number;
}

/**
 * Build the flat-text projection of a ProseMirror document plus an
 * offset-to-position index.
 *
 * Block boundaries: a "\n" is emitted between consecutive top-level block
 * children of the doc, matching how a reader perceives paragraph breaks. This
 * keeps the projection stable and human-meaningful for anchoring (the same
 * shape the engine's quote/context was captured from).
 */
export function buildDocTextIndex(doc: PMNode): DocTextIndex {
  let text = "";
  const segments: Segment[] = [];

  doc.forEach((block, blockOffset, blockIndex) => {
    if (blockIndex > 0) text += "\n";
    // blockOffset is the position BEFORE the block; +1 enters the block.
    collectBlockText(block, blockOffset + 1, (str, pos) => {
      segments.push({ textStart: text.length, length: str.length, pos });
      text += str;
    });
  });

  const offsetToPos = (offset: number): number => {
    if (segments.length === 0) return 1; // empty doc — position inside the first block
    const clamped = Math.max(0, Math.min(offset, text.length));
    // Find the segment containing `clamped`. Linear scan is fine — docs are
    // small and this runs on a debounce, not per keystroke per character.
    for (const seg of segments) {
      const segEnd = seg.textStart + seg.length;
      if (clamped < segEnd || (clamped === segEnd && seg === segments[segments.length - 1])) {
        if (clamped < seg.textStart) {
          // Falls in an inter-block gap before this segment — clamp to the end
          // of the previous segment (or this segment's start if first).
          return seg.pos;
        }
        return seg.pos + (clamped - seg.textStart);
      }
    }
    const last = segments[segments.length - 1]!;
    return last.pos + last.length;
  };

  const posToOffset = (pos: number): number => {
    if (segments.length === 0) return 0;
    // A position inside a segment's PM range maps to that segment's text offset
    // plus the intra-segment delta. A position before the first segment maps to
    // 0; after the last, to text.length; in an inter-block gap, to the end of
    // the preceding segment (so a selection boundary landing on a block edge
    // snaps to the nearest character, never into the "\n" join).
    for (const seg of segments) {
      const segPosEnd = seg.pos + seg.length;
      if (pos < seg.pos) {
        return seg.textStart;
      }
      if (pos <= segPosEnd) {
        return seg.textStart + (pos - seg.pos);
      }
    }
    return text.length;
  };

  return { text, offsetToPos, posToOffset };
}

/**
 * Walk a block node's descendants, emitting each text node's string with its
 * ProseMirror position. Inline nodes contribute their text; non-text leaf nodes
 * are skipped (they have no character offset in the flat projection).
 */
function collectBlockText(
  block: PMNode,
  blockContentStart: number,
  emit: (str: string, pos: number) => void,
): void {
  block.descendants((node, pos) => {
    if (node.isText && node.text) {
      emit(node.text, blockContentStart + pos);
    }
    return true;
  });
}
