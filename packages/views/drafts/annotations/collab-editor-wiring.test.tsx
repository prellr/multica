// @vitest-environment jsdom
//
// Locks the production editor's slice-3a collaboration wiring contract:
//   1. Mount EMPTY + seed the Y.Doc once = correct content via Collaboration
//      hydration (no double-apply corruption).
//   2. The editor's `onUpdate` listener fires for a REMOTE Yjs transaction, and
//      driving the recompute+repaint from inside that listener heals the
//      decorations the coarse remote ReplaceStep drops.
//
// This is the wiring counterpart to yjs-spike.test.tsx: the spike proved the
// primitives in isolation; this proves the exact onUpdate→recompute loop the
// AnnotationDraftEditor relies on actually runs on remote edits. We use a
// headless Editor (no React tree, no query/auth/WS mocks) configured the same
// way the production editor configures it — empty mount, collaboration option,
// onUpdate-driven repaint — which is the focused, durable contract.

import { describe, it, expect, afterEach } from "vitest";
import { Editor } from "@tiptap/core";
import { DecorationSet } from "@tiptap/pm/view";
import * as Y from "yjs";
import {
  connectDocs,
  DRAFT_YJS_FRAGMENT,
} from "@multica/core/drafts/collab";

import { createEditorExtensions } from "../../editor/extensions";
import { seedYDocFromMarkdown } from "./seed-y-doc";
import {
  AnnotationDecorationExtension,
  annotationDecorationPluginKey,
  type AnnotationDecorationRange,
} from "./decoration-extension";
import { buildDocTextIndex } from "./text-position";

const SAMPLE_BODY = [
  "# Launch plan",
  "",
  "The quick brown fox jumps over the lazy dog.",
].join("\n");

const EXT_OPTIONS = { placeholder: "Write..." } as const;

const editors: Editor[] = [];
function track(e: Editor): Editor {
  editors.push(e);
  return e;
}
afterEach(() => {
  while (editors.length) editors.pop()!.destroy();
});

/**
 * Mount the editor the way AnnotationDraftEditor does: EMPTY (no `content`), with
 * the collaboration option bound to `doc`, plus an onUpdate handler that drives
 * the recompute+repaint (the production loop). `onRemoteRepaint` lets the test
 * observe each recompute.
 */
function mountProductionLikeEditor(
  doc: Y.Doc,
  resolveDecorations: (ed: Editor) => AnnotationDecorationRange[],
  onUpdateFired: () => void,
): Editor {
  const editor: Editor = new Editor({
    extensions: [
      ...createEditorExtensions({ ...EXT_OPTIONS, collaboration: { doc } }),
      AnnotationDecorationExtension,
    ],
    onUpdate: ({ editor: ed }) => {
      onUpdateFired();
      // The production editor debounces this; here we run it synchronously so
      // the test asserts the post-repaint state deterministically.
      ed.commands.setAnnotationDecorations(resolveDecorations(ed));
    },
  });
  return track(editor);
}

function decoratedText(editor: Editor, id: string): string | null {
  const set: DecorationSet =
    annotationDecorationPluginKey.getState(editor.state) ?? DecorationSet.empty;
  const found = set
    .find()
    .find((d) => (d as { type?: { attrs?: Record<string, string> } }).type?.attrs?.["data-annotation-id"] === id);
  if (!found) return null;
  return editor.state.doc.textBetween(found.from, found.to);
}

/** Resolve a single annotation's decoration by locating its quote in flat text. */
function resolveByQuote(quote: string, id: string) {
  return (ed: Editor): AnnotationDecorationRange[] => {
    const { text, offsetToPos } = buildDocTextIndex(ed.state.doc);
    const from = text.indexOf(quote);
    if (from < 0) return [];
    return [
      {
        from: offsetToPos(from),
        to: offsetToPos(from + quote.length),
        id,
        type: "comment",
        state: "open",
        changed: false,
      },
    ];
  };
}

describe("collab editor wiring / mount-empty-and-seed", () => {
  it("hydrates the seeded body exactly once (no double-apply)", () => {
    const doc = new Y.Doc();
    seedYDocFromMarkdown(doc, SAMPLE_BODY, EXT_OPTIONS);

    const editor = mountProductionLikeEditor(doc, () => [], () => {});

    const md = editor.getMarkdown().trimEnd();
    expect(md).toBe(SAMPLE_BODY.trimEnd());
    // The fox sentence appears exactly once — a double-apply would duplicate it.
    expect(md.split("The quick brown fox").length - 1).toBe(1);
  });
});

describe("collab editor wiring / recompute-on-remote-transaction", () => {
  it("onUpdate fires on a remote edit and the repaint heals the decoration", () => {
    const local = new Y.Doc();
    seedYDocFromMarkdown(local, SAMPLE_BODY, EXT_OPTIONS);
    const remote = new Y.Doc();
    const stop = connectDocs(local, remote);

    let updates = 0;
    const editor = mountProductionLikeEditor(
      local,
      resolveByQuote("quick brown fox", "anno-1"),
      () => {
        updates += 1;
      },
    );

    // Initial paint (no onUpdate yet — paint directly, as the production list
    // effect does on mount).
    editor.commands.setAnnotationDecorations(
      resolveByQuote("quick brown fox", "anno-1")(editor),
    );
    expect(decoratedText(editor, "anno-1")).toBe("quick brown fox");

    // A remote peer inserts text before the annotated range. y-tiptap applies
    // this as a coarse ReplaceStep that drops the inline decoration via
    // map-through — but it ALSO dispatches a local transaction, firing onUpdate,
    // whose recompute repaints the decoration over the correct text.
    const remoteFrag = remote.getXmlFragment(DRAFT_YJS_FRAGMENT);
    const headingText = (remoteFrag.get(0) as Y.XmlElement).get(0) as Y.XmlText;
    headingText.insert(0, "URGENT: ");
    stop();

    // onUpdate fired for the remote transaction.
    expect(updates).toBeGreaterThan(0);
    // The edit landed and the decoration was healed by the onUpdate repaint.
    expect(editor.getText()).toContain("URGENT: Launch plan");
    expect(decoratedText(editor, "anno-1")).toBe("quick brown fox");
  });
});
