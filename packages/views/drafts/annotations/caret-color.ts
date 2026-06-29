/**
 * Deterministic caret color for a co-editing peer (Drafts slice 3a).
 *
 * CollaborationCaret renders each peer's cursor in the color carried on their
 * awareness state. Deriving the color from a stable id means the same user keeps
 * the same color across sessions and tabs without any server-side assignment.
 * The palette is a small set of distinct, legible hues (semantic-token-adjacent
 * values are not used here because the caret color must be a concrete hex the
 * CollaborationCaret extension writes inline).
 */
const CARET_PALETTE = [
  "#ef4444", // red
  "#f97316", // orange
  "#eab308", // amber
  "#22c55e", // green
  "#06b6d4", // cyan
  "#3b82f6", // blue
  "#8b5cf6", // violet
  "#ec4899", // pink
];

export function caretColorForId(id: string): string {
  let hash = 0;
  for (let i = 0; i < id.length; i++) {
    hash = (hash * 31 + id.charCodeAt(i)) | 0;
  }
  const idx = Math.abs(hash) % CARET_PALETTE.length;
  // Non-empty palette guarantees a hit; the fallback satisfies the indexed
  // access checker without ever being reached.
  return CARET_PALETTE[idx] ?? CARET_PALETTE[0]!;
}
