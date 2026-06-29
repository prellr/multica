import { Extension } from "@tiptap/core";

/**
 * Drafts-local editor keymap: Cmd/Ctrl+Enter fires the draft-turn Send action
 * from *inside* the editor.
 *
 * Why a dedicated extension (not the shared submitShortcut): the drafts editor's
 * StarterKit ships `HardBreak`, which binds `Mod-Enter` to insert a line break.
 * If we listened at the window level instead, Cmd+Enter would BOTH insert a
 * break AND send. We intercept at the editor level and return `true` so the
 * event is consumed before HardBreak ever sees it.
 *
 * Priority: set ABOVE the default (100) so this extension's keymap plugin is
 * ordered before HardBreak's in the ProseMirror plugin stack — ProseMirror runs
 * keymap plugins in order and stops at the first handler that returns true, so a
 * higher priority guarantees Send wins the `Mod-Enter` race over the hard break.
 *
 * Both `Mod-Enter` (Cmd on Mac, Ctrl elsewhere) and `Ctrl-Enter` are bound so a
 * Mac user pressing Ctrl+Enter (which `Mod` does NOT cover on Mac) also sends.
 *
 * `onSend` decides whether to act: it returns the in-flight guard's result via
 * the caller, but the binding always returns `true` so a hard break is never
 * inserted on Cmd/Ctrl+Enter even while a turn is pending.
 */
export function createDraftSendShortcutExtension(onSend: () => void) {
  return Extension.create({
    name: "draftSendShortcut",
    priority: 1000,
    addKeyboardShortcuts() {
      const send = () => {
        onSend();
        // Always consume: even if onSend no-ops (a turn is already in flight),
        // we must not fall through to HardBreak's Mod-Enter and insert a break.
        return true;
      };
      return {
        "Mod-Enter": send,
        "Ctrl-Enter": send,
      };
    },
  });
}
