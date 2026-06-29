// Headless Yjs co-editing floor for Drafts (slice 3a).
export {
  DRAFT_YJS_FRAGMENT,
  createDraftYContext,
  destroyDraftYContext,
  connectDocs,
  type DraftYContext,
  type DocRelay,
} from "./y-doc";

export {
  DraftYjsProvider,
  REMOTE_ORIGIN,
  type DraftYjsProviderOptions,
} from "./provider";
export { draftYjsWsUrl } from "./url";
export { useDraftYjs } from "./use-draft-yjs";
