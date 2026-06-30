/**
 * @vitest-environment jsdom
 */
import { describe, it, expect, vi, beforeEach } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { useAddDraftAnnotationMessage } from "./annotation-mutations";
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

const addDraftAnnotationMessage = vi.fn();

vi.mock("../api", () => ({
  api: {
    addDraftAnnotationMessage: (...args: unknown[]) => addDraftAnnotationMessage(...args),
  },
}));

// Zustand store mock: callable selector form, used as useAuthStore((s) => s.user).
vi.mock("../auth", () => ({
  useAuthStore: (selector: (state: { user: { id: string } }) => unknown) =>
    selector({ user: { id: userId } }),
}));

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

function makeWrapper(qc: QueryClient) {
  const Wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={qc}>{children}</QueryClientProvider>
  );
  Wrapper.displayName = "TestQueryClientWrapper";
  return Wrapper;
}

function seedCache(qc: QueryClient, annotation: DraftAnnotation): void {
  const response: ListDraftAnnotationsResponse = { annotations: [annotation], total: 1 };
  qc.setQueryData(draftAnnotationKeys.list(wsId, draftId), response);
}

function readMessages(qc: QueryClient): DraftAnnotationMessage[] {
  const cached = qc.getQueryData<ListDraftAnnotationsResponse>(
    draftAnnotationKeys.list(wsId, draftId),
  );
  return cached?.annotations.find((a) => a.id === annotationId)?.messages ?? [];
}

beforeEach(() => {
  addDraftAnnotationMessage.mockReset();
});

describe("useAddDraftAnnotationMessage — onSuccess swaps temp→real", () => {
  it("inserts an optimistic temp message, then replaces it with the server row's real id", async () => {
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    seedCache(qc, makeAnnotation([]));

    const real: DraftAnnotationMessage = {
      id: "server-msg-1",
      annotation_id: annotationId,
      author_type: "user",
      author_user_id: userId,
      body: "my reply",
      created_at: "2025-01-01T00:00:01Z",
    };
    // Resolve the POST only when we choose to, so we can observe the optimistic
    // (temp) state before success swaps it.
    let resolvePost: (m: DraftAnnotationMessage) => void = () => {};
    addDraftAnnotationMessage.mockReturnValue(
      new Promise<DraftAnnotationMessage>((resolve) => {
        resolvePost = resolve;
      }),
    );

    const { result } = renderHook(() => useAddDraftAnnotationMessage(wsId, draftId), {
      wrapper: makeWrapper(qc),
    });

    result.current.mutate({ annotationId, body: "my reply" });

    // Optimistic state: one temp message present.
    await waitFor(() => {
      const messages = readMessages(qc);
      expect(messages).toHaveLength(1);
      expect(messages[0]?.id.startsWith("temp-message-")).toBe(true);
      expect(messages[0]?.body).toBe("my reply");
    });

    // Resolve the POST → onSuccess swaps temp → real.
    resolvePost(real);

    await waitFor(() => {
      const messages = readMessages(qc);
      expect(messages).toHaveLength(1);
      expect(messages[0]?.id).toBe("server-msg-1");
    });
  });

  it("rolls back the optimistic message on error", async () => {
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    seedCache(qc, makeAnnotation([]));

    addDraftAnnotationMessage.mockRejectedValue(new Error("boom"));

    const { result } = renderHook(() => useAddDraftAnnotationMessage(wsId, draftId), {
      wrapper: makeWrapper(qc),
    });

    result.current.mutate({ annotationId, body: "doomed reply" });

    await waitFor(() => expect(result.current.isError).toBe(true));
    expect(readMessages(qc)).toHaveLength(0);
  });
});
