// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { I18nProvider } from "@multica/core/i18n/react";
import type { DraftAnnotation } from "@multica/core/types";
import enCommon from "../../locales/en/common.json";
import enDrafts from "../../locales/en/drafts.json";
import type { AnchoredAnnotation } from "./use-annotation-anchoring";

const TEST_RESOURCES = { en: { common: enCommon, drafts: enDrafts } };

// Capture the mutation calls so we can assert state transitions / replies fire.
const updateMutate = vi.hoisted(() => vi.fn());
const addMessageMutate = vi.hoisted(() => vi.fn());
const deleteMutate = vi.hoisted(() => vi.fn());

vi.mock("@multica/core/drafts", () => ({
  useUpdateDraftAnnotation: () => ({ mutate: updateMutate }),
  useAddDraftAnnotationMessage: () => ({ mutate: addMessageMutate }),
  useDeleteDraftAnnotation: () => ({ mutate: deleteMutate }),
}));

// The expanded thread resolves human senders by member id via useActorName.
// Mock it so the panel renders without a QueryClient/workspace provider — same
// pattern issue-detail and friends use.
vi.mock("@multica/core/workspace/hooks", () => ({
  useActorName: () => ({
    getMemberName: (id: string) => (id === "u-1" ? "Ada Lovelace" : "Unknown"),
    getAgentName: () => "Unknown Agent",
    getActorName: (type: string, id: string) =>
      type === "member" && id === "u-1" ? "Ada Lovelace" : "Unknown",
    getActorInitials: () => "AL",
    getActorAvatarUrl: () => null,
  }),
}));

import { AnnotationThreadPanel } from "./annotation-thread-panel";

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
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
    messages: [
      {
        id: "m-1",
        annotation_id: "a-1",
        author_type: "user",
        author_user_id: "u-1",
        body: "Is this the right animal?",
        created_at: "2026-01-01T00:00:00Z",
      },
    ],
    ...over,
  };
}

function anchored(annotation: DraftAnnotation, over: Partial<AnchoredAnnotation> = {}): AnchoredAnnotation {
  return {
    annotation,
    status: "matched",
    range: { from: 1, to: 16 },
    flaggedChanged: false,
    ...over,
  };
}

function renderPanel(props: Partial<React.ComponentProps<typeof AnnotationThreadPanel>> = {}) {
  const onSelect = props.onSelect ?? vi.fn();
  const utils = render(
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      <AnnotationThreadPanel
        wsId="ws-1"
        draftId="d-1"
        anchored={props.anchored ?? []}
        orphaned={props.orphaned ?? []}
        activeId={props.activeId ?? null}
        onSelect={onSelect}
      />
    </I18nProvider>,
  );
  return { onSelect, container: utils.container };
}

beforeEach(() => vi.clearAllMocks());

