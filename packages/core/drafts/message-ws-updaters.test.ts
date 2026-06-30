import { describe, it, expect } from "vitest";
import { QueryClient } from "@tanstack/react-query";
import { onDraftMessageCreated } from "./message-ws-updaters";
import { draftMessageKeys } from "./message-queries";
import type { DraftMessage, ListDraftMessagesResponse } from "../types";

const wsId = "ws-1";
const draftId = "draft-1";
const userId = "user-1";

function makeMessage(id: string, overrides: Partial<DraftMessage> = {}): DraftMessage {
  return {
    id,
    draft_id: draftId,
    workspace_id: wsId,
    author_type: "user",
    author_user_id: userId,
    body: `message ${id}`,
    created_at: "2025-01-01T00:00:00Z",
    ...overrides,
  };
}

function seedCache(qc: QueryClient, messages: DraftMessage[]): void {
  const response: ListDraftMessagesResponse = { messages, total: messages.length };
  qc.setQueryData(draftMessageKeys.list(wsId, draftId), response);
}

function readMessages(qc: QueryClient): DraftMessage[] {
  const cached = qc.getQueryData<ListDraftMessagesResponse>(
    draftMessageKeys.list(wsId, draftId),
  );
  return cached?.messages ?? [];
}

describe("onDraftMessageCreated — robust dedupe", () => {
  it("appends a brand-new message and bumps total", () => {
    const qc = new QueryClient();
    seedCache(qc, [makeMessage("server-msg-1")]);

    onDraftMessageCreated(qc, wsId, draftId, makeMessage("server-msg-2"));

    const cached = qc.getQueryData<ListDraftMessagesResponse>(
      draftMessageKeys.list(wsId, draftId),
    );
    expect(cached?.messages.map((m) => m.id)).toEqual(["server-msg-1", "server-msg-2"]);
    expect(cached?.total).toBe(2);
  });

  it("is a no-op when the real id is already present (post temp→real swap)", () => {
    const qc = new QueryClient();
    const real = makeMessage("server-msg-1", { body: "my message" });
    seedCache(qc, [real]);

    onDraftMessageCreated(qc, wsId, draftId, real);

    expect(readMessages(qc).map((m) => m.id)).toEqual(["server-msg-1"]);
  });

  it("replaces a still-optimistic temp message (same author+body) instead of appending a duplicate", () => {
    // Window before the mutation's onSuccess swaps temp→real: the echo arrives
    // first. Dedupe-by-author+body must replace the temp row, not duplicate it.
    const qc = new QueryClient();
    const optimistic = makeMessage("temp-draft-message-abc", { body: "my thought" });
    seedCache(qc, [optimistic]);

    const real = makeMessage("server-msg-1", { body: "my thought" });
    onDraftMessageCreated(qc, wsId, draftId, real);

    const messages = readMessages(qc);
    expect(messages.map((m) => m.id)).toEqual(["server-msg-1"]);
    expect(messages).toHaveLength(1);
  });

  it("appends a genuinely new message from another author (Rail-2 agent post)", () => {
    const qc = new QueryClient();
    seedCache(qc, [makeMessage("server-msg-1", { body: "mine" })]);

    const fromAgent = makeMessage("server-msg-2", {
      author_type: "agent",
      author_user_id: "",
      body: "agent reply",
    });
    onDraftMessageCreated(qc, wsId, draftId, fromAgent);

    expect(readMessages(qc).map((m) => m.id)).toEqual(["server-msg-1", "server-msg-2"]);
  });

  it("is a no-op when the draft's rail isn't cached", () => {
    const qc = new QueryClient();
    // Nothing seeded for this draft.
    onDraftMessageCreated(qc, wsId, draftId, makeMessage("server-msg-1"));
    expect(
      qc.getQueryData<ListDraftMessagesResponse>(draftMessageKeys.list(wsId, draftId)),
    ).toBeUndefined();
  });
});
