"use client";

import { useState, useMemo } from "react";
import { ListTodo } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { cn } from "@multica/ui/lib/utils";
import { taskListOptions } from "@multica/core/tasks";
import { useWorkspaceId } from "@multica/core/hooks";
import { useAuthStore } from "@multica/core/auth";
import type { ListTasksParams } from "@multica/core/types";
import { PageHeader } from "../../layout/page-header";
import { useT } from "../../i18n";
import { TaskRow } from "./task-row";
import { QuickAddTask } from "./quick-add-task";

type Scope = "mine" | "all";

/**
 * Workspace task list with a Mine/All toggle. Defaults to "Mine" since
 * the sidebar entry lives under the user (per the brand spec) — framing
 * tasks as a personal surface. The toggle is a one-click escape valve to
 * the workspace-wide view; we don't paginate or filter beyond that
 * because the lightness thesis says tasks should never need it.
 *
 * Flat by design — no per-status buckets, no kanban, no view-mode
 * switcher. If the surface grows the equivalent of {@link IssuesPage},
 * the lightness thesis has failed and we should consolidate the two
 * surfaces instead.
 */
export function TasksPage() {
  const { t } = useT("tasks");
  const wsId = useWorkspaceId();
  const user = useAuthStore((s) => s.user);
  const [scope, setScope] = useState<Scope>("mine");

  // The "mine" scope falls back to "all" if for some reason we don't have
  // a user yet — better than passing a blank assignee_id and getting an
  // empty list. The auth store is hydrated before this page mounts in
  // practice, so this is a defensive fallback.
  const filter: ListTasksParams = useMemo(
    () => (scope === "mine" && user?.id ? { assignee_id: user.id } : {}),
    [scope, user?.id],
  );

  const { data: tasks = [], isLoading } = useQuery(taskListOptions(wsId, filter));

  return (
    <div className="flex h-full flex-col">
      <PageHeader>
        <ListTodo className="mr-2 h-4 w-4 text-muted-foreground" aria-hidden />
        <span className="text-sm font-medium">{t(($) => $.page.title)}</span>
        <ScopeToggle scope={scope} onChange={setScope} />
      </PageHeader>

      {/* Quick-add is always mounted (even in loading and empty states) so
        * the user can start adding tasks immediately on first load — the
        * surface's whole point is being fast. */}
      <QuickAddTask />

      {isLoading ? (
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
        <div className="flex-1 overflow-auto">
          {tasks.map((task) => (
            <TaskRow key={task.id} task={task} />
          ))}
        </div>
      )}
    </div>
  );
}

interface ScopeToggleProps {
  scope: Scope;
  onChange: (scope: Scope) => void;
}

/** Two-button scope toggle. Inline in the page header rather than a
 *  separate row to keep vertical density tight on the lightweight
 *  surface. Tiny by design — bigger filter UIs would push tasks toward
 *  the issue surface. */
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
