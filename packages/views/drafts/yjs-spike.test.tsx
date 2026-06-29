// @vitest-environment jsdom
//
// De-risking spike for Drafts slice 3a (Yjs co-editing floor).
//
// This test is the empirical verification harness for the three assumptions the
// architecture research flagged as gating the whole Yjs slice. It is intended to
// stay as a regression lock: if a future Tiptap / Yjs bump breaks the
// decoration-remap / markdown-round-trip / re-anchor-on-remote contract, this
// goes red BEFORE the backend is built on top of a broken floor.
//
// It exercises the editor side ONLY, with an in-process two-way Y.Doc relay
// standing in for the real binary-WS backend (slice 3a proper). No Go endpoint,
// no persistence, no agent wiring — purely: does the existing decoration +
// markdown + re-anchor machinery survive a Yjs CRDT underneath the editor.
//
// The three assumptions, mapped to the three describe blocks below:
//   1. Single-seed: editor mounts EMPTY, Collaboration hydrates from the seeded
//      Y.Doc, getMarkdown() round-trips the original body (no double-apply).
//   2. Decorations survive REMOTE edits: a relayed edit from a second doc remaps
//      the DecorationSet so each annotation still covers the intended text.
//   3. Markdown round-trips a merged doc cleanly, and feeding it through the pure
//      reanchor engine reclassifies annotations correctly.

import { describe, it, expect, afterEach } from "vitest";
import { Editor } from "@tiptap/core";
import { DecorationSet } from "@tiptap/pm/view";
import * as Y from "yjs";
import { reanchor, type Anchor } from "@multica/core/drafts";
import {
  createDraftYContext,
  destroyDraftYContext,
  connectDocs,
  DRAFT_YJS_FRAGMENT,
} from "@multica/core/drafts/collab";

import { createEditorExtensions } from "../editor/extensions";
import { seedYDocFromMarkdown } from "./annotations/seed-y-doc";
import {
  AnnotationDecorationExtension,
  annotationDecorationPluginKey,
  type AnnotationDecorationRange,
} from "./annotations/decoration-extension";
import { buildDocTextIndex } from "./annotations/text-position";
import { buildAnchorFromSelection } from "./annotations/use-annotation-anchoring";

const SAMPLE_BODY = [
  "# Launch plan",
  "",
  "The quick brown fox jumps over the lazy dog.",
  "",
  "We should ship the beta on Friday and tell the whole team.",
].join("\n");

const EXTENSION_OPTIONS = { placeholder: "Write..." } as const;

/** Mount a collaborative editor bound to `doc`, empty (Collaboration hydrates). */
function mountCollabEditor(doc: Y.Doc): Editor {
  return new Editor({
    // CRITICAL: no `content` — Collaboration hydrates from the fragment. Passing
    // content here would double-apply and corrupt the CRDT.
    extensions: [
      ...createEditorExtensions({ ...EXTENSION_OPTIONS, collaboration: { doc } }),
      AnnotationDecorationExtension,
    ],
  });
}

/** The editor's flat-text projection — the surface anchoring works against. */
function flatText(editor: Editor): string {
  return buildDocTextIndex(editor.state.doc).text;
}

/** Build a decoration range for a [from,to) flat-text span of `editor`. */
function decorationForQuote(
  editor: Editor,
  quote: string,
  id: string,
): AnnotationDecorationRange {
  const { text, offsetToPos } = buildDocTextIndex(editor.state.doc);
  const from = text.indexOf(quote);
  if (from < 0) throw new Error(`quote not found in projection: ${JSON.stringify(quote)}`);
  return {
    from: offsetToPos(from),
    to: offsetToPos(from + quote.length),
    id,
    type: "comment",
    state: "open",
    changed: false,
  };
}

/** Read the current DecorationSet out of the annotation plugin. */
function currentDecorations(editor: Editor): DecorationSet {
  return annotationDecorationPluginKey.getState(editor.state) ?? DecorationSet.empty;
}

/** The PM text currently covered by the decoration with `id`. */
function decoratedText(editor: Editor, id: string): string | null {
  const set = currentDecorations(editor);
  const found = set.find().find((d) => (d as any).type?.attrs?.["data-annotation-id"] === id);
  if (!found) return null;
  return editor.state.doc.textBetween(found.from, found.to);
}

const editors: Editor[] = [];
function track(editor: Editor): Editor {
  editors.push(editor);
  return editor;
}
afterEach(() => {
  while (editors.length) editors.pop()!.destroy();
});

// ---------------------------------------------------------------------------
// Assumption 1 — single-seed + markdown round-trip
// ---------------------------------------------------------------------------

