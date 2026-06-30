/**
 * Draft conversation rail (Drafts "global germination rail", Rail-1). A
 * draft-level, UN-anchored conversation surface — a flat per-draft message log,
 * distinct from the anchored annotation threads (see ./draft-annotation.ts).
 * Where an annotation message is scoped to a span via a parent annotation, a
 * DraftMessage hangs directly off the draft: the document-wide back-and-forth,
 * not a margin note.
 *
 * Rail-1 is HUMAN-ONLY (every message is author_type "user"), but `author_type`
 * + `author_user_id` are modeled now so Rail-2 (Aye authoring via `multica
 * draft say`) is a drop-in with no type change. See server migration 126.
 */

import type { DraftAnnotationAuthorType } from "./draft-annotation";

/**
 * One message on a draft's conversation rail. `author_type` reuses the
 * annotation surface's open author-type union ("user" | "agent" | unknown) so
 * an unknown server value renders generically (enum-drift rule).
 */
export interface DraftMessage {
  id: string;
  draft_id: string;
  workspace_id: string;
  author_type: DraftAnnotationAuthorType;
  /** Empty string when author_type !== "user" (e.g. an agent author). */
  author_user_id: string;
  body: string;
  created_at: string;
}

export interface ListDraftMessagesResponse {
  messages: DraftMessage[];
  total: number;
}

/** Post a message to a draft's conversation rail. */
export interface AddDraftMessageRequest {
  body: string;
}
