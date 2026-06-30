// @vitest-environment jsdom
import { describe, it, expect, vi } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { I18nProvider } from "@multica/core/i18n/react";
import type { DraftAnnotation } from "@multica/core/types";
import enCommon from "../../locales/en/common.json";
import enDrafts from "../../locales/en/drafts.json";
import type { AnchoredAnnotation } from "../annotations/use-annotation-anchoring";

const TEST_RESOURCES = { en: { common: enCommon, drafts: enDrafts } };

// Stub the two tab surfaces — they're tested in their own files; here we assert
// the wrapper's tab-switching + open-count badge wiring. The annotation panel
// module keeps its real `openAnnotationCount` (the wrapper imports it for the
// tab badge) while its `AnnotationThreadPanel` is stubbed to a marker.
vi.mock("./conversation-rail", () => ({
  ConversationRail: () => <div data-testid="conversation-rail">conversation</div>,
}));

vi.mock("../annotations/annotation-thread-panel", async () => {
  const actual = await vi.importActual<typeof import("../annotations/annotation-thread-panel")>(
    "../annotations/annotation-thread-panel",
  );
  return {
    ...actual,
    AnnotationThreadPanel: () => <div data-testid="annotation-panel">annotations</div>,
  };
});

import { DraftSidePanel } from "./draft-side-panel";

function makeAnnotation(over: Partial<DraftAnnotation> = {}): DraftAnnotation {
  return {
    id: "a-1",
    draft_id: "d-1",
    workspace_id: "ws-1",
    author_type: "user",
    author_user_id: "u-1",
    type: "comment",
    quote: "quick brown fox",
    context_before: "",
    context_after: "",
    pos_hint: 0,
    state: "open",
    suggestion_before: null,
    suggestion_after: null,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
    messages: [],
    ...over,
  };
}

function anchored(annotation: DraftAnnotation): AnchoredAnnotation {
  return { annotation, status: "matched", range: { from: 1, to: 16 }, flaggedChanged: false };
}

function renderPanel(anchoredList: AnchoredAnnotation[] = []) {
  return render(
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      <DraftSidePanel
        wsId="ws-1"
        draftId="d-1"
        anchored={anchoredList}
        orphaned={[]}
        activeId={null}
        onSelect={vi.fn()}
      />
    </I18nProvider>,
  );
}

describe("DraftSidePanel", () => {
  it("defaults to the Conversation tab", () => {
    renderPanel();
    expect(screen.getByTestId("conversation-rail")).toBeInTheDocument();
    expect(screen.queryByTestId("annotation-panel")).not.toBeInTheDocument();
  });

  it("switches to the Annotations tab on click", () => {
    renderPanel();
    fireEvent.click(screen.getByRole("tab", { name: new RegExp(enDrafts.conversation.tab_annotations) }));
    expect(screen.getByTestId("annotation-panel")).toBeInTheDocument();
  });

  it("shows an open-count badge on the Annotations tab for open annotations", () => {
    renderPanel([anchored(makeAnnotation({ state: "open" }))]);
    // The badge carries the shared open_count aria-label; it reads 1.
    expect(screen.getByLabelText(enDrafts.annotations.open_count)).toHaveTextContent("1");
  });

  it("hides the open-count badge when no annotations are open", () => {
    renderPanel([anchored(makeAnnotation({ state: "resolved" }))]);
    expect(screen.queryByLabelText(enDrafts.annotations.open_count)).not.toBeInTheDocument();
  });
});
