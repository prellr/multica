import { Editor } from "@tiptap/core";
import * as Y from "yjs";
import { prosemirrorToYXmlFragment } from "@tiptap/y-tiptap";
import { DRAFT_YJS_FRAGMENT } from "@multica/core/drafts/collab";
import { createEditorExtensions, type EditorExtensionsOptions } from "../../editor/extensions";
import { preprocessMarkdown } from "../../editor/utils/preprocess";

/**
 * Seed a draft `Y.Doc` from a markdown body — ONCE, before the collaborative
 * editor mounts (Drafts slice 3a).
 *
 * The single-seed rule: under Collaboration the live editor mounts EMPTY and
 * hydrates from the Y.Doc's fragment. So the body must be written into the CRDT
 * exactly once, here — never also passed as the editor's `content`, which would
 * double-apply and corrupt the document. Seeding is idempotent-guarded: if the
 * fragment already has content (e.g. a reconnect after the backend persisted
 * updates), we no-op so we don't stack a second copy.
 *
 * How it works: markdown → ProseMirror is schema-dependent, so we spin up a
 * transient HEADLESS editor with the SAME extension set the live editor uses
 * (minus Collaboration itself — seeding must not bind a second sync plugin),
 * parse the markdown into its doc, then project that doc into the Y fragment via
 * y-tiptap's `prosemirrorToYXmlFragment`. The transient editor is destroyed
 * immediately; only the populated Y.Doc survives.
 *
 * The extension set MUST match the live editor's (same schema) or the fragment
 * the live editor reads back will contain nodes its schema rejects.
 */
export function seedYDocFromMarkdown(
  doc: Y.Doc,
  body: string,
  options: EditorExtensionsOptions,
  field: string = DRAFT_YJS_FRAGMENT,
): void {
  const fragment = doc.getXmlFragment(field);
  // Already seeded (or remotely hydrated) — never double-apply.
  if (fragment.length > 0) return;

  // Build the markdown→PM doc with a transient editor using the SAME extensions
  // as the live editor, but WITHOUT collaboration (we are the seed source).
  const { collaboration: _collab, ...seedOptions } = options;
  const seedEditor = new Editor({
    extensions: createEditorExtensions(seedOptions),
    content: body ? preprocessMarkdown(body) : "",
    contentType: body ? "markdown" : undefined,
  });

  try {
    prosemirrorToYXmlFragment(seedEditor.state.doc, fragment);
  } finally {
    seedEditor.destroy();
  }
}
