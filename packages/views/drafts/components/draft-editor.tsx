"use client";

import { useEffect, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Trash2, X } from "lucide-react";
import { draftDetailOptions, useUpdateDraft, useDeleteDraft } from "@multica/core/drafts";
import { useWorkspaceId } from "@multica/core/hooks";
import { ContentEditor, type ContentEditorRef, TitleEditor } from "../../editor";
import { PageHeader } from "../../layout/page-header";
import { Button } from "@multica/ui/components/ui/button";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { toast } from "sonner";
import { useT } from "../../i18n";

interface DraftEditorProps {
  draftId: string;
  /** Clears the master-detail selection (closes the editor pane). */
  onClose: () => void;
  /** Called after a successful delete so the parent can clear its selection. */
  onDeleted: (id: string) => void;
}

/**
 * Draft editor pane (right side of the master-detail). Title + markdown body,
 * both autosaving through the optimistic {@link useUpdateDraft} mutation:
 *   - Title commits on blur / Enter (TitleEditor).
 *   - Body autosaves on a 1s debounce (ContentEditor's onUpdate).
 *
 * Mirrors the memory artifact detail editor. Slice 0 has no annotations,
 * agent, or co-editing surface — just single-user editing.
 */
export function DraftEditor({ draftId, onClose, onDeleted }: DraftEditorProps) {
  const { t } = useT("drafts");
  const wsId = useWorkspaceId();

  const { data: draft, isLoading } = useQuery(draftDetailOptions(wsId, draftId));

  const updateDraft = useUpdateDraft();
  const deleteDraft = useDeleteDraft();

  // Pull markdown on title-blur so persisting the title also flushes any
  // in-flight body edits (mirrors the memory editor).
  const contentRef = useRef<ContentEditorRef>(null);

  // Local title mirrors the server-side draft.title until the user types, then
  // drifts and syncs back on save / remount.
  const [titleDraft, setTitleDraft] = useState("");
  useEffect(() => {
    if (draft) setTitleDraft(draft.title);
  }, [draft?.id, draft?.title]);

  if (isLoading || !draft) {
    return (
      <div className="flex h-full flex-col">
        <PageHeader className="justify-end px-5">
          <Button size="sm" variant="ghost" className="h-7 w-7 p-0" onClick={onClose}>
            <X className="h-3.5 w-3.5" />
          </Button>
        </PageHeader>
        <div className="mx-auto w-full max-w-3xl flex-1 space-y-3 px-8 py-6">
          <Skeleton className="h-9 w-2/3" />
          <Skeleton className="h-32 w-full" />
        </div>
      </div>
    );
  }

  const saveTitle = (next: string) => {
    const trimmed = next.trim();
    if (trimmed === draft.title) return;
    updateDraft.mutate(
      { id: draft.id, patch: { title: trimmed } },
      { onError: () => toast.error(t(($) => $.errors.save_failed)) },
    );
  };

  const saveBody = (markdown: string) => {
    if (markdown === draft.body) return;
    updateDraft.mutate(
      { id: draft.id, patch: { body: markdown } },
      { onError: () => toast.error(t(($) => $.errors.save_failed)) },
    );
  };

  const handleDelete = () => {
    if (!window.confirm(t(($) => $.detail.delete_confirm))) return;
    deleteDraft.mutate(draft.id, {
      onSuccess: () => {
        toast.success(t(($) => $.detail.delete_success));
        onDeleted(draft.id);
      },
      onError: () => toast.error(t(($) => $.errors.delete_failed)),
    });
  };

  return (
    <div className="flex h-full flex-col">
      <PageHeader className="justify-between px-5">
        <span className="truncate text-sm font-medium">
          {draft.title.trim() || t(($) => $.detail.untitled)}
        </span>
        <div className="flex items-center gap-2">
          <Button
            size="sm"
            variant="ghost"
            className="text-muted-foreground hover:text-destructive"
            onClick={handleDelete}
            disabled={deleteDraft.isPending}
          >
            <Trash2 className="mr-1 h-3.5 w-3.5" />
            {t(($) => $.detail.delete_button)}
          </Button>
          <Button size="sm" variant="ghost" className="h-7 w-7 p-0" onClick={onClose}>
            <X className="h-3.5 w-3.5" />
          </Button>
        </div>
      </PageHeader>

      <div className="flex-1 overflow-y-auto">
        <div className="mx-auto max-w-3xl space-y-3 px-8 py-6">
          <TitleEditor
            key={draft.id}
            defaultValue={titleDraft}
            placeholder={t(($) => $.detail.title_placeholder)}
            className="text-2xl font-semibold"
            onChange={setTitleDraft}
            onSubmit={() => saveTitle(titleDraft)}
            onBlur={() => saveTitle(titleDraft)}
          />
          <div className="pt-2">
            <ContentEditor
              key={draft.id}
              ref={contentRef}
              defaultValue={draft.body}
              placeholder={t(($) => $.detail.body_placeholder)}
              onUpdate={saveBody}
              debounceMs={1000}
            />
          </div>
        </div>
      </div>
    </div>
  );
}
