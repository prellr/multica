// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nProvider } from "@multica/core/i18n/react";
import type { DraftMessage } from "@multica/core/types";
import enCommon from "../../locales/en/common.json";
import enDrafts from "../../locales/en/drafts.json";

const TEST_RESOURCES = { en: { common: enCommon, drafts: enDrafts } };

// The rail reads its list via draftMessageListOptions and posts via
// useAddDraftMessage. Drive the list from a static array and capture the post.
const addMutate = vi.hoisted(() => vi.fn());
const messagesRef = vi.hoisted(() => ({ current: [] as DraftMessage[] }));

vi.mock("@multica/core/drafts", () => ({
  draftMessageListOptions: () => ({
    queryKey: ["draft-messages", "ws-1", "d-1", "list"],
    queryFn: () => Promise.resolve({ messages: messagesRef.current, total: messagesRef.current.length }),
    select: (data: { messages: DraftMessage[] }) => data.messages,
  }),
  useAddDraftMessage: () => ({ mutate: addMutate }),
}));

// MessageRow resolves human senders by member id via useActorName. Mock it so
// the rail renders without a workspace provider — same pattern the annotation
// panel test uses.
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

import { ConversationRail } from "./conversation-rail";

function makeMessage(over: Partial<DraftMessage> = {}): DraftMessage {
  return {
    id: "m-1",
    draft_id: "d-1",
    workspace_id: "ws-1",
    author_type: "user",
    author_user_id: "u-1",
    body: "first thought",
    created_at: "2026-01-01T00:00:00Z",
    ...over,
  };
}

function renderRail() {
  // A QueryClient with the query already resolved synchronously: seed the cache
  // so useQuery returns data on first render (no async flush needed).
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  qc.setQueryData(
    ["draft-messages", "ws-1", "d-1", "list"],
    { messages: messagesRef.current, total: messagesRef.current.length },
  );
  return render(
    <QueryClientProvider client={qc}>
      <I18nProvider locale="en" resources={TEST_RESOURCES}>
        <ConversationRail wsId="ws-1" draftId="d-1" />
      </I18nProvider>
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  vi.clearAllMocks();
  messagesRef.current = [];
});

describe("ConversationRail", () => {
  it("shows the empty state when there are no messages", () => {
    renderRail();
    expect(screen.getByText(enDrafts.conversation.empty)).toBeInTheDocument();
  });

  it("renders messages via the shared message row (human + agent)", () => {
    messagesRef.current = [
      makeMessage({ id: "m-1", body: "human thought" }),
      makeMessage({
        id: "m-2",
        author_type: "agent",
        author_user_id: "",
        body: "**Key point:** consider rollback.",
      }),
    ];
    const { container } = renderRail();
    // The human message renders, resolved to its member name by the MessageRow.
    expect(screen.getByText("human thought")).toBeInTheDocument();
    expect(screen.getByText("Ada Lovelace")).toBeInTheDocument();
    // The agent message renders via MessageRow: labeled Aye, markdown bold as
    // <strong> (proof the shared row's markdown renderer ran).
    expect(screen.getByText(enDrafts.turn.agent_name)).toBeInTheDocument();
    expect(container.querySelector("strong")).toHaveTextContent("Key point:");
  });

  it("posts a message from the composer and clears the input (optimistic append path)", () => {
    renderRail();
    const textarea = screen.getByPlaceholderText(enDrafts.conversation.composer_placeholder);
    fireEvent.change(textarea, { target: { value: "a new message" } });
    fireEvent.click(screen.getByRole("button", { name: enDrafts.conversation.send }));
    expect(addMutate).toHaveBeenCalledWith("a new message");
    // Input cleared after submit.
    expect((textarea as HTMLTextAreaElement).value).toBe("");
  });

  it("sends on Cmd/Ctrl+Enter from the composer", () => {
    renderRail();
    const textarea = screen.getByPlaceholderText(enDrafts.conversation.composer_placeholder);
    fireEvent.change(textarea, { target: { value: "via shortcut" } });
    fireEvent.keyDown(textarea, { key: "Enter", metaKey: true });
    expect(addMutate).toHaveBeenCalledWith("via shortcut");
  });

  it("does not post an empty / whitespace-only message", () => {
    renderRail();
    const textarea = screen.getByPlaceholderText(enDrafts.conversation.composer_placeholder);
    fireEvent.change(textarea, { target: { value: "   " } });
    fireEvent.keyDown(textarea, { key: "Enter", metaKey: true });
    expect(addMutate).not.toHaveBeenCalled();
  });
});
