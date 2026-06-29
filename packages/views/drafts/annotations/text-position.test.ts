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
