// @vitest-environment jsdom
import { describe, it, expect } from "vitest";
import { Editor } from "@tiptap/core";
import StarterKit from "@tiptap/starter-kit";
import { buildDocTextIndex } from "./text-position";

/**
 * Tests for the flat-text ⇄ ProseMirror-position bridge. A real ProseMirror
 * document (built via a headless Tiptap editor) is the only honest fixture —
 * the index walks actual text nodes and positions.
 */

function makeEditor(html: string): Editor {
  return new Editor({
    extensions: [StarterKit],
    content: html,
  });
}

describe("buildDocTextIndex", () => {
  it("projects a single paragraph to its text and maps offsets to positions", () => {
    const editor = makeEditor("<p>The quick brown fox</p>");
    const { text, offsetToPos } = buildDocTextIndex(editor.state.doc);
    expect(text).toBe("The quick brown fox");

    // Offset 0 is the first char; in PM the first text char sits at position 1
    // (position 0 is before the paragraph node).
    const pos0 = offsetToPos(0);
    const posQuick = offsetToPos(text.indexOf("quick"));
    expect(posQuick).toBeGreaterThan(pos0);
    // The PM position for "quick" should round-trip to the same text.
    expect(editor.state.doc.textBetween(posQuick, posQuick + "quick".length)).toBe("quick");
    editor.destroy();
  });

  it("separates blocks with a newline in the flat projection", () => {
    const editor = makeEditor("<p>first para</p><p>second para</p>");
    const { text } = buildDocTextIndex(editor.state.doc);
    expect(text).toBe("first para\nsecond para");
    editor.destroy();
  });

  it("maps an offset in the second block to a position inside that block", () => {
    const editor = makeEditor("<p>alpha</p><p>bravo charlie</p>");
    const { text, offsetToPos } = buildDocTextIndex(editor.state.doc);
    const charlieOffset = text.indexOf("charlie");
    const pos = offsetToPos(charlieOffset);
    expect(editor.state.doc.textBetween(pos, pos + "charlie".length)).toBe("charlie");
    editor.destroy();
  });

  it("clamps an out-of-range offset to the document end", () => {
    const editor = makeEditor("<p>tiny</p>");
    const { offsetToPos } = buildDocTextIndex(editor.state.doc);
    const pos = offsetToPos(9999);
    expect(pos).toBeLessThanOrEqual(editor.state.doc.content.size);
    editor.destroy();
  });
});

describe("posToOffset (capture-time inverse)", () => {
  it("round-trips a position through offsetToPos within a single block", () => {
    const editor = makeEditor("<p>The quick brown fox</p>");
    const { text, offsetToPos, posToOffset } = buildDocTextIndex(editor.state.doc);
    const off = text.indexOf("brown");
    const pos = offsetToPos(off);
    expect(posToOffset(pos)).toBe(off);
    editor.destroy();
  });

  it("REGRESSION: a selection spanning two list items captures the correct span", () => {
    // buildDocTextIndex concatenates list items with NO separator, so the flat
    // projection of these two <li>s is "first itemsecond item". A selection
    // from inside the first li to inside the second must capture the exact
    // contiguous slice of that projection — the previous textBetween(from,to,"\n")
    // approach injected a "\n" at the li boundary and produced a string that
    // doesn't exist in the projection, falling back to a wrong span.
    const editor = makeEditor("<ul><li>first item</li><li>second item</li></ul>");
    const { text, posToOffset } = buildDocTextIndex(editor.state.doc);
    expect(text).toBe("first itemsecond item");

    const doc = editor.state.doc;
    // PM positions: locate "item" (end of first li) and the start of the second.
    // Select from the "i" of the first "item" to the "d" boundary in "second".
    const firstItemPmStart = findPmPosOfText(doc, "item", 0);
    const secondStartPm = findPmPosOfText(doc, "second", 0);
    const from = firstItemPmStart; // start of "item" in li #1
    const to = secondStartPm + "second".length; // through "second" in li #2

    const fromOffset = posToOffset(from);
    const toOffset = posToOffset(to);
    const captured = text.slice(fromOffset, toOffset);

    // The captured quote must exist verbatim in the projection and match the
    // intended span "itemsecond".
    expect(text.indexOf(captured)).toBe(fromOffset);
    expect(captured).toBe("itemsecond");
    editor.destroy();
  });

  it("snaps a boundary landing on a block edge to the nearest character, not the join", () => {
    const editor = makeEditor("<p>alpha</p><p>bravo</p>");
    const { text, posToOffset } = buildDocTextIndex(editor.state.doc);
    expect(text).toBe("alpha\nbravo");
    const doc = editor.state.doc;
    // End of the first paragraph's text.
    const alphaPm = findPmPosOfText(doc, "alpha", 0);
    const endOfAlpha = alphaPm + "alpha".length;
    // Maps to offset 5 ("alpha".length) — the char before the "\n" join.
    expect(posToOffset(endOfAlpha)).toBe(5);
    editor.destroy();
  });
});

/** Find the PM position of the first character of `needle` in the doc. */
function findPmPosOfText(doc: import("@tiptap/pm/model").Node, needle: string, occurrence: number): number {
  let found = -1;
  let seen = 0;
  doc.descendants((node, pos) => {
    if (found !== -1) return false;
    if (node.isText && node.text) {
      const idx = node.text.indexOf(needle);
      if (idx !== -1) {
        if (seen === occurrence) {
          found = pos + idx;
          return false;
        }
        seen++;
      }
    }
    return true;
  });
  return found;
}