describe("AnnotationThreadPanel", () => {
  it("renders an anchored annotation as a pin card with its quote and initial message", () => {
    const ann = makeAnnotation();
    renderPanel({ anchored: [anchored(ann)] });
    expect(screen.getByText(/quick brown fox/)).toBeInTheDocument();
    expect(screen.getByText("Is this the right animal?")).toBeInTheDocument();
  });

  it("renders an agent-authored 'question' annotation (the first-class type) as a pin card", () => {
    // 'question' is Aye's primary inquisitive verb (slice 2) — a first-class
    // type, not a 'comment' downgrade. The panel must render it (its own icon)
    // without falling over.
    const ann = makeAnnotation({
      id: "a-q",
      type: "question",
      author_type: "agent",
      author_user_id: "",
      quote: "§4 rollback",
      messages: [
        {
          id: "m-q",
          annotation_id: "a-q",
          author_type: "agent",
          author_user_id: "",
          body: "Should this cover rollback?",
          created_at: "2026-01-01T00:00:00Z",
        },
      ],
    });
    renderPanel({ anchored: [anchored(ann)] });
    expect(screen.getByText(/§4 rollback/)).toBeInTheDocument();
    expect(screen.getByText("Should this cover rollback?")).toBeInTheDocument();
  });

  it("shows an open-count badge for open annotations", () => {
    renderPanel({ anchored: [anchored(makeAnnotation())] });
    // The open-count badge carries an aria-label.
    expect(screen.getByLabelText(enDrafts.annotations.open_count)).toHaveTextContent("1");
  });

  it("resolves an annotation (state transition) from the expanded thread", () => {
    const ann = makeAnnotation();
    renderPanel({ anchored: [anchored(ann)], activeId: "a-1" });
    fireEvent.click(screen.getByRole("button", { name: enDrafts.annotations.resolve }));
    expect(updateMutate).toHaveBeenCalledWith({ id: "a-1", patch: { state: "resolved" } });
  });

  it("renders the expanded thread message body as markdown (not raw text)", () => {
    // Aye's replies are markdown: bold + a list must render as real elements so
    // a structured answer reads as prose, not an undifferentiated wall.
    const ann = makeAnnotation({
      author_type: "agent",
      author_user_id: "",
      messages: [
        {
          id: "m-1",
          annotation_id: "a-1",
          author_type: "agent",
          author_user_id: "",
          body: "**Key risk:** rollback.\n\n1. First point\n2. Second point",
          created_at: "2026-01-01T00:00:00Z",
        },
      ],
    });
    const { container } = renderPanel({ anchored: [anchored(ann)], activeId: "a-1" });
    // Bold renders as <strong>, the list as <li> — proof the markdown renderer
    // ran instead of dumping the raw string.
    expect(container.querySelector("strong")).toHaveTextContent("Key risk:");
    expect(container.querySelectorAll("li").length).toBe(2);
  });

  it("labels the agent sender as Aye on an agent-authored thread message", () => {
    const ann = makeAnnotation({
      author_type: "agent",
      author_user_id: "",
      messages: [
        {
          id: "m-1",
          annotation_id: "a-1",
          author_type: "agent",
          author_user_id: "",
          body: "Consider rollback.",
          created_at: "2026-01-01T00:00:00Z",
        },
      ],
    });
    renderPanel({ anchored: [anchored(ann)], activeId: "a-1" });
    expect(screen.getByText(enDrafts.turn.agent_name)).toBeInTheDocument();
  });

  it("labels a human sender by their resolved member name", () => {
    // makeAnnotation's default message is authored by member u-1 → "Ada
    // Lovelace" per the useActorName mock.
    const ann = makeAnnotation();
    renderPanel({ anchored: [anchored(ann)], activeId: "a-1" });
    expect(screen.getByText("Ada Lovelace")).toBeInTheDocument();
  });

  it("adds a reply from the expanded thread", () => {
    const ann = makeAnnotation();
    renderPanel({ anchored: [anchored(ann)], activeId: "a-1" });
    const textarea = screen.getByPlaceholderText(enDrafts.annotations.reply_placeholder);
    fireEvent.change(textarea, { target: { value: "Yes, keep the fox." } });
    fireEvent.click(screen.getByRole("button", { name: enDrafts.annotations.reply_submit }));
    expect(addMessageMutate).toHaveBeenCalledWith({ annotationId: "a-1", body: "Yes, keep the fox." });
  });

  it("flags 'text changed' on an open annotation whose anchor was materially edited", () => {
    const ann = makeAnnotation();
    renderPanel({ anchored: [anchored(ann, { status: "changed", flaggedChanged: true })] });
    expect(screen.getByText(enDrafts.annotations.text_changed)).toBeInTheDocument();
  });

  it("moves an orphaned annotation into the orphaned tray (not the main list)", () => {
    const orphan = makeAnnotation({ id: "a-orphan", quote: "deleted span" });
    renderPanel({ anchored: [anchored(orphan, { status: "orphaned", range: undefined })], orphaned: [orphan] });
    // The tray header is present...
    expect(screen.getByText(enDrafts.annotations.orphaned_title)).toBeInTheDocument();
    // ...and the orphaned annotation renders under the tray, identified by the
    // data attribute the tray sets.
    expect(document.querySelector('[data-orphaned-annotation="a-orphan"]')).not.toBeNull();
  });
});
