// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from "vitest";
import { renderHook } from "@testing-library/react";
import { Editor } from "@tiptap/core";
import StarterKit from "@tiptap/starter-kit";
import type { DraftAnnotation } from "@multica/core/types";

// The hook PATCHes a moved anchor; capture the mutate calls.
const updateMutate = vi.hoisted(() => vi.fn());
vi.mock("@multica/core/drafts", async () => {
  const actual = await vi.importActual<typeof import("@multica/core/drafts")>("@multica/core/drafts");
  return {
    ...actual,
    useUpdateDraftAnnotation: () => ({ mutate: updateMutate }),
  };
});

import {
  useAnnotationAnchoring,
  buildAnchorFromSelection,
} from "./use-annotation-anchoring";

function makeAnnotation(over: Partial<DraftAnnotation> = {}): DraftAnnotation {
  return {
    id: "a-1",
    draft_id: "d-1",
    workspace_id: "ws-1",
    author_type: "user",
    author_user_id: "u-1",
    type: "comment",
    quote: "quick brown fox",
    context_before: "The ",
    context_after: " jumps",
    pos_hint: 4,
    state: "open",
    suggestion_before: null,
    suggestion_after: null,
    created_at: "",
    updated_at: "",
    messages: [],
    ...over,
  };
}

function makeEditor(html: string): Editor {
  return new Editor({ extensions: [StarterKit], content: html });
}

beforeEach(() => vi.clearAllMocks());

describe("buildAnchorFromSelection", () => {
  it("captures quote + surrounding context + posHint from a flat-text span", () => {
    const text = "The quick brown fox jumps over the lazy dog.";
    const from = text.indexOf("quick brown fox");
    const to = from + "quick brown fox".length;
    const anchor = buildAnchorFromSelection(text, from, to);
    expect(anchor.quote).toBe("quick brown fox");
    expect(anchor.posHint).toBe(from);
    expect(anchor.contextBefore.endsWith("The ")).toBe(true);
    expect(anchor.contextAfter.startsWith(" jumps")).toBe(true);
  });
});

describe("useAnnotationAnchoring.recompute", () => {
  it("anchors an unchanged annotation as matched with a decoration range", () => {
    const editor = makeEditor("<p>The quick brown fox jumps over the lazy dog.</p>");
    const annotations = [makeAnnotation()];
    const { result } = renderHook(() => useAnnotationAnchoring("ws-1", "d-1", annotations));

    const out = result.current.recompute(editor);
    expect(out.orphaned).toHaveLength(0);
    expect(out.anchored).toHaveLength(1);
    expect(out.anchored[0]!.status).toBe("matched");
    expect(out.decorationRanges).toHaveLength(1);
    // The decoration range round-trips to the quoted text.
    const r = out.decorationRanges[0]!;
    expect(editor.state.doc.textBetween(r.from, r.to)).toBe("quick brown fox");
    editor.destroy();
  });

  it("moves an annotation to the orphaned tray when its quoted span is deleted", () => {
    // The quote no longer appears: the span was removed.
    const editor = makeEditor("<p>The lazy dog sleeps.</p>");
    const annotations = [makeAnnotation()];
    const { result } = renderHook(() => useAnnotationAnchoring("ws-1", "d-1", annotations));

    const out = result.current.recompute(editor);
    expect(out.orphaned).toHaveLength(1);
    expect(out.orphaned[0]!.id).toBe("a-1");
    // No decoration is painted for an orphaned annotation.
    expect(out.decorationRanges).toHaveLength(0);
    editor.destroy();
  });

  it("follows a shifted anchor and PATCHes the moved range back to the server", () => {
    // Prepend a long paragraph so the quote moves far; identical text → shifted.
    const editor = makeEditor(
      "<p>A brand new opening paragraph long enough to push the anchor far past the position tolerance window.</p><p>The quick brown fox jumps over the lazy dog.</p>",
    );
    const annotations = [makeAnnotation()];
    const { result } = renderHook(() => useAnnotationAnchoring("ws-1", "d-1", annotations));

    const out = result.current.recompute(editor);
    expect(out.anchored[0]!.status).toBe("shifted");
    // A shifted anchor persists its new position/quote/context.
    expect(updateMutate).toHaveBeenCalledTimes(1);
    const call = updateMutate.mock.calls[0]![0];
    expect(call.id).toBe("a-1");
    expect(call.patch.quote).toBe("quick brown fox");
    editor.destroy();
  });

  it("flags an open annotation as changed when its span is materially edited", () => {
    const editor = makeEditor("<p>The quick green fox jumps over the lazy dog.</p>");
    const annotations = [makeAnnotation()];
    const { result } = renderHook(() => useAnnotationAnchoring("ws-1", "d-1", annotations));

    const out = result.current.recompute(editor);
    expect(out.anchored[0]!.status).toBe("changed");
    expect(out.anchored[0]!.flaggedChanged).toBe(true);
    editor.destroy();
  });
});
