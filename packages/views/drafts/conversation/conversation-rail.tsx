"use client";

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { draftMessageListOptions, useAddDraftMessage } from "@multica/core/drafts";
import { Button } from "@multica/ui/components/ui/button";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { useT } from "../../i18n";
import { MessageRow } from "../common/message-row";

interface ConversationRailProps {
  wsId: string;
  draftId: string;
}

/**
 * The draft conversation rail (Rail-1) — a draft-level, UN-anchored chat
 * surface, distinct from the anchored annotation threads. A scrollable message
 * list (each row via the shared {@link MessageRow}) over a persistent bottom
 * composer. Rail-1 is human-only; agent-authored messages (Rail-2) render with
 * agent styling for free via MessageRow.
 *
 * Cache-only reads (TanStack Query owns the list); the composer posts via the
 * optimistic {@link useAddDraftMessage}. Cmd/Ctrl+Enter sends, mirroring the
 * annotation reply box.
 */
export function ConversationRail({ wsId, draftId }: ConversationRailProps) {
  const { t } = useT("drafts");
  const { data: messages = [] } = useQuery(draftMessageListOptions(wsId, draftId));
  const addMessage = useAddDraftMessage(wsId, draftId);
  const [draft, setDraft] = useState("");

  const submit = () => {
    const body = draft.trim();
    if (!body) return;
    addMessage.mutate(body);
    setDraft("");
  };

  return (
    <div className="flex h-full flex-col">
      <div className="flex-1 overflow-y-auto">
        {messages.length === 0 ? (
          <p className="p-4 text-sm text-muted-foreground">
            {t(($) => $.conversation.empty)}
          </p>
        ) : (
          <div className="flex flex-col gap-4 px-4 py-3">
            {messages.map((m) => (
              <MessageRow key={m.id} {...m} />
            ))}
          </div>
        )}
      </div>

      {/* Persistent bottom composer. Stays mounted whether or not there are
          messages — the rail is the always-available conversation surface. */}
      <div className="space-y-1.5 border-t p-3">
        <Textarea
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          placeholder={t(($) => $.conversation.composer_placeholder)}
          rows={2}
          className="text-sm"
          onKeyDown={(e) => {
            if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
              e.preventDefault();
              submit();
            }
          }}
        />
        <div className="flex justify-end">
          <Button
            size="sm"
            className="h-7 px-2 text-xs"
            onClick={submit}
            disabled={!draft.trim()}
          >
            {t(($) => $.conversation.send)}
          </Button>
        </div>
      </div>
    </div>
  );
}
