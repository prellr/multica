// Wire types for the polymorphic memory_artifact substrate.
//
// Design rationale lives in server/migrations/068_memory_artifact.up.sql.
// One discriminator (`kind`) covers wiki pages, agent notes, runbooks,
// and decisions — each kind shares the same search/anchor/archive plumbing
// so the UI doesn't have to know about per-kind silos.

export type MemoryArtifactKind =
  | "wiki_page"
  | "agent_note"
  | "runbook"
  | "decision"
  // System / orchestrator-log kinds. Written by the squad runtime, not
  // hand-created. Hidden from the default memory list (see include_system on
  // ListMemoryArtifactsParams) so they don't bury curated knowledge.
  | "session"
  | "dispatch_event";

// Polymorphic anchor — what is this artifact about? Mirrors the
// allowedAnchorTypes set in the server handler. New anchor types must
// be registered on both sides.
export type MemoryArtifactAnchorType =
  | "issue"
  | "project"
  | "agent"
  | "channel";

export type MemoryArtifactAuthorType = "member" | "agent";

export interface MemoryArtifact {
  id: string;
  workspace_id: string;
  kind: MemoryArtifactKind;
  parent_id: string | null;
  title: string;
  content: string;
  slug: string | null;
  anchor_type: MemoryArtifactAnchorType | null;
  anchor_id: string | null;
  author_type: MemoryArtifactAuthorType;
  author_id: string;
  tags: string[];
  // Free-form JSON object — server returns "{}" when empty so callers
  // can index without null checks.
  metadata: Record<string, unknown>;
  archived_at: string | null;
  archived_by: string | null;
  created_at: string;
  updated_at: string;
  verified_at: string | null;
}

export interface CreateMemoryArtifactRequest {
  kind: MemoryArtifactKind;
  title: string;
  content: string;
  parent_id?: string | null;
  slug?: string | null;
  anchor_type?: MemoryArtifactAnchorType | null;
  anchor_id?: string | null;
  tags?: string[];
  metadata?: Record<string, unknown>;
}

// PATCH semantics — every field is independently optional. `null` for
// title/content is rejected by the server; `null` for slug/parent/anchor
// clears the value. Tags replaces the whole array when present.
export interface UpdateMemoryArtifactRequest {
  title?: string;
  content?: string;
  slug?: string | null;
  parent_id?: string | null;
  anchor_type?: MemoryArtifactAnchorType | null;
  anchor_id?: string | null;
  tags?: string[];
  metadata?: Record<string, unknown>;
}

export interface ListMemoryArtifactsParams {
  kind?: MemoryArtifactKind;
  parent_id?: string;
  // Anchor pivot — "everything about issue X". anchor_id accepts the issue
  // identifier form (e.g. "ROA-427") in addition to a UUID.
  anchor_type?: MemoryArtifactAnchorType;
  anchor_id?: string;
  // OR-semantics tag filter (a row matches if it has at least one tag).
  tags?: string[];
  // Surface system/log kinds (session, dispatch_event). Default false — they
  // stay hidden unless explicitly requested or filtered by kind.
  include_system?: boolean;
  include_archived?: boolean;
  // Narrows to verified_at IS NULL — powers the "Needs review" filter
  // for triage. Composes with every other filter.
  unverified_only?: boolean;
  limit?: number;
  offset?: number;
}

export interface ListMemoryArtifactsResponse {
  memory_artifacts: MemoryArtifact[];
  total: number;
}

export interface SearchMemoryArtifactsParams {
  q: string;
  kind?: MemoryArtifactKind;
  tags?: string[];
  include_system?: boolean;
  limit?: number;
  offset?: number;
}

// Tag-frequency entry for the filter bar's autocomplete.
export interface MemoryTag {
  tag: string;
  count: number;
}

export interface ListMemoryTagsResponse {
  tags: MemoryTag[];
}
