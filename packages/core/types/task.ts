import type { Label } from "./label";
import type { IssueAssigneeType, IssueMetadata } from "./issue";

/**
 * Tasks are issue rows discriminated by `kind = 'task'` on the backend.
 * They share the same backbone (assignees, parent/child, project, metadata)
 * but render through a lighter human-centric surface. See server migration
 * 116 for the schema split and server task.sql for the query-layer
 * enforcement.
 *
 * Three deliberate deltas from {@link Issue}:
 *   - No `number` / `identifier` — tasks have no human-readable ID. The
 *     backend leaves `number` NULL; the wire response omits both fields.
 *   - No `priority` — the lighter surface drops it.
 *   - Status is narrower — only the subset the handler enforces.
 *
 * Reactions are intentionally not modeled in v1 to keep the surface tight;
 * they can be added later without breaking consumers (optional field).
 */

export type TaskStatus = "todo" | "in_progress" | "done" | "cancelled";

export type TaskAssigneeType = IssueAssigneeType;

export interface Task {
  id: string;
  workspace_id: string;
  /** Always "task". Present so consumers that hold both issues and tasks
   *  in the same surface can discriminate without runtime checks. */
  kind: "task";
  title: string;
  description: string | null;
  status: TaskStatus;
  assignee_type: TaskAssigneeType | null;
  assignee_id: string | null;
  creator_type: TaskAssigneeType;
  creator_id: string;
  parent_issue_id: string | null;
  project_id: string | null;
  position: number;
  start_date: string | null;
  due_date: string | null;
  metadata?: IssueMetadata;
  labels?: Label[];
  created_at: string;
  updated_at: string;
}

export interface ListTasksParams {
  workspace_id?: string;
  status?: TaskStatus;
  assignee_id?: string;
  creator_id?: string;
  project_id?: string;
  parent_issue_id?: string;
  limit?: number;
  offset?: number;
}

export interface ListTasksResponse {
  tasks: Task[];
  total: number;
}

export interface CreateTaskRequest {
  title: string;
  description?: string | null;
  status?: TaskStatus;
  assignee_type?: TaskAssigneeType | null;
  assignee_id?: string | null;
  parent_issue_id?: string | null;
  project_id?: string | null;
  start_date?: string | null;
  due_date?: string | null;
}

export interface UpdateTaskRequest {
  title?: string;
  description?: string | null;
  status?: TaskStatus;
  assignee_type?: TaskAssigneeType | null;
  assignee_id?: string | null;
  position?: number;
  start_date?: string | null;
  due_date?: string | null;
  parent_issue_id?: string | null;
  project_id?: string | null;
}
