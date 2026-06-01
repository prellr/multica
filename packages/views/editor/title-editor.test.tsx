// Regression coverage for the async-defaultValue bug: TitleEditor used
// to ignore `defaultValue` changes after mount because TipTap's `content`
// is read once at editor init. Parents that load data via React Query
// would mount with `defaultValue=""`, then set the real title in a
// useEffect — the editor never reflected the loaded value and the page
// rendered "Untitled" indefinitely. The fix syncs the first non-empty
// defaultValue into the editor when its content is empty.

import { describe, it, expect, vi, beforeEach } from "vitest";
import { render } from "@testing-library/react";

// State the mocked editor exposes so tests can observe setContent calls
// and reflect the current text the editor would report.
const mockSetContent = vi.hoisted(() => vi.fn());
const editorText = vi.hoisted(() => ({ current: "" }));
const editorInitialized = vi.hoisted(() => ({ current: false }));

vi.mock("@tiptap/react", () => ({
  // Mirror TipTap's useEditor: `content` is read ONLY ONCE at init —
  // subsequent renders return the same editor and don't re-read config.
  // Reproducing that here matters: the regression we're guarding against
  // is "defaultValue arrives after mount and never reaches the editor"
  // — if the mock re-seeds editorText on every render it short-circuits
  // the test's whole reason to exist.
  useEditor: (config: any) => {
    if (!editorInitialized.current) {
      if (typeof config?.content === "string") {
        editorText.current = config.content;
      } else if (config?.content?.content?.[0]?.content?.[0]?.text) {
        editorText.current = config.content.content[0].content[0].text;
      }
      editorInitialized.current = true;
    }
    return {
      commands: {
        focus: vi.fn(),
        setContent: (doc: any) => {
          mockSetContent(doc);
          // Track text so subsequent useEffect runs see the editor as
          // "no longer empty" and won't re-sync.
          if (doc?.content?.[0]?.content?.[0]?.text) {
            editorText.current = doc.content[0].content[0].text;
          }
        },
      },
      getText: () => editorText.current,
    };
  },
  EditorContent: ({ className }: { className?: string }) => (
    <div className={className} data-testid="title-editor-content" />
  ),
}));

// Stub the i18n hook used inside TitleEditor (it only reads one aria
// label string; we don't need the real provider just for that).
vi.mock("../i18n", () => ({
  useT: () => ({ t: () => "title" }),
}));

import { TitleEditor } from "./title-editor";

beforeEach(() => {
  mockSetContent.mockReset();
  editorText.current = "";
  editorInitialized.current = false;
});

describe("TitleEditor — defaultValue arriving after mount", () => {
  it("syncs the editor content when defaultValue transitions from empty to a real value", () => {
    // Mount with empty default — mirrors what the memory detail page
    // does while waiting for the React Query response.
    const { rerender } = render(<TitleEditor defaultValue="" />);
    expect(mockSetContent).not.toHaveBeenCalled();

    // Parent re-renders with the loaded title once the query resolves.
    rerender(<TitleEditor defaultValue="Decision: Rebrand Safra 360" />);
    expect(mockSetContent).toHaveBeenCalledTimes(1);
    const doc = mockSetContent.mock.calls[0]?.[0];
    expect(doc?.content?.[0]?.content?.[0]?.text).toBe(
      "Decision: Rebrand Safra 360",
    );
  });

  it("does not re-sync once the editor has non-empty content (user edits aren't clobbered)", () => {
    // Mount with a real default — useEditor seeds editorText from
    // `content`, so the editor is already non-empty at first render.
    const { rerender } = render(
      <TitleEditor defaultValue="Initial title" />,
    );
    expect(mockSetContent).not.toHaveBeenCalled();

    // Simulate the editor receiving a new defaultValue from the parent
    // — perhaps because of a background refetch. The fix should NOT
    // clobber the existing content (which would, in production, also
    // include any unsaved user edits).
    rerender(<TitleEditor defaultValue="Different title" />);
    expect(mockSetContent).not.toHaveBeenCalled();
  });
});
