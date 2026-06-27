import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../locales/en/common.json";
import enChannels from "../../locales/en/channels.json";
import type { Channel } from "@multica/core/types";

const TEST_RESOURCES = { en: { common: enCommon, channels: enChannels } };

// Capture the composer's drop handler so the test can fire a "real" drop.
const dropHandlers = vi.hoisted(() => ({
  onDrop: null as null | ((files: File[]) => void),
}));

vi.mock("../../editor", () => ({
  useFileDropZone: ({ onDrop }: { onDrop: (files: File[]) => void }) => {
    dropHandlers.onDrop = onDrop;
    return { isDragOver: false, dropZoneProps: { "data-testid": "drop-zone" } };
  },
  FileDropOverlay: () => null,
  ContentEditor: () => <div data-testid="editor" />,
}));

// api.uploadFile is the single upload path shared by the paperclip button
// and (now) drag-and-drop.
const uploadFile = vi.hoisted(() => vi.fn());
vi.mock("@multica/core/api", () => ({
  api: { uploadFile },
}));

const sendMutate = vi.hoisted(() => vi.fn());
const inertOptions = (key: string) => ({
  queryKey: [key],
  queryFn: async () => [],
  enabled: false,
});
vi.mock("@multica/core/channels", () => {
  const state = {
    inputDrafts: {} as Record<string, string>,
    setInputDraft: vi.fn(),
    clearInputDraft: vi.fn(),
  };
  const useChannelsStore = (sel: (s: typeof state) => unknown) => sel(state);
  return {
    useChannelsStore,
    useSendChannelMessage: () => ({ mutate: sendMutate, isPending: false }),
    channelMembersOptions: () => inertOptions("members"),
  };
});

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "ws-1",
}));

vi.mock("@multica/core/auth", () => ({
  useAuthStore: (sel: (s: { user: { id: string } | null }) => unknown) =>
    sel({ user: { id: "u-1" } }),
}));

vi.mock("@multica/core/workspace/queries", () => ({
  agentTagListOptions: () => inertOptions("agent-tags"),
  agentListOptions: () => inertOptions("agents"),
  memberListOptions: () => inertOptions("members"),
}));

import { ChannelComposer } from "./channel-composer";

const channel = {
  id: "chan-1",
  name: "general",
  kind: "channel",
} as unknown as Channel;

function renderComposer(props?: { disabled?: boolean }) {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(
    <QueryClientProvider client={qc}>
      <I18nProvider resources={TEST_RESOURCES} defaultNS="channels">
        <ChannelComposer channel={channel} disabled={props?.disabled} />
      </I18nProvider>
    </QueryClientProvider>,
  );
}

describe("ChannelComposer drag-and-drop", () => {
  beforeEach(() => {
    dropHandlers.onDrop = null;
    uploadFile.mockReset();
    uploadFile.mockResolvedValue({ id: "att-1" });
    sendMutate.mockReset();
  });

  it("routes dropped files through api.uploadFile", async () => {
    renderComposer();
    expect(dropHandlers.onDrop).not.toBeNull();

    const file = new File(["x"], "shot.png", { type: "image/png" });
    dropHandlers.onDrop?.([file]);

    await waitFor(() => expect(uploadFile).toHaveBeenCalledWith(file));
  });

  it("uploads every file in a multi-file drop", async () => {
    renderComposer();
    const a = new File(["a"], "a.png", { type: "image/png" });
    const b = new File(["b"], "b.png", { type: "image/png" });
    dropHandlers.onDrop?.([a, b]);

    await waitFor(() => expect(uploadFile).toHaveBeenCalledTimes(2));
    expect(uploadFile).toHaveBeenCalledWith(a);
    expect(uploadFile).toHaveBeenCalledWith(b);
  });
});
