"use client";

import { useCallback } from "react";
import { NotebookPen, Plus } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { Button } from "@multica/ui/components/ui/button";
import { cn } from "@multica/ui/lib/utils";
import {
  ResizablePanelGroup,
  ResizablePanel,
  ResizableHandle,
} from "@multica/ui/components/ui/resizable";
import { useDefaultLayout } from "react-resizable-panels";
import { draftListOptions, useCreateDraft } from "@multica/core/drafts";
import { useWorkspaceId } from "@multica/core/hooks";
import type { Draft } from "@multica/core/types";
import { PageHeader } from "../../layout/page-header";
import { useNavigation } from "../../navigation";
import { useT, useTimeAgo } from "../../i18n";
import { AnnotationDraftEditor } from "../annotations/annotation-draft-editor";

/**
 * Drafts master-detail page. Left panel: the user's drafts (newest-edited
 * first) with a "New draft" affordance. Right panel: the editor for the
 * selected draft (title + markdown body, autosave).
 *
 * Selection lives in the URL as `?draft=<id>` — mirrors the tasks page. Drafts
 * are owner-scoped server-side, so there's no Mine/All toggle; every draft in
 * the list belongs to the current user.
 *
 * Slice 0 has no annotations, agent, or co-editing. If/when those land they
 * slot into the editor pane without changing this master-detail shell.
 */
export function DraftsPage() {
  const { t } = useT("drafts");
  const wsId = useWorkspaceId();
  const { searchParams, replace, pathname } = useNavigation();

  const selectedId = searchParams.get("draft") ?? "";

  const setSelected = useCallback(
    (id: string) => {
      const next = new URLSearchParams(searchParams);
      if (id) next.set("draft", id);
      else next.delete("draft");
      const qs = next.toString();
      replace(qs ? `${pathname}?${qs}` : pathname);
    },
    [pathname, replace, searchParams],
  );

  const { data: drafts = [], isLoading } = useQuery(draftListOptions(wsId));
  const createDraft = useCreateDraft();

  const handleNewDraft = useCallback(() => {
    createDraft.mutate(
      {},
      {
        // Select the new draft once the server returns its real id, so the
        // editor opens on a persisted row (not the optimistic temp id).
        onSuccess: (draft) => setSelected(draft.id),
      },
    );
  }, [createDraft, setSelected]);

  const { defaultLayout, onLayoutChanged } = useDefaultLayout({
    id: "multica_drafts_layout",
  });

  const listHeader = (
    <PageHeader className="justify-between">
      <div className="flex items-center">
        <NotebookPen className="mr-2 h-4 w-4 text-muted-foreground" aria-hidden />
        <span className="text-sm font-medium">{t(($) => $.page.title)}</span>
      </div>
      <Button
        size="sm"
        variant="ghost"
        className="text-muted-foreground"
        onClick={handleNewDraft}
        disabled={createDraft.isPending}
      >
        <Plus className="mr-1 h-3.5 w-3.5" />
        {t(($) => $.page.new_draft)}
      </Button>
    </PageHeader>
  );

  const listBody = isLoading ? (
    <div className="flex-1 overflow-auto">
      {Array.from({ length: 8 }).map((_, i) => (
        <div key={i} className="flex flex-col gap-1.5 border-b px-4 py-3">
          <Skeleton className="h-4 w-2/3" />
          <Skeleton className="h-3 w-20" />
        </div>
      ))}
    </div>
  ) : drafts.length === 0 ? (
    <EmptyState onNewDraft={handleNewDraft} pending={createDraft.isPending} />
  ) : (
    <div className="flex-1 overflow-auto">
      {drafts.map((draft) => (
        <DraftRow
          key={draft.id}
          draft={draft}
          selected={draft.id === selectedId}
          onSelect={() => setSelected(draft.id)}
        />
      ))}
    </div>
  );

  return (
    <ResizablePanelGroup
      orientation="horizontal"
      className="flex-1 min-h-0"
      defaultLayout={defaultLayout}
      onLayoutChanged={onLayoutChanged}
    >
      <ResizablePanel
        id="list"
        defaultSize={380}
        minSize={280}
        maxSize={560}
        groupResizeBehavior="preserve-pixel-size"
      >
        <div className="flex h-full flex-col border-r">
          {listHeader}
          {listBody}
        </div>
      </ResizablePanel>
      <ResizableHandle />
      <ResizablePanel id="detail" minSize="40%">
        {selectedId ? (
          <AnnotationDraftEditor
            key={selectedId}
            draftId={selectedId}
            onClose={() => setSelected("")}
            onDeleted={(id) => {
              if (id === selectedId) setSelected("");
            }}
          />
        ) : (
          <SelectPrompt />
        )}
      </ResizablePanel>
    </ResizablePanelGroup>
  );
}

interface DraftRowProps {
  draft: Draft;
  selected: boolean;
  onSelect: () => void;
}

function DraftRow({ draft, selected, onSelect }: DraftRowProps) {
  const { t } = useT("drafts");
  const timeAgo = useTimeAgo();
  const title = draft.title.trim() || t(($) => $.detail.untitled);
  return (
    <button
      type="button"
      onClick={onSelect}
      className={cn(
        "flex w-full flex-col items-start gap-1 border-b px-4 py-3 text-left transition-colors",
        selected ? "bg-sidebar-accent" : "hover:bg-muted/50",
      )}
    >
      <span className="line-clamp-1 w-full text-sm font-medium">{title}</span>
      <span className="text-xs text-muted-foreground">{timeAgo(draft.updated_at)}</span>
    </button>
  );
}

function EmptyState({ onNewDraft, pending }: { onNewDraft: () => void; pending: boolean }) {
  const { t } = useT("drafts");
  return (
    <div className="flex flex-1 flex-col items-center justify-center gap-3 p-8 text-center">
      <NotebookPen className="h-10 w-10 text-muted-foreground" aria-hidden />
      <div>
        <h2 className="text-lg font-medium">{t(($) => $.page.empty_title)}</h2>
        <p className="mt-1 max-w-md text-sm text-muted-foreground">
          {t(($) => $.page.empty_description)}
        </p>
      </div>
      <Button size="sm" variant="outline" onClick={onNewDraft} disabled={pending}>
        <Plus className="mr-1 h-3.5 w-3.5" />
        {t(($) => $.page.new_draft)}
      </Button>
    </div>
  );
}

function SelectPrompt() {
  const { t } = useT("drafts");
  return (
    <div className="flex h-full flex-col items-center justify-center gap-2 p-8 text-center">
      <NotebookPen className="h-8 w-8 text-muted-foreground/60" aria-hidden />
      <p className="max-w-xs text-sm text-muted-foreground">
        {t(($) => $.detail.select_prompt)}
      </p>
    </div>
  );
}
