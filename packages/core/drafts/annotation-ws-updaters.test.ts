import { describe, it, expect } from "vitest";
import { QueryClient } from "@tanstack/react-query";
import {
  onDraftAnnotationCreated,
  onDraftAnnotationUpdated,
  onDraftAnnotationMessageCreated,
} from "./annotation-ws-updaters";
import { draftAnnotationKeys } from "./annotation-queries";
import type {
  DraftAnnotation,
  DraftAnnotationMessage,
  ListDraftAnnotationsResponse,
} from "../types";

const wsId = "ws-1";
const draftId = "draft-1";
const annotationId = "annotation-1";
const userId = "user-1";

function makeMessage(
  id: string,
  overrides: Partial<DraftAnnotationMessage> = {},
): DraftAnnotationMessage {
  return {
    id,
    annotation_id: annotationId,
    author_type: "user",
    author_user_id: userId,
    body: `message ${id}`,
    created_at: "2025-01-01T00:00:00Z",
    ...overrides,
  };
}

function makeAnnotation(messages: DraftAnnotationMessage[]): DraftAnnotation {
  return {
    id: annotationId,
    draft_id: draftId,
    workspace_id: wsId,
    author_type: "user",
    author_user_id: userId,
    type: "comment",
    quote: "quoted span",
    context_before: "",
    context_after: "",
    pos_hint: 0,
    state: "open",
    suggestion_before: null,
    suggestion_after: null,
    created_at: "2025-01-01T00:00:00Z",
    updated_at: "2025-01-01T00:00:00Z",
    messages,
  };
}

function seedCache(qc: QueryClient, annotations: DraftAnnotation[]): void {
  const response: ListDraftAnnotationsResponse = {
    annotations,
    total: annotations.length,
  };
  qc.setQueryData(draftAnnotationKeys.list(wsId, draftId), response);
}

function readMessages(qc: QueryClient): DraftAnnotationMessage[] {
  const cached = qc.getQueryData<ListDraftAnnotationsResponse>(
    draftAnnotationKeys.list(wsId, draftId),
  );
  const annotation = cached?.annotations.find((a) => a.id === annotationId);
  return annotation?.messages ?? [];
}

describe("onDraftAnnotationUpdated — optimistic reply preservation", () => {
  it("keeps an in-flight temp message the incoming WS payload omits", () => {
    // This reproduces the flicker: the user posted a reply (optimistic temp
    // message in cache), and a concurrent draft_annotation:updated event arrives
    // mid-flight whose server `messages` predate the reply.
    const qc = new QueryClient();
    const committed = makeMessage("server-msg-1");
    const optimistic = makeMessage("temp-message-abc", { body: "my in-flight reply" });
    seedCache(qc, [makeAnnotation([committed, optimistic])]);

    // WS payload from the server does NOT include the optimistic reply.
    onDraftAnnotationUpdated(qc, wsId, makeAnnotation([committed]));

    const messages = readMessages(qc);
    // Before the fix the optimistic reply was dropped here (the disappear).
    expect(messages.map((m) => m.id)).toEqual(["server-msg-1", "temp-message-abc"]);
  });

  it("does not duplicate a temp message the incoming payload already includes", () => {
    const qc = new QueryClient();
    const optimistic = makeMessage("temp-message-abc");
    seedCache(qc, [makeAnnotation([optimistic])]);

    // Payload happens to carry the same temp id (defensive): no duplication.
    onDraftAnnotationUpdated(qc, wsId, makeAnnotation([optimistic]));

    expect(readMessages(qc).map((m) => m.id)).toEqual(["temp-message-abc"]);
  });

  it("replaces non-temp server messages wholesale (no stale rows linger)", () => {
    const qc = new QueryClient();
    seedCache(qc, [makeAnnotation([makeMessage("server-msg-1")])]);

    onDraftAnnotationUpdated(
      qc,
      wsId,
      makeAnnotation([makeMessage("server-msg-1"), makeMessage("server-msg-2")]),
    );

    expect(readMessages(qc).map((m) => m.id)).toEqual(["server-msg-1", "server-msg-2"]);
  });

  it("ignores temp-annotation-id payloads (optimistic create not yet committed)", () => {
    const qc = new QueryClient();
    const tempAnnotation: DraftAnnotation = {
      ...makeAnnotation([makeMessage("temp-message-abc")]),
      id: "temp-annotation-xyz",
    };
    seedCache(qc, [tempAnnotation]);

    onDraftAnnotationUpdated(qc, wsId, {
      ...makeAnnotation([]),
      id: "temp-annotation-xyz",
    });

    // Untouched: a temp-id update is a no-op.
    const cached = qc.getQueryData<ListDraftAnnotationsResponse>(
      draftAnnotationKeys.list(wsId, draftId),
    );
    expect(cached?.annotations[0]?.messages.map((m) => m.id)).toEqual(["temp-message-abc"]);
  });
});

