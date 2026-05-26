"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import { ListTodo } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { cn } from "@multica/ui/lib/utils";
import {
  ResizablePanelGroup,
  ResizablePanel,
  ResizableHandle,
} from "@multica/ui/components/ui/resizable";
import { useDefaultLayout } from "react-resizable-panels";
import { taskListOptions } from "@multica/core/tasks";
import { useWorkspaceId } from "@multica/core/hooks";
import { useAuthStore } from "@multica/core/auth";
import type { ListTasksParams } from "@multica/core/types";
import { PageHeader } from "../../layout/page-header";
import { useNavigation } from "../../navigation";
import { useT } from "../../i18n";
import { QuickAddTask } from "./quick-add-task";
import { TaskDetailSidebar } from "./task-detail-sidebar";
import { SortableTaskRow } from "./sortable-task-row";
import { TaskDndProvider } from "./task-dnd-context";

type Scope = "mine" | "all";

/**
 * Workspace task list, master-detail. Left panel: scoped task list with
 * Mine/All toggle + inline quick-add. Right panel: detail sidebar for
 * the selected task, with subtasks inline.
 *
 * Selection state lives in the URL as `?task=<id>` (mirrors the inbox
 * pattern). Clicking a row in either the main list or the subtasks list
 * pushes to the same URL shape; clicking close clears the param. The
 * legacy `/tasks/:id` route still resolves — it redirects here on mount,
 * preserving any shared links.
 *
 * Flat list by design — no per-status buckets, no kanban. If the surface
 * grows the equivalent of IssuesPage, the lightness thesis has failed
 * and we should consolidate the two surfaces instead.
 */
export function TasksPage() {
  const { t } = useT("tasks");
  const wsId = useWorkspaceId();
  const user = useAuthStore((s) => s.user);
  const { searchParams, replace, pathname } = useNavigation();
  const [scope, setScope] = useState<Scope>("mine");

  const selectedId = searchParams.get("task") ?? "";

  const setSelected = useCallback(
    (id: string) => {
      const next = new URLSearchParams(searchParams);
      if (id) next.set("task", id);
      else next.delete("task");
      const qs = next.toString();
      replace(qs ? `${pathname}?${qs}` : pathname);
    },
    [pathname, replace, searchParams],
  );

  // Mine/All filter. Mine falls back to All when the user isn't hydrated
  // yet — better than passing a blank assignee_id and getting an empty
  // list. The auth store is normally hydrated before this page mounts;
  // this is a defensive fallback.
  const filter: ListTasksParams = useMemo(
    () => (scope === "mine" && user?.id ? { assignee_id: user.id } : {}),
    [scope, user?.id],
  );

  const { data: tasks = [], isLoading } = useQuery(taskListOptions(wsId, filter));

  // If the selected task isn't in the visible list (filtered out, deleted,
  // never in this scope), clear the selection so the right panel doesn't
  // show stale data. The detail query still drives the panel; this just
  // keeps the URL honest when the user changes scope.
  useEffect(() => {
    if (!selectedId || isLoading) return;
    if (!tasks.some((t) => t.id === selectedId)) {
      // Don't auto-clear: a task could be a valid subtask of something in
      // the visible list, OR the user opened the sidebar via a shared
      // link. Leaving the selection lets the detail query 404 gracefully
      // through its own error path.
    }
  }, [selectedId, tasks, isLoading]);

  const { defaultLayout, onLayoutChanged } = useDefaultLayout({
    id: "multica_tasks_layout",
  });

  const listHeader = (
    <PageHeader>
      <ListTodo className="mr-2 h-4 w-4 text-muted-foreground" aria-hidden />
      <span className="text-sm font-medium">{t(($) => $.page.title)}</span>
      <ScopeToggle scope={scope} onChange={setScope} />
    </PageHeader>
  );

  const listBody = isLoading ? (
    <div className="flex-1 overflow-auto">
      {Array.from({ length: 8 }).map((_, i) => (
        <div key={i} className="flex items-center gap-3 border-b px-4 py-2.5">
          <Skeleton className="h-4 w-4 rounded-full" />
          <Skeleton className="h-4 flex-1 max-w-md" />
        </div>
      ))}
    </div>
  ) : tasks.length === 0 ? (
    <EmptyState scope={scope} />
  ) : (
    // Drag-and-drop wiring. acceptReparent=true so the user can drop a
    // task onto another row to make it a child. parentScope=null
    // because the main list mixes top-level and subtask rows — see
    // task-dnd-context's reorder branch for how it preserves each
    // dragged row's existing parent_issue_id (the backend fix in
    // PR-fix-task-update prevents stale-parent regressions).
    <div className="flex-1 overflow-auto">
      <TaskDndProvider items={tasks} acceptReparent>
        {tasks.map((task) => (
          <SortableTaskRow key={task.id} task={task} selected={task.id === selectedId} />
        ))}
      </TaskDndProvider>
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
          {/* Quick-add is always mounted so the surface is ready to
            * accept input immediately on first paint. */}
          <QuickAddTask />
          {listBody}
        </div>
      </ResizablePanel>
      <ResizableHandle />
      <ResizablePanel id="detail" minSize="40%">
        {selectedId ? (
          <TaskDetailSidebar
            key={selectedId}
            taskId={selectedId}
            onClose={() => setSelected("")}
          />
        ) : (
          <SelectPrompt />
        )}
      </ResizablePanel>
    </ResizablePanelGroup>
  );
}

interface ScopeToggleProps {
  scope: Scope;
  onChange: (scope: Scope) => void;
}

function ScopeToggle({ scope, onChange }: ScopeToggleProps) {
  const { t } = useT("tasks");
  return (
    <div className="ml-4 inline-flex items-center rounded-md border bg-muted/40 p-0.5 text-xs">
      <ScopeButton active={scope === "mine"} onClick={() => onChange("mine")}>
        {t(($) => $.page.scope_mine)}
      </ScopeButton>
      <ScopeButton active={scope === "all"} onClick={() => onChange("all")}>
        {t(($) => $.page.scope_all)}
      </ScopeButton>
    </div>
  );
}

function ScopeButton({
  active,
  onClick,
  children,
}: {
  active: boolean;
  onClick: () => void;
  children: React.ReactNode;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      aria-pressed={active}
      className={cn(
        "rounded px-2 py-0.5 transition-colors",
        active
          ? "bg-background text-foreground shadow-sm"
          : "text-muted-foreground hover:text-foreground",
      )}
    >
      {children}
    </button>
  );
}

function EmptyState({ scope }: { scope: Scope }) {
  const { t } = useT("tasks");
  const titleKey = scope === "mine" ? "empty_mine_title" : "empty_title";
  const descKey = scope === "mine" ? "empty_mine_description" : "empty_description";
  return (
    <div className="flex flex-1 flex-col items-center justify-center gap-3 p-8 text-center">
      <ListTodo className="h-10 w-10 text-muted-foreground" aria-hidden />
      <div>
        <h2 className="text-lg font-medium">{t(($) => $.page[titleKey])}</h2>
        <p className="mt-1 max-w-md text-sm text-muted-foreground">
          {t(($) => $.page[descKey])}
        </p>
      </div>
    </div>
  );
}

function SelectPrompt() {
  const { t } = useT("tasks");
  return (
    <div className="flex h-full flex-col items-center justify-center gap-2 p-8 text-center">
      <ListTodo className="h-8 w-8 text-muted-foreground/60" aria-hidden />
      <p className="max-w-xs text-sm text-muted-foreground">
        {t(($) => $.detail.select_prompt)}
      </p>
    </div>
  );
}
