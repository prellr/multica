"use client";

import { useState } from "react";
import {
  MessageSquare,
  HelpCircle,
  Lightbulb,
  Check,
  Ban,
  Highlighter,
  CircleAlert,
  Trash2,
} from "lucide-react";
import type {
  DraftAnnotation,
  DraftAnnotationType,
  DraftAnnotationState,
} from "@multica/core/types";
import {
  useAddDraftAnnotationMessage,
  useUpdateDraftAnnotation,
  useDeleteDraftAnnotation,
} from "@multica/core/drafts";
import { Button } from "@multica/ui/components/ui/button";
import { Badge } from "@multica/ui/components/ui/badge";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../../i18n";
import { MessageRow } from "../common/message-row";
import type { AnchoredAnnotation } from "./use-annotation-anchoring";

const TYPE_ICON: Record<string, typeof MessageSquare> = {
  comment: MessageSquare,
  question: HelpCircle,
  suggestion: Lightbulb,
  approve: Check,
  block: Ban,
  highlight: Highlighter,
};

/** Open-enum default: an unknown type renders the generic comment icon. */
function typeIcon(type: DraftAnnotationType): typeof MessageSquare {
  return TYPE_ICON[type] ?? MessageSquare;
}

/**
 * Count of open (non-orphaned) annotations — the number the panel's own
 * open-count badge shows. Exported so the two-tab {@link DraftSidePanel} can put
 * the same count on the "Annotations" tab without re-deriving the filter.
 */
export function openAnnotationCount(anchored: AnchoredAnnotation[]): number {
  return anchored.filter(
    (a) => a.status !== "orphaned" && a.annotation.state === "open",
  ).length;
}

interface AnnotationThreadPanelProps {
  wsId: string;
  draftId: string;
  anchored: AnchoredAnnotation[];
  orphaned: DraftAnnotation[];
  /** The currently expanded annotation id (pin → thread), or null. */
  activeId: string | null;
  onSelect: (id: string | null) => void;
  /**
   * When rendered inside the two-tab {@link DraftSidePanel}, the wrapper already
   * supplies the `w-96` rail chrome (border, width) and a tab strip, so the
   * panel drops its own outer width/border and the redundant title header. The
   * default (standalone) keeps the full chrome — that's what this component's
   * own tests render, so they stay behaviorally unchanged.
   */
  embedded?: boolean;
}

/**
 * Right-rail panel for the annotation overlay: a list of annotation pins (one
 * card each), an open-count badge, an orphaned tray, and — when a pin is
 * expanded — its thread with a reply box and resolve/dismiss controls.
 */
export function AnnotationThreadPanel({
  wsId,
  draftId,
  anchored,
  orphaned,
  activeId,
  onSelect,
  embedded = false,
}: AnnotationThreadPanelProps) {
  const { t } = useT("drafts");

  const openCount = openAnnotationCount(anchored);

  const visible = anchored.filter((a) => a.status !== "orphaned");

  return (
    <div className={cn("flex h-full flex-col", !embedded && "w-96 border-l")}>
      {/* Standalone keeps its own title header; embedded in the tab wrapper the
          tab strip already names the surface (and carries the open-count badge),
          so the header is redundant. */}
      {!embedded && (
        <div className="flex items-center justify-between border-b px-4 py-3">
          <span className="text-sm font-medium">{t(($) => $.annotations.panel_title)}</span>
          {openCount > 0 && (
            <Badge variant="secondary" aria-label={t(($) => $.annotations.open_count)}>
              {openCount}
            </Badge>
          )}
        </div>
      )}

      <div className="flex-1 overflow-y-auto">
        {visible.length === 0 && orphaned.length === 0 ? (
          <p className="p-4 text-sm text-muted-foreground">
            {t(($) => $.annotations.empty)}
          </p>
        ) : (
          <div className="flex flex-col">
            {visible.map((a) => (
              <AnnotationCard
                key={a.annotation.id}
                wsId={wsId}
                draftId={draftId}
                anchored={a}
                active={a.annotation.id === activeId}
                onSelect={() =>
                  onSelect(a.annotation.id === activeId ? null : a.annotation.id)
                }
              />
            ))}
          </div>
        )}

        {orphaned.length > 0 && (
          <OrphanedTray
            wsId={wsId}
            draftId={draftId}
            orphaned={orphaned}
            activeId={activeId}
          />
        )}
      </div>
    </div>
  );
}

interface AnnotationCardProps {
  wsId: string;
  draftId: string;
  anchored: AnchoredAnnotation;
  active: boolean;
  onSelect: () => void;
}