describe("onDraftAnnotationCreated — optimistic reply preservation on re-match", () => {
  it("keeps an in-flight temp message when an existing annotation is replaced", () => {
    const qc = new QueryClient();
    const optimistic = makeMessage("temp-message-abc", { body: "my in-flight reply" });
    seedCache(qc, [makeAnnotation([makeMessage("server-msg-1"), optimistic])]);

    onDraftAnnotationCreated(qc, wsId, makeAnnotation([makeMessage("server-msg-1")]));

    expect(readMessages(qc).map((m) => m.id)).toEqual(["server-msg-1", "temp-message-abc"]);
  });

  it("appends a brand-new annotation untouched", () => {
    const qc = new QueryClient();
    seedCache(qc, []);

    onDraftAnnotationCreated(qc, wsId, makeAnnotation([makeMessage("server-msg-1")]));

    const cached = qc.getQueryData<ListDraftAnnotationsResponse>(
      draftAnnotationKeys.list(wsId, draftId),
    );
    expect(cached?.annotations.map((a) => a.id)).toEqual([annotationId]);
  });
});

describe("onDraftAnnotationMessageCreated — robust dedupe", () => {
  it("is a no-op when the real id is already present (post temp→real swap)", () => {
    const qc = new QueryClient();
    const real = makeMessage("server-msg-1", { body: "my reply" });
    seedCache(qc, [makeAnnotation([real])]);

    onDraftAnnotationMessageCreated(qc, wsId, draftId, annotationId, real);

    expect(readMessages(qc).map((m) => m.id)).toEqual(["server-msg-1"]);
  });

  it("replaces a still-optimistic temp message (same author+body) instead of appending a duplicate", () => {
    // Window before onSuccess swaps temp→real: the echo arrives first.
    const qc = new QueryClient();
    const optimistic = makeMessage("temp-message-abc", { body: "my reply" });
    seedCache(qc, [makeAnnotation([optimistic])]);

    const real = makeMessage("server-msg-1", { body: "my reply" });
    onDraftAnnotationMessageCreated(qc, wsId, draftId, annotationId, real);

    const messages = readMessages(qc);
    expect(messages.map((m) => m.id)).toEqual(["server-msg-1"]);
    expect(messages).toHaveLength(1);
  });

  it("appends a genuinely new message from another author", () => {
    const qc = new QueryClient();
    seedCache(qc, [makeAnnotation([makeMessage("server-msg-1", { body: "mine" })])]);

    const fromAgent = makeMessage("server-msg-2", {
      author_type: "agent",
      author_user_id: "",
      body: "agent reply",
    });
    onDraftAnnotationMessageCreated(qc, wsId, draftId, annotationId, fromAgent);

    expect(readMessages(qc).map((m) => m.id)).toEqual(["server-msg-1", "server-msg-2"]);
  });
});

describe("flicker regression — concurrent WS update then echo yields exactly one copy", () => {
  it("reply survives a mid-flight annotation rewrite, then the echo reconciles to one row", () => {
    const qc = new QueryClient();
    const committed = makeMessage("server-msg-1", { body: "earlier" });
    const optimistic = makeMessage("temp-message-abc", { body: "@Aye please review" });
    seedCache(qc, [makeAnnotation([committed, optimistic])]);

    // 1. Concurrent draft_annotation:updated (the @-mention triggered a turn).
    //    Payload lacks the optimistic reply. It MUST survive (no disappear).
    onDraftAnnotationUpdated(qc, wsId, makeAnnotation([committed]));
    expect(readMessages(qc).map((m) => m.id)).toEqual(["server-msg-1", "temp-message-abc"]);

    // 2. The user's own message:created echo arrives with the real row. Because
    //    the temp wasn't swapped yet, dedupe-by-author+body replaces it → ONE copy.
    const real = makeMessage("server-msg-2", { body: "@Aye please review" });
    onDraftAnnotationMessageCreated(qc, wsId, draftId, annotationId, real);

    const messages = readMessages(qc);
    expect(messages.map((m) => m.id)).toEqual(["server-msg-1", "server-msg-2"]);
    expect(messages.filter((m) => m.body === "@Aye please review")).toHaveLength(1);
  });
});