describe("Yjs spike / assumption 1: single-seed hydration + markdown round-trip", () => {
  it("seeds the Y.Doc ONCE and the editor hydrates the seeded doc (no double-apply)", () => {
    const doc = new Y.Doc();
    seedYDocFromMarkdown(doc, SAMPLE_BODY, EXTENSION_OPTIONS);

    const editor = track(mountCollabEditor(doc));

    // The editor rendered the seeded content (heading + both paragraphs present
    // exactly once — a double-apply would duplicate them).
    const md = editor.getMarkdown().trimEnd();
    expect(md).toContain("# Launch plan");
    expect(md).toContain("The quick brown fox jumps over the lazy dog.");
    expect(md).toContain("ship the beta on Friday");
    // No duplication: the fox sentence appears exactly once.
    const occurrences = md.split("The quick brown fox").length - 1;
    expect(occurrences).toBe(1);
  });

  it("getMarkdown() equals the original body modulo trailing-whitespace normalization", () => {
    const doc = new Y.Doc();
    seedYDocFromMarkdown(doc, SAMPLE_BODY, EXTENSION_OPTIONS);
    const editor = track(mountCollabEditor(doc));
    expect(editor.getMarkdown().trimEnd()).toBe(SAMPLE_BODY.trimEnd());
  });

  it("a second seed call is a no-op (idempotent guard prevents double content)", () => {
    const doc = new Y.Doc();
    seedYDocFromMarkdown(doc, SAMPLE_BODY, EXTENSION_OPTIONS);
    // Calling seed again must NOT stack a second copy into the fragment.
    seedYDocFromMarkdown(doc, SAMPLE_BODY, EXTENSION_OPTIONS);
    const editor = track(mountCollabEditor(doc));
    const md = editor.getMarkdown();
    expect(md.split("The quick brown fox").length - 1).toBe(1);
  });
});

// ---------------------------------------------------------------------------
// Assumption 2 — decorations survive remote Yjs edits
//
// VERIFIED FINDING (load-bearing for 3a): the y-tiptap ySyncPlugin applies a
// remote update as ONE coarse ReplaceStep spanning the whole doc (confirmed:
// oldSize→newSize replace, a single ReplaceStep). ProseMirror's position
// mapping collapses BOTH endpoints of an inline decoration to the doc end across
// that step, so `DecorationSet.map(tr.mapping, tr.doc)` DROPS the decoration.
//
// => Inline decorations CANNOT survive remote transactions by map-through alone.
//    The production surface must RECOMPUTE-and-repaint from the re-anchor truth
//    after a remote transaction (it already recomputes from flat-text on local
//    edits; 3a must also drive that recompute on remote transactions — see the
//    onTransaction finding in assumption 3). This block verifies BOTH halves:
//    the map-through drop, and the recompute repair.
// ---------------------------------------------------------------------------

/**
 * Mirror what the production surface does on recompute: project the merged doc to
 * flat text, find the annotation's quote, and repaint the decoration over it.
 * In production this runs through `useAnnotationAnchoring.recompute` (which uses
 * the pure reanchor engine); here we resolve by exact quote to keep the harness
 * focused on the decoration-repaint contract under Yjs.
 */
function recomputeDecorations(
  editor: Editor,
  specs: { quote: string; id: string }[],
): void {
  const ranges: AnnotationDecorationRange[] = [];
  const { text, offsetToPos } = buildDocTextIndex(editor.state.doc);
  for (const { quote, id } of specs) {
    const from = text.indexOf(quote);
    if (from < 0) continue; // orphaned — no decoration (matches production)
    ranges.push({
      from: offsetToPos(from),
      to: offsetToPos(from + quote.length),
      id,
      type: "comment",
      state: "open",
      changed: false,
    });
  }
  editor.commands.setAnnotationDecorations(ranges);
}

