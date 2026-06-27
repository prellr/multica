import { describe, it, expect } from "vitest";
import { QueryClient, type InfiniteData } from "@tanstack/react-query";
import { channelKeys } from "./queries";
import { patchChannelMessages, prependChannelMessage } from "./mutations";
import type { ChannelMessage, ChannelMessagesPage } from "../types";

// Regression for the ROA-1139 infinite-query migration: the channel timeline
// cache changed from a flat ChannelMessage[] to InfiniteData<ChannelMessagesPage>.
// The optimistic mutations still wrote the flat shape — and the optimistic
// SEND did `[optimistic, ...old]`, which throws on the non-iterable page
// object inside onMutate and ABORTS the mutation, so messages never sent
// (incl. messages to agents). These tests pin the cache-shape contract.

function msg(id: string, content = id): ChannelMessage {
  return {
    id,
    channel_id: "c1",
    author_type: "member",
    author_id: "u1",
    content,
    parent_message_id: null,
    edited_at: null,
    deleted_at: null,
    created_at: "2026-06-27T00:00:00.000Z",
    reactions: [],
    thread_reply_count: 0,
    attachments: [],
  };
}

function page(messages: ChannelMessage[], has_more = false): ChannelMessagesPage {
  return { messages, has_more, next_cursor: null };
}

function seed(qc: QueryClient, data: InfiniteData<ChannelMessagesPage>) {
  qc.setQueryData(channelKeys.messages("c1"), data);
}

function read(qc: QueryClient): InfiniteData<ChannelMessagesPage> | undefined {
  return qc.getQueryData(channelKeys.messages("c1"));
}

describe("channel message optimistic cache helpers (infinite-query shape)", () => {
  it("prepends the optimistic send to the newest page WITHOUT throwing", () => {
    const qc = new QueryClient();
    // Page 0 = newest, newest-first. Page 1 = older.
    seed(qc, {
      pages: [page([msg("m2"), msg("m1")], true), page([msg("m0")])],
      pageParams: [null, "cursor-0"],
    });

    // The pre-fix code did `[optimistic, ...old]` here and threw — this asserts
    // it doesn't, and that the message lands at the head of page 0.
    expect(() => prependChannelMessage(qc, "c1", msg("optimistic-x"))).not.toThrow();

    const data = read(qc)!;
    expect(data.pages).toHaveLength(2);
    expect(data.pages[0]!.messages.map((m) => m.id)).toEqual([
      "optimistic-x",
      "m2",
      "m1",
    ]);
    // Older page is untouched.
    expect(data.pages[1]!.messages.map((m) => m.id)).toEqual(["m0"]);
  });

  it("seeds a single page when the cache is empty", () => {
    const qc = new QueryClient();
    prependChannelMessage(qc, "c1", msg("optimistic-x"));
    const data = read(qc)!;
    expect(data.pages).toHaveLength(1);
    expect(data.pages[0]!.messages.map((m) => m.id)).toEqual(["optimistic-x"]);
  });

  it("patches a message in ANY loaded page (edit/delete/react), preserving order", () => {
    const qc = new QueryClient();
    seed(qc, {
      pages: [page([msg("m2"), msg("m1")]), page([msg("m0")])],
      pageParams: [null, "cursor-0"],
    });

    // Target a message in the OLDER page (page 1) — the flat-array write would
    // have missed it entirely.
    patchChannelMessages(qc, "c1", (msgs) =>
      msgs.map((m) => (m.id === "m0" ? { ...m, deleted_at: "2026-06-27T01:00:00.000Z" } : m)),
    );

    const data = read(qc)!;
    expect(data.pages[1]!.messages[0]!.deleted_at).toBe("2026-06-27T01:00:00.000Z");
    // Order preserved, other rows untouched.
    expect(data.pages[0]!.messages.map((m) => m.id)).toEqual(["m2", "m1"]);
    expect(data.pages[0]!.messages.every((m) => m.deleted_at === null)).toBe(true);
  });

  it("is a no-op on an unset cache (no throw)", () => {
    const qc = new QueryClient();
    expect(() => patchChannelMessages(qc, "c1", (m) => m)).not.toThrow();
    expect(read(qc)).toBeUndefined();
  });
});
