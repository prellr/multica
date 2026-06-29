/**
 * Drafts are a standalone, workspace- + owner-scoped entity: a living markdown
 * document a user co-authors with an agent. Slice 0 models only the single-user
 * CRUD surface (title + markdown body + status); later slices add the
 * annotation layer, the agent, and co-editing — the `owner_user_id` field and
 * the open `status` enum are the forward-compat seams for that work. See server
 * migration 122 and server draft.sql for the query-layer scoping.
 */

/**
 * Open enum. Slice 0 only ever produces "draft"; future values (e.g.
 * "accepted") arrive without a frontend change. Modeled as a wide string union
 * with an explicit fallback member so consumers can `switch` with a default
 * branch and unknown server values render a generic fallback (enum-drift rule).
 */
export type DraftStatus = "draft" | "accepted" | (string & {});

export interface Draft {
  id: string;
  workspace_id: string;
  /** The user who owns this draft. Named *owner* (not a single-user flag) so
   *  the multi-author surface in a later slice doesn't need a rename. */
  owner_user_id: string;
  title: string;
  /** Markdown body. Defaults to "" on the server. */
  body: string;
  status: DraftStatus;
  created_at: string;
  updated_at: string;
}

export interface ListDraftsParams {
  workspace_id?: string;
}

export interface ListDraftsResponse {
  drafts: Draft[];
  total: number;
}

/** All fields optional — a blank "New draft" create is a valid empty body. */
export interface CreateDraftRequest {
  title?: string;
  body?: string;
  status?: DraftStatus;
}

/** A missing field is "no change" (server uses COALESCE), so a body-only
 *  autosave PATCH leaves title and status untouched. */
export interface UpdateDraftRequest {
  title?: string;
  body?: string;
  status?: DraftStatus;
}

/**
 * Response from POST /api/drafts/{id}/turn (slice 2 — the Send-turn). The
 * server enqueues a draft turn for Aye and echoes the task so the view can
 * subscribe to its task:message stream and show the "Aye is working" indicator.
 * `status` is the queued task's status ("queued"); it's an open string for the
 * same enum-drift reasons as DraftStatus.
 */
export interface DraftTurnResponse {
  task_id: string;
  draft_id: string;
  agent_id: string;
  status: string;
}
