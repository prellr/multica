"use client";

import { useEffect } from "react";
import { useCurrentWorkspace } from "@multica/core/paths";
import { paths } from "@multica/core/paths";
import { useNavigation } from "../../navigation";

interface TaskDetailPageProps {
  taskId: string;
}

/**
 * Legacy redirect — `/tasks/:id` now resolves into the master-detail
 * sidebar on `/tasks` via `?task=<id>`. This component exists so
 * pre-sidebar shared links (and any unmigrated caller) still land on
 * something meaningful instead of 404'ing.
 *
 * `paths.workspace(slug).taskDetail(id)` already returns the new sidebar
 * URL, so other than this redirect there's no caller minting the old
 * `/tasks/:id` shape. If you find one, switch it to the path helper.
 *
 * Returns null on the brief render before the navigation effect fires —
 * a flash of "Loading…" would be slower than just letting the next
 * frame paint the sidebar.
 */
export function TaskDetailPage({ taskId }: TaskDetailPageProps) {
  const workspace = useCurrentWorkspace();
  const { replace } = useNavigation();

  useEffect(() => {
    if (!workspace) return;
    replace(paths.workspace(workspace.slug).taskDetail(taskId));
  }, [taskId, workspace, replace]);

  return null;
}
