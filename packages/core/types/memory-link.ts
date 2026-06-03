// memory_artifact_link wire types — the many-to-many graph layer over
// memory_artifact. See server/migrations/118 for the schema rationale.

export type MemoryArtifactLinkTargetType =
  | "issue"
  | "project"
  | "agent"
  | "channel"
  | "memory_artifact";

// Semantic relation types. Keep stable; new ones append. The server
// validates against the same set so type and runtime stay in sync.
export type MemoryArtifactLinkRelationType =
  | "cites"
  | "supersedes"
  | "contradicts"
  | "implements"
  | "scope"
  | "discussed-in"
  | "informs";

export interface MemoryArtifactLink {
  id: string;
  workspace_id: string;
  artifact_id: string;
  target_type: MemoryArtifactLinkTargetType;
  target_id: string;
  relation_type: MemoryArtifactLinkRelationType;
  created_by_type: "member" | "agent";
  created_by_id: string;
  created_at: string;
}

export interface CreateMemoryArtifactLinkRequest {
  target_type: MemoryArtifactLinkTargetType;
  // Accepts either a UUID or an identifier ("ROA-427") for issue
  // targets — mirrors what the primary anchor accepts.
  target_id: string;
  relation_type: MemoryArtifactLinkRelationType;
}

export interface ListMemoryArtifactLinksResponse {
  links: MemoryArtifactLink[];
}
