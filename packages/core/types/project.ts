export type ProjectStatus = "planned" | "in_progress" | "paused" | "completed" | "cancelled";

export type ProjectPriority = "urgent" | "high" | "medium" | "low" | "none";

/**
 * Ship Hub pipeline topology. Determines which stages a release for this
 * project passes through:
 *   `staged`         → merging → in_staging → verifying → promoting → in_production → done
 *   `direct_to_prod` → merging → promoting → in_production → done
 * See migration 095 + completeMergeTrain in server/internal/service/ship/.
 *
 * PR5a/b of the Ship Hub rebuild: pipeline_kind is being superseded by
 * `pipeline_config` (a structured per-project shape covering all 5 real
 * pipeline shapes — direct_to_prod, staged_strict, manual_only, library,
 * manual_compose). Existing consumers reading pipeline_kind continue to
 * work; new consumers should read pipeline_config.
 */
export type ProjectPipelineKind = "staged" | "direct_to_prod";

/**
 * PR5a phase 1 of the Ship Hub rebuild — structured per-project
 * pipeline shape. Mirrors the Go types in
 * server/internal/service/ship/pipeline_config.go. The backend always
 * populates this field via the read shim, even when the underlying
 * JSONB column is NULL (it synthesizes a default from pipeline_kind),
 * so clients can render the kanban from pipeline_config unconditionally.
 */
export type PipelineShape =
  | "direct_to_prod"
  | "staged_strict"
  | "manual_only"
  | "library"
  | "manual_compose";

export type PipelineTriggerKind =
  | "push_branch"
  | "workflow_run"
  | "workflow_dispatch"
  | "deployment_status"
  | "manual_ack"
  | "image_publish_tag";

export interface PipelineTriggerConfig {
  branch?: string;
  workflow?: string;
  parent_workflow?: string;
  environment?: string;
  tag_pattern?: string;
}

export interface PipelineTrigger {
  kind: PipelineTriggerKind;
  config?: PipelineTriggerConfig;
}

export interface PipelineStage {
  /** Stable identifier within a project. Lowercase + snake_case. */
  id: string;
  /** Human-readable display name. Used as the kanban column header
   *  when no locale key matches the id. */
  name: string;
  /** 0-indexed kanban column position. Stages render left-to-right
   *  in ascending position order. */
  position: number;
  /** Once a release reaches this stage, the kanban renders it in the
   *  archive column and IsTerminalDerivedStage returns true. Exactly
   *  one stage per pipeline is terminal. */
  is_terminal?: boolean;
  /** Surface a "Mark verified / deployed" button instead of waiting
   *  for automation. Used by stages whose trigger is manual_ack and
   *  by gates that need operator sign-off. */
  requires_human_ack?: boolean;
  /** When the stage is tied to a tracked deploy_environment row.
   *  Empty when not. */
  deploy_environment_id?: string;
  /** ANY of these firing advances a release into this stage. Empty
   *  slice means "only reachable by explicit advance" (e.g. a
   *  terminal stage advanced only via manual_ack). */
  triggers?: PipelineTrigger[];
}

export interface PipelineConfig {
  /** Canonical name of the underlying pipeline pattern — the kanban
   *  can specialize copy or chip behavior by shape without re-walking
   *  the stage list. */
  shape: PipelineShape | string;
  /** Ordered list of kanban columns. Always contains at least one
   *  terminal stage. */
  stages: PipelineStage[];
}

export interface Project {
  id: string;
  workspace_id: string;
  title: string;
  description: string | null;
  icon: string | null;
  status: ProjectStatus;
  priority: ProjectPriority;
  lead_type: "member" | "agent" | null;
  lead_id: string | null;
  /**
   * Soft-delete marker. Non-null means the project is archived: it stays
   * in the DB and keeps its issue + resource references, but is hidden
   * from the default projects list. Restored by clearing back to null.
   */
  archived_at: string | null;
  archived_by: string | null;
  created_at: string;
  updated_at: string;
  issue_count: number;
  done_count: number;
  resource_count: number;
  pipeline_kind: ProjectPipelineKind;
  /** PR5a — structured per-project pipeline. The backend always
   *  populates this (synthesizing from pipeline_kind when the JSONB
   *  column is NULL) so consumers can render unconditionally. */
  pipeline_config: PipelineConfig;
}

export interface CreateProjectRequest {
  title: string;
  description?: string;
  icon?: string;
  status?: ProjectStatus;
  priority?: ProjectPriority;
  lead_type?: "member" | "agent";
  lead_id?: string;
  // Resources to attach in the same transaction as the project. Server returns
  // 4xx (and rolls back) if any one is invalid or duplicate.
  resources?: CreateProjectResourceRequest[];
}

export interface UpdateProjectRequest {
  title?: string;
  description?: string | null;
  icon?: string | null;
  status?: ProjectStatus;
  priority?: ProjectPriority;
  lead_type?: "member" | "agent" | null;
  lead_id?: string | null;
  pipeline_kind?: ProjectPipelineKind;
}

export interface ListProjectsResponse {
  projects: Project[];
  total: number;
}

// ProjectResource is a typed pointer from a project to an external resource.
// The resource_ref shape depends on resource_type (e.g. github_repo carries
// { url, default_branch_hint? }). New types add a case in
// validateAndNormalizeResourceRef on the server and a renderer in the UI;
// no schema or type changes required.
export type ProjectResourceType = "github_repo";

export interface GithubRepoResourceRef {
  url: string;
  default_branch_hint?: string;
}

export interface ProjectResource {
  id: string;
  project_id: string;
  workspace_id: string;
  resource_type: ProjectResourceType;
  resource_ref: GithubRepoResourceRef | Record<string, unknown>;
  label: string | null;
  position: number;
  created_at: string;
  created_by: string | null;
}

export interface CreateProjectResourceRequest {
  resource_type: ProjectResourceType;
  resource_ref: GithubRepoResourceRef | Record<string, unknown>;
  label?: string;
  position?: number;
}

export interface ListProjectResourcesResponse {
  resources: ProjectResource[];
  total: number;
}
