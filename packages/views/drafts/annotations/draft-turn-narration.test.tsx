// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { I18nProvider } from "@multica/core/i18n/react";
import type { TaskMessagePayload } from "@multica/core/types";
import enCommon from "../../locales/en/common.json";
import enDrafts from "../../locales/en/drafts.json";

const TEST_RESOURCES = { en: { common: enCommon, drafts: enDrafts } };

// --- Mocks ------------------------------------------------------------------

// The live task:message stream the narration rail reads. Each test sets it
// before render.
let turnMessages: TaskMessagePayload[] = [];
// The most recent draft:turn_completed handler registered via useWSEvent, so a
// test can fire a completion.
let turnCompletedHandler: ((p: unknown) => void) | null = null;

vi.mock("@multica/core/drafts/turn-mutations", () => ({
  useDraftTurnMessages: () => turnMessages,
}));

vi.mock("@multica/core/realtime", () => ({
  useWSEvent: (event: string, handler: (p: unknown) => void) => {
    if (event === "draft:turn_completed") turnCompletedHandler = handler;
  },
}));

import { DraftSendControl, DraftTurnNarration } from "./draft-turn-narration";

function renderSend(
  props: { working?: boolean; pending?: boolean; onSend?: () => void } = {},
) {
  const onSend = props.onSend ?? vi.fn();
  render(
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      <DraftSendControl
        onSend={onSend}
        working={props.working ?? false}
        pending={props.pending ?? false}
      />
    </I18nProvider>,
  );
  return { onSend };
}

function renderNarration(activeTaskId: string | null, onTurnChange = vi.fn()) {
  render(
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      <DraftTurnNarration draftId="d-1" activeTaskId={activeTaskId} onTurnChange={onTurnChange} />
    </I18nProvider>,
  );
  return { onTurnChange };
}

beforeEach(() => {
  turnMessages = [];
  turnCompletedHandler = null;
});

describe("DraftSendControl", () => {
  it("renders the Send label when idle and fires onSend on click", () => {
    const { onSend } = renderSend();
    const btn = screen.getByRole("button", { name: /send/i });
    expect(btn).not.toBeDisabled();

    fireEvent.click(btn);
    expect(onSend).toHaveBeenCalledTimes(1);
  });

  it("is disabled and shows the working label while a turn is active", () => {
    renderSend({ working: true });
    const btn = screen.getByRole("button");
    expect(btn).toBeDisabled();
    // The working copy interpolates the agent name (Aye).
    expect(btn.textContent).toMatch(/Aye is thinking/i);
  });

  it("shows the sending label during the mutate-pending window", () => {
    renderSend({ working: true, pending: true });
    expect(screen.getByRole("button").textContent).toMatch(/Sending/i);
  });

  it("exposes the Cmd/Ctrl+Enter shortcut in the button tooltip", () => {
    renderSend();
    // formatShortcut renders ⌘↵ on Mac and Ctrl+Enter elsewhere — assert the
    // Enter token is present regardless of platform.
    const title = screen.getByRole("button").getAttribute("title") ?? "";
    expect(title).toMatch(/↵|Enter/);
  });
});

describe("DraftTurnNarration", () => {
  it("renders nothing when there is no active turn", () => {
    const { container } = render(
      <I18nProvider locale="en" resources={TEST_RESOURCES}>
        <DraftTurnNarration draftId="d-1" activeTaskId={null} onTurnChange={vi.fn()} />
      </I18nProvider>,
    );
    expect(container.firstChild).toBeNull();
  });

  it("renders the streamed task:message steps as narration", () => {
    turnMessages = [
      msg(0, "thinking", { content: "Reading the open queue" }),
      msg(1, "tool_use", { tool: "multica_draft_reply", input: { body: "agreed on the rollback" } }),
    ];
    renderNarration("task-1");
    // The thinking step's content surfaces as a label.
    expect(screen.getByText("Reading the open queue")).toBeTruthy();
    // The tool step uses the localized "Using {{tool}}" label.
    expect(screen.getByText(/Using multica_draft_reply/)).toBeTruthy();
  });

  it("downgrades an unknown message type to a generic step instead of throwing", () => {
    // Enum-drift: a server type the frontend has never seen must render, not
    // crash. We cast through unknown to model a drifted payload.
    turnMessages = [
      { task_id: "task-1", issue_id: "", seq: 0, type: "some_future_type" as never, content: "" },
    ];
    expect(() => renderNarration("task-1")).not.toThrow();
    // The generic fallback label ("Working") renders. It's the active (last)
    // step, so the component appends an ellipsis — match the prefix.
    expect(screen.getAllByText(/Working/).length).toBeGreaterThan(0);
  });

  it("clears the active turn when draft:turn_completed fires for this draft", () => {
    const { onTurnChange } = renderNarration("task-1");
    expect(turnCompletedHandler).toBeTruthy();
    turnCompletedHandler?.({ draft_id: "d-1", task_id: "task-1" });
    expect(onTurnChange).toHaveBeenCalledWith(null);
  });

  it("ignores a completion event for a different draft", () => {
    const { onTurnChange } = renderNarration("task-1");
    turnCompletedHandler?.({ draft_id: "other-draft", task_id: "task-2" });
    expect(onTurnChange).not.toHaveBeenCalled();
  });
});

function msg(
  seq: number,
  type: TaskMessagePayload["type"],
  over: Partial<TaskMessagePayload> = {},
): TaskMessagePayload {
  return { task_id: "task-1", issue_id: "", seq, type, ...over };
}
