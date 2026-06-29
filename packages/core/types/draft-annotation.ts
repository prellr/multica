/**
 * Draft annotation layer (Drafts slice 1). A non-destructive overlay on a
 * draft's markdown body: the user selects a span of text and attaches a typed,
 * anchored, threaded annotation. The body stays clean markdown — the
 * highlight/pin renders from the persisted anchor, never inlined into the doc.
 * Anchors re-anchor to their text as the body is edited (see drafts/reanchor.ts);
 * when the range moves, the new quote/context/pos_hint is PATCHed back.
 *
 * Slice 1 is HUMAN-ONLY (every annotation/message is author_type "user"), but
 * `author_type` + `author_user_id` are modeled now so slice 2 (agent
 * authoring/replies) is a drop-in with no type change. See server migration 123.
 */

/**
 * Who authored an annotation or message. Open enum: "user" in slice 1, "agent"
 * in slice 2; modeled as a wide union with a fallback member so consumers can
 * `switch` with a default branch and an unknown server value renders generically
 * (enum-drift rule).
 */
export type DraftAnnotationAuthorType = "user" | "agent" | (string & {});

/**
 * The kind of annotation. Open enum (no DB CHECK; Go switch + default). A bare
 * "highlight" has no thread messages; the others open with an initial comment.
 */
export type DraftAnnotationType =
  | "comment"
  // A 'question' is Aye's primary inquisitive verb (slice 2): an anchored
  // "I'm asking you something" thread, distinct from a comment so it can be
  // filtered/styled separately. Persists as 'question' on the server.
  | "question"
  | "suggestion"
  | "approve"
  | "block"
  | "highlight"
  | (string & {});

/**
 * Lifecycle state of an annotation thread. Open enum (default branch downgrades
 * unknown values).
 */
export type DraftAnnotationState =
  | "open"
  | "acknowledged"
  | "resolved"
  | "dismissed"
  | (string & {});

/** One message in an annotation's thread. message[0] is the initial comment. */
export interface DraftAnnotationMessage {
  id: string;
  annotation_id: string;
  author_type: DraftAnnotationAuthorType;
  /** Empty string when author_type !== "user" (e.g. an agent author). */
  author_user_id: string;
  body: string;
  created_at: string;
}

/**
 * An annotation: the anchor + type + state, plus its thread. The anchor fields
 * (quote/context_before/context_after/pos_hint) are what the re-anchoring engine
 * consumes to relocate the span in an edited body.
 */
export interface DraftAnnotation {
  id: string;
  draft_id: string;
  workspace_id: string;
  author_type: DraftAnnotationAuthorType;
  /** Empty string when author_type !== "user". */
  author_user_id: string;
  type: DraftAnnotationType;
  quote: string;
  context_before: string;
  context_after: string;
  pos_hint: number;
  state: DraftAnnotationState;
  /** Suggestion payload (type "suggestion"); null otherwise. */
  suggestion_before: string | null;
  suggestion_after: string | null;
  created_at: string;
  updated_at: string;
  /** The thread. message[0] is the initial comment; a bare highlight is []. */
  messages: DraftAnnotationMessage[];
}

export interface ListDraftAnnotationsResponse {
  annotations: DraftAnnotation[];
  total: number;
}

/**
 * Create an annotation. `type` + anchor describe the span; `message` is the
 * optional initial comment (omitted for a bare highlight). State is server-set
 * to "open" at create time.
 */
export interface CreateDraftAnnotationRequest {
  type?: DraftAnnotationType;
  quote?: string;
  context_before?: string;
  context_after?: string;
  pos_hint?: number;
  suggestion_before?: string | null;
  suggestion_after?: string | null;
  message?: string;
}

/**
 * Patch an annotation. Two distinct shapes:
 *   - state transition: { state }
 *   - re-anchor persistence: { quote, context_before, context_after, pos_hint }
 * A missing field is "no change" (server uses COALESCE).
 */
export interface UpdateDraftAnnotationRequest {
  state?: DraftAnnotationState;
  quote?: string;
  context_before?: string;
  context_after?: string;
  pos_hint?: number;
}

/** Append a reply to an annotation thread. */
export interface AddDraftAnnotationMessageRequest {
  body: string;
}