describe("Yjs spike / assumption 2: decoration repaint under remote edits", () => {
  it("map-through DROPS a decoration across a remote insert before the range (finding)", () => {
    const local = new Y.Doc();
    seedYDocFromMarkdown(local, SAMPLE_BODY, EXTENSION_OPTIONS);
    const remote = new Y.Doc();
    const stop = connectDocs(local, remote);

    const editor = track(mountCollabEditor(local));

    editor.commands.setAnnotationDecorations([decorationForQuote(editor, "quick brown fox", "anno-1")]);
    expect(decoratedText(editor, "anno-1")).toBe("quick brown fox");

    // Remote insert before the annotated range (into the heading text).
    const remoteFrag = remote.getXmlFragment(DRAFT_YJS_FRAGMENT);
    const headingText = (remoteFrag.get(0) as Y.XmlElement).get(0) as Y.XmlText;
    headingText.insert(0, "URGENT: ");
    stop();

    // The insert landed (text remapped), but the map-through decoration was
    // dropped by the coarse y-sync ReplaceStep — this is the finding.
    expect(flatText(editor)).toContain("URGENT: Launch plan");
    expect(decoratedText(editor, "anno-1")).toBeNull();
  });

  it("RECOMPUTE repaints the decoration over the right text after a remote insert", () => {
    const local = new Y.Doc();
    seedYDocFromMarkdown(local, SAMPLE_BODY, EXTENSION_OPTIONS);
    const remote = new Y.Doc();
    const stop = connectDocs(local, remote);

    const editor = track(mountCollabEditor(local));
    editor.commands.setAnnotationDecorations([decorationForQuote(editor, "quick brown fox", "anno-1")]);

    const remoteFrag = remote.getXmlFragment(DRAFT_YJS_FRAGMENT);
    const headingText = (remoteFrag.get(0) as Y.XmlElement).get(0) as Y.XmlText;
    headingText.insert(0, "URGENT: ");
    stop();

    // The production fix: recompute + repaint after the remote transaction.
    recomputeDecorations(editor, [{ quote: "quick brown fox", id: "anno-1" }]);
    expect(decoratedText(editor, "anno-1")).toBe("quick brown fox");
  });

  it("RECOMPUTE repaints over the edited text when a remote peer changes text INSIDE the range", () => {
    const local = new Y.Doc();
    seedYDocFromMarkdown(local, SAMPLE_BODY, EXTENSION_OPTIONS);
    const remote = new Y.Doc();
    const stop = connectDocs(local, remote);

    const editor = track(mountCollabEditor(local));
    editor.commands.setAnnotationDecorations([decorationForQuote(editor, "quick brown fox", "anno-2")]);

    // Remote edit INSIDE the annotated range: "brown" → "red".
    const remoteFrag = remote.getXmlFragment(DRAFT_YJS_FRAGMENT);
    const paraText = (remoteFrag.get(1) as Y.XmlElement).get(0) as Y.XmlText;
    const str = paraText.toString();
    const brownIdx = str.indexOf("brown");
    paraText.delete(brownIdx, "brown".length);
    paraText.insert(brownIdx, "red");
    stop();

    expect(flatText(editor)).toContain("quick red fox");
    // After recompute, the decoration tracks the new span ("quick red fox").
    recomputeDecorations(editor, [{ quote: "quick red fox", id: "anno-2" }]);
    expect(decoratedText(editor, "anno-2")).toBe("quick red fox");
  });
});

// ---------------------------------------------------------------------------
// Assumption 3 — markdown round-trip + re-anchor on remote change
// ---------------------------------------------------------------------------

