"use client";

import { useEffect, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useEditor, EditorContent } from "@tiptap/react";
import { Trash2, X } from "lucide-react";
import {
  draftDetailOptions,
  useUpdateDraft,
  useDeleteDraft,
  draftAnnotationListOptions,
  useCreateDraftAnnotation,
} from "@multica/core/drafts";
import type { DraftAnnotationType } from "@multica/core/types";
import { useWorkspaceId } from "@multica/core/hooks";
import { createEditorExtensions } from "../../editor/extensions";
import { preprocessMarkdown } from "../../editor/utils/preprocess";
import { PageHeader } from "../../layout/page-header";
import { Button } from "@multica/ui/components/ui/button";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { TitleEditor } from "../../editor";
import { toast } from "sonner";
import { useT } from "../../i18n";
import {
  AnnotationDecorationExtension,
} from "./decoration-extension";
import { AnnotationSelectToolbar } from "./annotation-select-toolbar";
import { AnnotationThreadPanel } from "./annotation-thread-panel";
import {
  useAnnotationAnchoring,
  buildAnchorFromSelection,
} from "./use-annotation-anchoring";
import { buildDocTextIndex } from "./text-position";
import "../../editor/content-editor.css";
import "./annotations.css";

const RECOMPUTE_DEBOUNCE_MS = 400;
const SAVE_DEBOUNCE_MS = 1000;

interface AnnotationDraftEditorProps {
  draftId: string;
  onClose: () => void;
  onDeleted: (id: string) => void;
}

/**
 * The slice-1 draft editor: the slice-0 title+body autosave surface PLUS the
 * non-destructive annotation overlay.
 *
 * Unlike the slice-0 DraftEditor (which used the generic ContentEditor), this
 * mounts its own Tiptap editor so it can own the annotation decoration plugin
 * and the re-anchoring loop. The body is still autosaved as clean markdown; the
 * annotations live entirely as decorations + a right-rail panel, never inlined.
 *
 * Re-anchoring loop: after every edit (debounced) and whenever the annotation
 * list changes, recompute() re-anchors all annotations against the editor's
 * current text, repaints decorations, applies the tiered posture
 * (matched/shifted → follow; changed-on-open → flag; orphaned → tray), and
 * PATCHes moved anchors back to the server.
 */
