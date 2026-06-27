"use client";

/**
 * SPIKE (ROA-1160) — proof that the shadcn `MessageScroller` composition
 * accepts our real `ChatMessage[]` data and compiles against our base-ui
 * stack. This is the migration TEMPLATE for chat-message-list.tsx, not a
 * shipping component.
 *
 * What it demonstrates maps 1:1 onto the engine it would replace:
 *  - useTranscriptScroll()            → <MessageScrollerProvider autoScroll
 *                                         defaultScrollPosition="last-anchor"
 *                                         scrollPreviousItemPeek={…}>
 *  - the manual anchorNewTurn effect  → <MessageScrollerItem scrollAnchor={role==="user"}>
 *                                         (the new-turn anchor is now declarative — the
 *                                         imperative effect is deleted)
 *  - data-message-id wrappers         → <MessageScrollerItem messageId={id}>
 *  - hasNewBelow + jumpToLatest pill   → <MessageScrollerButton direction="end" />
 *                                         (auto-shows via built-in scrollable tracking)
 *  - sentinel + ResizeObserver growth → built into Viewport/Content
 *  - prepareForPrepend/restorePrepend  → <MessageScrollerViewport preserveScrollOnPrepend>
 *
 * The real migration swaps the placeholder bubble below for the existing
 * MessageBubble, and wires useMessageScroller().scrollToMessage for
 * open-at-unread + permalinks, and useMessageScrollerVisibility for mark-read.
 */

import {
  MessageScroller,
  MessageScrollerButton,
  MessageScrollerContent,
  MessageScrollerItem,
  MessageScrollerProvider,
  MessageScrollerViewport,
} from "@multica/ui/components/ui/message-scroller";
import { cn } from "@multica/ui/lib/utils";
import type { ChatMessage } from "@multica/core/types";

interface MessageScrollerSpikeProps {
  messages: ChatMessage[];
}

export function MessageScrollerSpike({ messages }: MessageScrollerSpikeProps) {
  return (
    <MessageScrollerProvider
      autoScroll
      defaultScrollPosition="last-anchor"
      scrollPreviousItemPeek={64}
    >
      <MessageScroller className="flex-1">
        <MessageScrollerViewport preserveScrollOnPrepend>
          <MessageScrollerContent className="px-4 py-3">
            {messages.map((msg) => (
              <MessageScrollerItem
                key={msg.id}
                messageId={msg.id}
                // Anchor each user turn near the top of the viewport — this is
                // the declarative replacement for the engine's anchorNewTurn().
                scrollAnchor={msg.role === "user"}
              >
                <div
                  className={cn(
                    "max-w-[80%] rounded-2xl px-3 py-2 text-sm",
                    msg.role === "user"
                      ? "ml-auto bg-primary text-primary-foreground"
                      : "mr-auto bg-muted text-foreground",
                  )}
                >
                  {msg.content}
                </div>
              </MessageScrollerItem>
            ))}
          </MessageScrollerContent>
        </MessageScrollerViewport>
        {/* Jump-to-latest — shows itself when there is content below. */}
        <MessageScrollerButton direction="end" />
      </MessageScroller>
    </MessageScrollerProvider>
  );
}