describe("Yjs spike / assumption 3: markdown round-trip + re-anchor on remote change", () => {
  it("getMarkdown() stays clean after a Yjs merge (no syntax corruption)", () => {
    const local = new Y.Doc();
    seedYDocFromMarkdown(local, SAMPLE_BODY, EXTENSION_OPTIONS);
    const remote = new Y.Doc();
    const stop = connectDocs(local, remote);
    const editor = track(mountCollabEditor(local));

    // Remote insert before the fox sentence.
    const remoteFrag = remote.getXmlFragment(DRAFT_YJS_FRAGMENT);
    const headingText = (remoteFrag.get(0) as Y.XmlElement).get(0) as Y.XmlText;
    headingText.insert(0, "URGENT: ");
    stop();

    const md = editor.getMarkdown().trimEnd();
    // Heading marker intact, no doubled "#", no stray XML, fox sentence intact.
    expect(md.startsWith("# URGENT: Launch plan")).toBe(true);
    expect(md).toContain("The quick brown fox jumps over the lazy dog.");
    expect(md).not.toContain("<");
  });

  it("re-anchors a SHIFTED annotation after a remote insert before its range", () => {
    const local = new Y.Doc();
    seedYDocFromMarkdown(local, SAMPLE_BODY, EXTENSION_OPTIONS);
    const remote = new Y.Doc();
    const stop = connectDocs(local, remote);
    const editor = track(mountCollabEditor(local));

    // Capture an anchor for "quick brown fox" against the ORIGINAL projection.
    const before = flatText(editor);
    const q = "quick brown fox";
    const from = before.indexOf(q);
    const anchor: Anchor = buildAnchorFromSelection(before, from, from + q.length);

    // Remote inserts a whole new sentence at the very start of the heading,
    // shifting the fox sentence far enough to exceed EXACT_POS_TOLERANCE.
    const remoteFrag = remote.getXmlFragment(DRAFT_YJS_FRAGMENT);
    const headingText = (remoteFrag.get(0) as Y.XmlElement).get(0) as Y.XmlText;
    headingText.insert(
      0,
      "A long urgent preamble that pushes the anchored span well past the tolerance window. ",
    );
    stop();

    // Re-anchor against the merged doc's projection.
    const after = flatText(editor);
    const result = reanchor(after, anchor);
    expect(result.status).toBe("shifted");
    if (result.status !== "orphaned") {
      expect(after.slice(result.from, result.to)).toBe("quick brown fox");
    }
  });

  it("re-anchors a CHANGED annotation after a remote edit inside its range", () => {
    const local = new Y.Doc();
    seedYDocFromMarkdown(local, SAMPLE_BODY, EXTENSION_OPTIONS);
    const remote = new Y.Doc();
    const stop = connectDocs(local, remote);
    const editor = track(mountCollabEditor(local));

    const before = flatText(editor);
    const q = "quick brown fox";
    const from = before.indexOf(q);
    const anchor: Anchor = buildAnchorFromSelection(before, from, from + q.length);

    // Remote edit INSIDE the quote: "brown" → "red".
    const remoteFrag = remote.getXmlFragment(DRAFT_YJS_FRAGMENT);
    const paraText = (remoteFrag.get(1) as Y.XmlElement).get(0) as Y.XmlText;
    const str = paraText.toString();
    const brownIdx = str.indexOf("brown");
    paraText.delete(brownIdx, "brown".length);
    paraText.insert(brownIdx, "red");
    stop();

    const after = flatText(editor);
    expect(after).toContain("quick red fox");
    const result = reanchor(after, anchor);
    // The span was materially edited but is still recognizable → changed.
    expect(result.status).toBe("changed");
  });

  it("orphans an annotation whose quoted span a remote peer deleted entirely", () => {
    const local = new Y.Doc();
    seedYDocFromMarkdown(local, SAMPLE_BODY, EXTENSION_OPTIONS);
    const remote = new Y.Doc();
    const stop = connectDocs(local, remote);
    const editor = track(mountCollabEditor(local));

    const before = flatText(editor);
    const q = "quick brown fox";
    const from = before.indexOf(q);
    const anchor: Anchor = buildAnchorFromSelection(before, from, from + q.length);

    // Remote deletes the whole fox sentence's paragraph contents.
    const remoteFrag = remote.getXmlFragment(DRAFT_YJS_FRAGMENT);
    const paraText = (remoteFrag.get(1) as Y.XmlElement).get(0) as Y.XmlText;
    paraText.delete(0, paraText.length);
    paraText.insert(0, "Totally different content with no overlap whatsoever here.");
    stop();

    const after = flatText(editor);
    expect(after).not.toContain("quick brown fox");
    const result = reanchor(after, anchor);
    expect(result.status).toBe("orphaned");
  });

  // Wiring note for slice 3a: does the editor's transaction listener fire for a
  // REMOTE Yjs update? The annotation surface debounces re-anchor off onUpdate;
  // if remote transactions don't fire it, 3a must wire recompute off the Y.Doc
  // 'update' event (or the ySync plugin) instead of relying on onUpdate alone.
  it("FINDING: a remote Yjs update DOES dispatch a transaction on the local editor", () => {
    const local = new Y.Doc();
    seedYDocFromMarkdown(local, SAMPLE_BODY, EXTENSION_OPTIONS);
    const remote = new Y.Doc();
    const stop = connectDocs(local, remote);
    const editor = track(mountCollabEditor(local));

    let transactions = 0;
    editor.on("transaction", () => {
      transactions += 1;
    });

    const remoteFrag = remote.getXmlFragment(DRAFT_YJS_FRAGMENT);
    const headingText = (remoteFrag.get(0) as Y.XmlElement).get(0) as Y.XmlText;
    headingText.insert(0, "X");
    stop();

    // If this is > 0, the ySyncPlugin turned the remote update into a local
    // transaction — onUpdate/onTransaction fires and the existing debounced
    // recompute path will run for remote edits. If it were 0, 3a would need an
    // explicit Y.Doc 'update' subscription to drive recompute.
    expect(transactions).toBeGreaterThan(0);
  });
});

// ---------------------------------------------------------------------------
// Non-draft-editor invariant: the collaboration option is opt-in
// ---------------------------------------------------------------------------

describe("Yjs spike / non-draft editors are unaffected", () => {
  it("createEditorExtensions without collaboration adds no Collaboration extension", () => {
    const without = createEditorExtensions(EXTENSION_OPTIONS);
    const names = without.map((e) => e.name);
    expect(names).not.toContain("collaboration");
    expect(names).not.toContain("collaborationCaret");
    // UndoRedo (StarterKit's history) is still present without collaboration.
    const doc = new Y.Doc();
    const withColl = createEditorExtensions({ ...EXTENSION_OPTIONS, collaboration: { doc } });
    expect(withColl.map((e) => e.name)).toContain("collaboration");
  });

  it("createDraftYContext / destroyDraftYContext manage a doc + awareness cleanly", () => {
    const ctx = createDraftYContext();
    expect(ctx.doc).toBeInstanceOf(Y.Doc);
    expect(ctx.awareness).toBeDefined();
    // Destroy must not throw.
    expect(() => destroyDraftYContext(ctx)).not.toThrow();
  });
});