export function AnnotationDraftEditor({ draftId, onClose, onDeleted }: AnnotationDraftEditorProps) {
  const { t } = useT("drafts");
  const wsId = useWorkspaceId();

  const { data: draft, isLoading } = useQuery(draftDetailOptions(wsId, draftId));
  const { data: annotations = [] } = useQuery(draftAnnotationListOptions(wsId, draftId));

  const updateDraft = useUpdateDraft();
  const deleteDraft = useDeleteDraft();
  const createAnnotation = useCreateDraftAnnotation(wsId, draftId);
  const queryClient = useQueryClient();

  const { recompute, annotationsSignature } = useAnnotationAnchoring(wsId, draftId, annotations);

  const [activeId, setActiveId] = useState<string | null>(null);
  // Local title mirror, synced to the server title until the user types.
  const [titleDraft, setTitleDraft] = useState("");
  useEffect(() => {
    if (draft) setTitleDraft(draft.title);
  }, [draft?.id, draft?.title]);
  const [anchoringResult, setAnchoringResult] = useState(() => ({
    anchored: [] as ReturnType<typeof recompute>["anchored"],
    decorationRanges: [] as ReturnType<typeof recompute>["decorationRanges"],
    orphaned: [] as ReturnType<typeof recompute>["orphaned"],
  }));

  const saveTimerRef = useRef<ReturnType<typeof setTimeout>>(undefined);
  const recomputeTimerRef = useRef<ReturnType<typeof setTimeout>>(undefined);
  const lastSavedBodyRef = useRef<string>("");

  const draftBody = draft?.body ?? "";

  const editor = useEditor(
    {
      immediatelyRender: false,
      shouldRerenderOnTransaction: false,
      content: draftBody ? preprocessMarkdown(draftBody) : "",
      contentType: draftBody ? "markdown" : undefined,
      extensions: [
        ...createEditorExtensions({ placeholder: t(($) => $.detail.body_placeholder), queryClient }),
        AnnotationDecorationExtension,
      ],
      editorProps: {
        attributes: { class: "flex-1 rich-text-editor text-sm outline-none" },
      },
      onCreate: ({ editor: ed }) => {
        lastSavedBodyRef.current = ed.getMarkdown().trimEnd();
      },
      onUpdate: ({ editor: ed }) => {
        // Debounced autosave of the markdown body.
        if (saveTimerRef.current) clearTimeout(saveTimerRef.current);
        saveTimerRef.current = setTimeout(() => {
          const md = ed.getMarkdown().trimEnd();
          if (md === lastSavedBodyRef.current) return;
          lastSavedBodyRef.current = md;
          updateDraft.mutate(
            { id: draftId, patch: { body: md } },
            { onError: () => toast.error(t(($) => $.errors.save_failed)) },
          );
        }, SAVE_DEBOUNCE_MS);

        // Debounced re-anchoring of annotations against the new text.
        if (recomputeTimerRef.current) clearTimeout(recomputeTimerRef.current);
        recomputeTimerRef.current = setTimeout(() => {
          runRecompute(ed);
        }, RECOMPUTE_DEBOUNCE_MS);
      },
    },
    // Recreate the editor only when switching drafts.
    [draftId],
  );

  // Recompute + push decorations into the plugin.
  const runRecompute = (ed: NonNullable<typeof editor>) => {
    const result = recompute(ed);
    setAnchoringResult(result);
    ed.commands.setAnnotationDecorations(result.decorationRanges);
  };

  // Re-anchor whenever the annotation list (or its anchors/state) changes.
  useEffect(() => {
    if (!editor) return;
    runRecompute(editor);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [editor, annotationsSignature]);

  // Clean up timers.
  useEffect(() => {
    return () => {
      if (saveTimerRef.current) clearTimeout(saveTimerRef.current);
      if (recomputeTimerRef.current) clearTimeout(recomputeTimerRef.current);
    };
  }, []);

  const handlePick = (type: DraftAnnotationType) => {
    if (!editor) return;
    const { from, to } = editor.state.selection;
    if (to <= from) return;
    const { text, ...rest } = buildDocTextIndexForSelection(editor, from, to);
    const anchor = buildAnchorFromSelection(text, rest.fromOffset, rest.toOffset);
    if (!anchor.quote) return;
    createAnnotation.mutate(
      {
        type,
        quote: anchor.quote,
        context_before: anchor.contextBefore,
        context_after: anchor.contextAfter,
        pos_hint: anchor.posHint,
      },
      {
        onSuccess: (created) => {
          // Open the thread for a non-highlight so the user can type the comment.
          if (type !== "highlight" && created.id) setActiveId(created.id);
        },
        onError: () => toast.error(t(($) => $.errors.annotation_create_failed)),
      },
    );
    // Drop the selection so the toolbar hides.
    editor.commands.setTextSelection(to);
  };

  const handleDelete = () => {
    if (!draft) return;
    if (!window.confirm(t(($) => $.detail.delete_confirm))) return;
    deleteDraft.mutate(draft.id, {
      onSuccess: () => {
        toast.success(t(($) => $.detail.delete_success));
        onDeleted(draft.id);
      },
      onError: () => toast.error(t(($) => $.errors.delete_failed)),
    });
  };

  if (isLoading || !draft || !editor) {
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

  return (
    <div className="flex h-full">
      <div className="flex h-full flex-1 flex-col">
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
            <div className="relative pt-2">
              <EditorContent className="flex flex-1 flex-col" editor={editor} />
              <AnnotationSelectToolbar editor={editor} onPick={handlePick} />
            </div>
          </div>
        </div>
      </div>

      <AnnotationThreadPanel
        wsId={wsId}
        draftId={draftId}
        anchored={anchoringResult.anchored}
        orphaned={anchoringResult.orphaned}
        activeId={activeId}
        onSelect={setActiveId}
      />
    </div>
  );
}

/**
 * Convert the editor's current PM selection [from, to) into flat-text offsets
 * for anchoring, returning the full doc text alongside.
 */
function buildDocTextIndexForSelection(
  editor: NonNullable<ReturnType<typeof useEditor>>,
  from: number,
  to: number,
): { text: string; fromOffset: number; toOffset: number } {
  const { text } = buildDocTextIndex(editor.state.doc);
  // The selected slice's plain text, used to locate the offset within the
  // flat projection. This is robust against block boundaries because we search
  // for the exact selected text near the selection.
  const selectedText = editor.state.doc.textBetween(from, to, "\n");
  const approxOffset = approxOffsetForPos(editor, from);
  const fromOffset = locateOffset(text, selectedText, approxOffset);
  return { text, fromOffset, toOffset: fromOffset + selectedText.length };
}

/** Rough flat-text offset for a PM position: count text chars before it. */
function approxOffsetForPos(editor: NonNullable<ReturnType<typeof useEditor>>, pos: number): number {
  let offset = 0;
  editor.state.doc.nodesBetween(0, pos, (node, nodePos) => {
    if (node.isText && node.text) {
      const end = Math.min(pos, nodePos + node.text.length);
      offset += Math.max(0, end - nodePos);
    }
    return true;
  });
  return offset;
}

/** Find `needle` in `haystack` nearest to `approx`. */
function locateOffset(haystack: string, needle: string, approx: number): number {
  if (!needle) return Math.max(0, Math.min(approx, haystack.length));
  let best = haystack.indexOf(needle);
  if (best === -1) return Math.max(0, Math.min(approx, haystack.length));
  let bestDist = Math.abs(best - approx);
  let i = haystack.indexOf(needle, best + 1);
  while (i !== -1) {
    const dist = Math.abs(i - approx);
    if (dist < bestDist) {
      best = i;
      bestDist = dist;
    }
    i = haystack.indexOf(needle, i + 1);
  }
  return best;
}