function AnnotationCard({ wsId, draftId, anchored, active, onSelect }: AnnotationCardProps) {
  const { t } = useT("drafts");
  const { annotation, flaggedChanged } = anchored;
  const Icon = typeIcon(annotation.type);

  const addMessage = useAddDraftAnnotationMessage(wsId, draftId);
  const updateAnnotation = useUpdateDraftAnnotation(wsId, draftId);
  const deleteAnnotation = useDeleteDraftAnnotation(wsId, draftId);
  const [reply, setReply] = useState("");

  const resolved = annotation.state === "resolved" || annotation.state === "dismissed";

  const setState = (state: DraftAnnotationState) => {
    updateAnnotation.mutate({ id: annotation.id, patch: { state } });
  };

  const submitReply = () => {
    const body = reply.trim();
    if (!body) return;
    addMessage.mutate({ annotationId: annotation.id, body });
    setReply("");
  };

  return (
    <div
      data-annotation-card={annotation.id}
      className={cn(
        "border-b px-4 py-3 text-left transition-colors",
        active ? "bg-sidebar-accent" : "hover:bg-muted/50",
        resolved && "opacity-60",
      )}
    >
      <button type="button" className="flex w-full items-start gap-2" onClick={onSelect}>
        <Icon className="mt-0.5 h-4 w-4 shrink-0 text-muted-foreground" aria-hidden />
        <div className="min-w-0 flex-1">
          <p className="line-clamp-1 text-xs text-muted-foreground">
            “{annotation.quote || t(($) => $.annotations.no_quote)}”
          </p>
          {/* Collapsed preview: a single plain-text line so the card stays
              compact. The full markdown body renders in the expanded thread
              below. */}
          {!active && annotation.messages[0] && (
            <p className="mt-0.5 line-clamp-2 text-sm">{annotation.messages[0].body}</p>
          )}
          {flaggedChanged && (
            <span className="mt-1 inline-flex items-center gap-1 text-xs text-warning">
              <CircleAlert className="h-3 w-3" aria-hidden />
              {t(($) => $.annotations.text_changed)}
            </span>
          )}
        </div>
      </button>

      {active && (
        <div className="mt-3 space-y-3">
          {/* The full thread: every message (including the opener) renders as a
              conversation row — sender + relative timestamp + markdown body —
              so a structured Aye reply reads as prose, not a cramped wall. */}
          {annotation.messages.length > 0 && (
            <div className="space-y-3">
              {annotation.messages.map((m) => (
                <MessageRow key={m.id} {...m} />
              ))}
            </div>
          )}

          {annotation.type !== "highlight" && (
            <div className="space-y-1.5">
              <Textarea
                value={reply}
                onChange={(e) => setReply(e.target.value)}
                placeholder={t(($) => $.annotations.reply_placeholder)}
                rows={2}
                className="text-sm"
                onKeyDown={(e) => {
                  if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
                    e.preventDefault();
                    submitReply();
                  }
                }}
              />
              <div className="flex items-center justify-between">
                <div className="flex gap-1">
                  {!resolved ? (
                    <>
                      <Button size="sm" variant="ghost" className="h-7 px-2 text-xs" onClick={() => setState("resolved")}>
                        {t(($) => $.annotations.resolve)}
                      </Button>
                      <Button size="sm" variant="ghost" className="h-7 px-2 text-xs" onClick={() => setState("dismissed")}>
                        {t(($) => $.annotations.dismiss)}
                      </Button>
                    </>
                  ) : (
                    <Button size="sm" variant="ghost" className="h-7 px-2 text-xs" onClick={() => setState("open")}>
                      {t(($) => $.annotations.reopen)}
                    </Button>
                  )}
                </div>
                <Button
                  size="sm"
                  className="h-7 px-2 text-xs"
                  onClick={submitReply}
                  disabled={!reply.trim()}
                >
                  {t(($) => $.annotations.reply_submit)}
                </Button>
              </div>
            </div>
          )}

          {annotation.type === "highlight" && (
            <Button
              size="sm"
              variant="ghost"
              className="h-7 px-2 text-xs text-muted-foreground hover:text-destructive"
              onClick={() => deleteAnnotation.mutate(annotation.id)}
            >
              <Trash2 className="mr-1 h-3 w-3" />
              {t(($) => $.annotations.remove_highlight)}
            </Button>
          )}
        </div>
      )}
    </div>
  );
}

interface OrphanedTrayProps {
  wsId: string;
  draftId: string;
  orphaned: DraftAnnotation[];
  activeId: string | null;
}

function OrphanedTray({ wsId, draftId, orphaned, activeId }: OrphanedTrayProps) {
  const { t } = useT("drafts");
  const deleteAnnotation = useDeleteDraftAnnotation(wsId, draftId);
  const [open, setOpen] = useState(true);

  return (
    <div className="border-t">
      <button
        type="button"
        className="flex w-full items-center justify-between px-4 py-2 text-xs font-medium text-muted-foreground hover:bg-muted/50"
        onClick={() => setOpen((v) => !v)}
      >
        <span className="inline-flex items-center gap-1">
          <CircleAlert className="h-3.5 w-3.5" aria-hidden />
          {t(($) => $.annotations.orphaned_title)}
        </span>
        <Badge variant="outline">{orphaned.length}</Badge>
      </button>
      {open && (
        <div className="flex flex-col">
          {orphaned.map((a) => (
            <div
              key={a.id}
              data-orphaned-annotation={a.id}
              className={cn(
                "flex items-start gap-2 px-4 py-2",
                a.id === activeId ? "bg-sidebar-accent" : "hover:bg-muted/50",
              )}
            >
              <div className="min-w-0 flex-1">
                <p className="line-clamp-1 text-xs text-muted-foreground">
                  “{a.quote || t(($) => $.annotations.no_quote)}”
                </p>
                {a.messages[0] && <p className="mt-0.5 line-clamp-1 text-sm">{a.messages[0].body}</p>}
              </div>
              <Button
                size="sm"
                variant="ghost"
                className="h-6 w-6 shrink-0 p-0 text-muted-foreground hover:text-destructive"
                onClick={() => deleteAnnotation.mutate(a.id)}
                title={t(($) => $.annotations.delete)}
              >
                <Trash2 className="h-3.5 w-3.5" />
              </Button>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}
