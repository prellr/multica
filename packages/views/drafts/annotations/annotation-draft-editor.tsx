"use client";

import { useEffect, useMemo, useRef, useState } from "react";
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
import { useStartDraftTurn } from "@multica/core/drafts/turn-mutations";
import { createDraftYContext, destroyDraftYContext, useDraftYjs } from "@multica/core/drafts/collab";
import { useAuthStore } from "@multica/core/auth";
import type { DraftAnnotationType } from "@multica/core/types";
import { useWorkspaceId } from "@multica/core/hooks";
import { createEditorExtensions } from "../../editor/extensions";
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
import { DraftSidePanel } from "../conversation/draft-side-panel";
import { DraftSendControl, DraftTurnNarration } from "./draft-turn-narration";
import { createDraftSendShortcutExtension } from "./send-shortcut";
import {
  useAnnotationAnchoring,
  buildAnchorFromSelection,
} from "./use-annotation-anchoring";
import { buildDocTextIndex } from "./text-position";
import { seedYDocFromMarkdown } from "./seed-y-doc";
import { caretColorForId } from "./caret-color";
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
  // The in-flight draft-turn task id (slice 2). Transient UI state: one turn per
  // open draft, cleared on draft:turn_completed. Drives the Send control's
  // working state and the narration rail.
  const [activeTurnTaskId, setActiveTurnTaskId] = useState<string | null>(null);

  // --- The Send-turn action (lifted from DraftSendControl) ----------------
  // Owning the mutation here lets the SAME send fire from the header button,
  // the editor's Cmd/Ctrl+Enter keymap, and the window-level fallback — one
  // in-flight guard, no double-enqueue.
  const startTurn = useStartDraftTurn(draftId);
  const turnPending = startTurn.isPending;
  const turnWorking = activeTurnTaskId !== null || turnPending;

  const handleSend = () => {
    // In-flight guard: never enqueue a second turn while one is pending or open.
    if (turnWorking) return;
    startTurn.mutate(undefined, {
      onSuccess: (res) => {
        // An empty task_id means the schema fell back (drifted/garbled
        // response) — treat it as "didn't start" rather than entering a turn
        // state we can't narrate.
        if (res.task_id) setActiveTurnTaskId(res.task_id);
        else toast.error(t(($) => $.turn.start_failed));
      },
      onError: () => toast.error(t(($) => $.turn.start_failed)),
    });
  };

  // Hold the latest handleSend in a ref so the editor keymap (created once per
  // draft) and the window listener always call the current guard/state without
  // re-binding. Same pattern as the editor's onSubmitRef.
  const handleSendRef = useRef(handleSend);
  handleSendRef.current = handleSend;
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

  // --- Yjs co-editing floor (slice 3a) -----------------------------------
  // One Y.Doc + awareness channel per open draft. The editor mounts EMPTY and
  // Collaboration hydrates it from the fragment; the body is seeded into the
  // CRDT exactly once (seededRef guards against the double-apply that corrupts
  // the doc). The networked provider (useDraftYjs) replays the server log on
  // connect, so a fresh client converges and a reconnect re-syncs.
  const yctx = useMemo(() => createDraftYContext(), [draftId]);
  const seededRef = useRef(false);
  const currentUser = useAuthStore((s) => s.user);

  // The local peer's caret identity for CollaborationCaret. Memoized so the
  // editor's extension config gets a stable reference.
  const caretUser = useMemo(
    () => ({
      // The editor is always behind auth, so name/email are present in
      // practice; the email local-part is the last-resort label.
      name:
        currentUser?.name?.trim() ||
        currentUser?.email?.split("@")[0] ||
        "Anonymous",
      color: caretColorForId(currentUser?.id ?? draftId),
    }),
    [currentUser?.id, currentUser?.name, currentUser?.email, draftId],
  );

  // Destroy the previous draft's Y context when switching drafts / unmounting.
  useEffect(() => {
    seededRef.current = false;
    return () => destroyDraftYContext(yctx);
  }, [yctx]);

  // Connect the doc + awareness to the relay. Headless; returns presence state.
  useDraftYjs(draftId, yctx.doc, yctx.awareness);

  const draftBody = draft?.body ?? "";
  // Whether the server already holds persisted Yjs state for this draft. When
  // true, the provider's replay is authoritative and we MUST NOT seed (seeding
  // on top of the replayed log duplicates the body — the slice-3a duplication
  // bug). Defaults to false at the API boundary, so an older backend that omits
  // the field is treated as "brand-new draft" and the seed still runs.
  const hasYjsState = draft?.has_yjs_state === true;

  // Seed the Y.Doc from the loaded body, ONCE — and ONLY for a brand-new draft
  // (no persisted Yjs state). Runs after the draft query resolves (body + state
  // known).
  //
  // For a draft that ALREADY has persisted Yjs state we skip seeding entirely:
  // the networked provider (useDraftYjs) replays the persisted update log on
  // connect, and that replay is the authoritative content. Seeding the markdown
  // body as a SECOND set of CRDT items (a different client id/clock) would not
  // dedupe against the replayed items, so the body would exist twice and the
  // provider would persist the duplication on next sync — compounding on every
  // open. We still prime the snapshot baseline so the replayed content doesn't
  // trigger a redundant initial PUT.
  //
  // seedYDocFromMarkdown keeps its own `fragment.length > 0` guard as
  // defense-in-depth (e.g. a reconnect that hydrated the fragment before this
  // effect runs), but that guard alone can't gate this bug: at first open the
  // fragment is still empty when this effect fires, so the gate must be the
  // persisted-state flag, not fragment length.
  useEffect(() => {
    if (seededRef.current || draft === undefined) return;
    seededRef.current = true;
    if (!hasYjsState) {
      seedYDocFromMarkdown(yctx.doc, draftBody, {
        placeholder: t(($) => $.detail.body_placeholder),
      });
    }
    // The seeded content (or the body the server's replayed log holds) equals
    // the body the server already has, so prime the snapshot baseline to
    // suppress a redundant initial PUT once Collaboration hydrates the editor.
    lastSavedBodyRef.current = draftBody.trimEnd();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [yctx, draft?.id, draftBody, hasYjsState]);

  const editor = useEditor(
    {
      immediatelyRender: false,
      shouldRerenderOnTransaction: false,
      // NO `content`: under collaboration the editor MUST mount empty and let
      // Collaboration hydrate from the seeded/replayed Y.Doc. Passing content
      // here would double-apply and corrupt the CRDT.
      extensions: [
        ...createEditorExtensions({
          placeholder: t(($) => $.detail.body_placeholder),
          queryClient,
          collaboration: { doc: yctx.doc, awareness: yctx.awareness, user: caretUser },
        }),
        AnnotationDecorationExtension,
        // Cmd/Ctrl+Enter sends the draft turn from inside the editor. High
        // priority so it consumes Mod-Enter before StarterKit's HardBreak
        // inserts a line break. Drafts-only — not added to the shared factory.
        createDraftSendShortcutExtension(() => handleSendRef.current()),
      ],
      editorProps: {
        attributes: { class: "flex-1 rich-text-editor text-sm outline-none" },
      },
      onCreate: ({ editor: ed }) => {
        lastSavedBodyRef.current = ed.getMarkdown().trimEnd();
      },
      onUpdate: ({ editor: ed }) => {
        // Debounced markdown-snapshot writer. `body` is now DERIVED state: the
        // Y.Doc is the live truth, and this serializes a markdown snapshot every
        // other surface (graduation, list previews, Aye) reads. This fires for
        // BOTH local keystrokes AND remote-origin transactions (y-tiptap applies
        // a remote update as a local ProseMirror transaction), so any connected
        // client keeps the snapshot fresh.
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

        // Debounced re-anchor + decoration REPAINT. CRITICAL (slice 3a finding):
        // a remote Yjs update arrives as one coarse doc-spanning ReplaceStep, so
        // DecorationSet.map drops inline decorations. We therefore RECOMPUTE from
        // re-anchor truth and repaint via setAnnotationDecorations — and we must
        // run this for remote transactions too. onUpdate fires for them, so as
        // long as the recompute isn't gated to local-only edits (it isn't), the
        // repaint heals the dropped decorations after every remote edit.
        if (recomputeTimerRef.current) clearTimeout(recomputeTimerRef.current);
        recomputeTimerRef.current = setTimeout(() => {
          runRecompute(ed);
        }, RECOMPUTE_DEBOUNCE_MS);
      },
    },
    // Recreate the editor only when switching drafts (the Y context is keyed the
    // same way, so the new editor binds to the new draft's doc).
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

  // Window-level Cmd/Ctrl+Enter fallback for when focus is OUTSIDE the editor
  // (the thread panel, the Send button, the title input). When the editor IS
  // focused, its keymap already handled the event — bail to avoid a double-fire
  // (the editor's handler also consumes the keydown, but checking focus is the
  // robust dedupe that doesn't depend on event ordering).
  useEffect(() => {
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key !== "Enter" || !(e.metaKey || e.ctrlKey)) return;
      if (editor?.isFocused) return; // editor keymap owns this case
      e.preventDefault();
      handleSendRef.current();
    };
    document.addEventListener("keydown", onKeyDown);
    return () => document.removeEventListener("keydown", onKeyDown);
  }, [editor]);

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
            <DraftSendControl
              onSend={handleSend}
              working={turnWorking}
              pending={turnPending}
            />
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

        {/* Aye's Send-turn narration rail (slice 2). Renders only while a turn
            is in flight; the live thinking/tool stream maps into FF
            ThinkingSteps. */}
        <DraftTurnNarration
          draftId={draft.id}
          activeTaskId={activeTurnTaskId}
          onTurnChange={setActiveTurnTaskId}
        />
      </div>

      <DraftSidePanel
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
 *
 * Both endpoints are mapped through the SAME projection that re-anchoring uses
 * (buildDocTextIndex.posToOffset), and the quote is sliced out of that
 * projection's `text`. This is the correctness contract: capture time and
 * re-anchor time share one block-separator convention, so a selection spanning
 * two list items (which buildDocTextIndex concatenates with no separator)
 * captures the exact span — unlike `doc.textBetween(from, to, "\n")`, which
 * would inject a separator at the list-item boundary and yield a string that
 * doesn't exist verbatim in the projection.
 */
function buildDocTextIndexForSelection(
  editor: NonNullable<ReturnType<typeof useEditor>>,
  from: number,
  to: number,
): { text: string; fromOffset: number; toOffset: number } {
  const { text, posToOffset } = buildDocTextIndex(editor.state.doc);
  const fromOffset = posToOffset(from);
  const toOffset = posToOffset(to);
  return { text, fromOffset, toOffset };
}

