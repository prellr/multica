import { describe, it, expect, vi } from "vitest";
import { getExtensionField } from "@tiptap/core";
import type { Editor } from "@tiptap/core";
import { createDraftSendShortcutExtension } from "./send-shortcut";

function getShortcuts(
  ext: ReturnType<typeof createDraftSendShortcutExtension>,
): Record<string, () => boolean> {
  const fn = getExtensionField<() => Record<string, () => boolean>>(
    ext,
    "addKeyboardShortcuts",
    {
      name: "draftSendShortcut",
      options: {},
      storage: {},
      editor: {} as Editor,
      type: null,
    },
  );
  return fn?.() ?? {};
}

describe("createDraftSendShortcutExtension", () => {
  it("binds both Mod-Enter and Ctrl-Enter to the send action", () => {
    const onSend = vi.fn();
    const shortcuts = getShortcuts(createDraftSendShortcutExtension(onSend));
    expect(shortcuts["Mod-Enter"]).toBeDefined();
    expect(shortcuts["Ctrl-Enter"]).toBeDefined();
  });

  it("fires onSend and returns true (consumes the event) so no hard break is inserted", () => {
    const onSend = vi.fn();
    const shortcuts = getShortcuts(createDraftSendShortcutExtension(onSend));

    // Returning true is the contract that stops ProseMirror from falling
    // through to HardBreak's Mod-Enter binding (which would insert a line
    // break). Both bindings must consume.
    expect(shortcuts["Mod-Enter"]!()).toBe(true);
    expect(shortcuts["Ctrl-Enter"]!()).toBe(true);
    expect(onSend).toHaveBeenCalledTimes(2);
  });

  it("still consumes the event even when onSend no-ops (turn already in flight)", () => {
    // The parent's in-flight guard makes onSend a no-op while a turn is
    // pending. Cmd+Enter must STILL be consumed so it never inserts a break.
    const onSend = vi.fn(() => {
      /* guarded no-op */
    });
    const shortcuts = getShortcuts(createDraftSendShortcutExtension(onSend));
    expect(shortcuts["Mod-Enter"]!()).toBe(true);
  });

  it("runs at a priority above HardBreak's default so its keymap wins", () => {
    const ext = createDraftSendShortcutExtension(vi.fn());
    // Priority > 100 (Tiptap's default) orders this extension's keymap plugin
    // before HardBreak's, so it gets the Mod-Enter keydown first.
    expect((ext.config as { priority?: number }).priority).toBeGreaterThan(100);
  });
});
