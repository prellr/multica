export { draftKeys, draftListOptions, draftDetailOptions } from "./queries";
export {
  useCreateDraft,
  useUpdateDraft,
  useDeleteDraft,
  isTempDraftId,
} from "./mutations";
export {
  prependDraftToLists,
  replaceDraftInLists,
  removeDraftFromLists,
  swapTempDraft,
} from "./cache-helpers";
export { onDraftCreated, onDraftUpdated, onDraftDeleted } from "./ws-updaters";

// Annotation layer (slice 1).
export {
  reanchor,
  similarity,
  type Anchor,
  type ReanchorResult,
  EXACT_POS_TOLERANCE,
  CONTEXT_WINDOW,
  SHIFTED_SIMILARITY_THRESHOLD,
  CHANGED_SIMILARITY_THRESHOLD,
} from "./reanchor";
export { draftAnnotationKeys, draftAnnotationListOptions } from "./annotation-queries";
export {
  useCreateDraftAnnotation,
  useUpdateDraftAnnotation,
  useDeleteDraftAnnotation,
  useAddDraftAnnotationMessage,
  isTempAnnotationId,
  isTempMessageId,
} from "./annotation-mutations";
export {
  onDraftAnnotationCreated,
  onDraftAnnotationUpdated,
  onDraftAnnotationDeleted,
  onDraftAnnotationMessageCreated,
} from "./annotation-ws-updaters";

// Conversation rail (Rail-1) — the draft-level, un-anchored chat surface,
// distinct from the anchored annotation threads above.
export { draftMessageKeys, draftMessageListOptions } from "./message-queries";
export { useAddDraftMessage, isTempDraftMessageId } from "./message-mutations";
export { onDraftMessageCreated } from "./message-ws-updaters";

// Send-turn (slice 2).
export { useStartDraftTurn, useDraftTurnMessages } from "./turn-mutations";
