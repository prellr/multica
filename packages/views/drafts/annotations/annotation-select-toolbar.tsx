"use client";

import { useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import type { Editor } from "@tiptap/core";
import { posToDOMRect } from "@tiptap/core";
import { computePosition, flip, offset, shift } from "@floating-ui/dom";
import { MessageSquare, Lightbulb, Check, Ban, Highlighter } from "lucide-react";
import type { DraftAnnotationType } from "@multica/core/types";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../../i18n";

/**
 * Floating toolbar shown when the user selects text in the draft body. Picking a
 * type (or pressing its shortcut) creates an annotation anchored to the
 * selection. Keys: c=comment, s=suggest, b=block, a=approve, h=highlight.
 *
 * Portaled to body and positioned with Floating UI against the live selection
 * rect (mirrors the editor's EditorBubbleMenu approach) so it escapes overflow
 * containers. The formatting bubble menu is disabled on this editor — the
 * annotation toolbar is the only selection affordance.
 */

type LabelKey = "comment" | "suggest" | "approve" | "block" | "highlight";

interface ToolbarItem {
  type: DraftAnnotationType;
  key: string;
  icon: typeof MessageSquare;
  labelKey: LabelKey;
}

const ITEMS: ToolbarItem[] = [
  { type: "comment", key: "c", icon: MessageSquare, labelKey: "comment" },
  { type: "suggestion", key: "s", icon: Lightbulb, labelKey: "suggest" },
  { type: "block", key: "b", icon: Ban, labelKey: "block" },
  { type: "approve", key: "a", icon: Check, labelKey: "approve" },
  { type: "highlight", key: "h", icon: Highlighter, labelKey: "highlight" },
];

interface AnnotationSelectToolbarProps {
  editor: Editor;
  /** Called with the chosen type when the user picks one for the live selection. */
  onPick: (type: DraftAnnotationType) => void;
}

export function AnnotationSelectToolbar({ editor, onPick }: AnnotationSelectToolbarProps) {
  const { t } = useT("drafts");
  // Bound to the `drafts` namespace, so the selector API resolves the new keys.
  const labelFor = (key: LabelKey): string => {
    switch (key) {
      case "comment":
        return t(($) => $.annotations.types.comment);
      case "suggest":
        return t(($) => $.annotations.types.suggest);
      case "approve":
        return t(($) => $.annotations.types.approve);
      case "block":
        return t(($) => $.annotations.types.block);
      case "highlight":
        return t(($) => $.annotations.types.highlight);
    }
  };
  const [visible, setVisible] = useState(false);
  const elRef = useRef<HTMLDivElement>(null);
  // Keep the latest onPick without re-binding the keydown listener.
  const onPickRef = useRef(onPick);
  onPickRef.current = onPick;

  // Track whether the editor currently has a non-empty text selection.
  useEffect(() => {
    const update = () => {
      const { empty, from, to } = editor.state.selection;
      const hasText = !empty && to > from;
      setVisible(hasText);
    };
    editor.on("selectionUpdate", update);
    editor.on("transaction", update);
    update();
    return () => {
      editor.off("selectionUpdate", update);
      editor.off("transaction", update);
    };
  }, [editor]);

  // Position against the selection rect.
  useEffect(() => {
    if (!visible || !elRef.current) return;
    const el = elRef.current;
    const { from, to } = editor.state.selection;
    const virtual = {
      getBoundingClientRect: () => posToDOMRect(editor.view, from, to),
    };
    computePosition(virtual, el, {
      placement: "top",
      middleware: [offset(8), flip(), shift({ padding: 8 })],
    }).then(({ x, y }) => {
      el.style.left = `${x}px`;
      el.style.top = `${y}px`;
    });
  }, [visible, editor]);

  // Keyboard shortcuts while a selection is active.
  useEffect(() => {
    if (!visible) return;
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.metaKey || e.ctrlKey || e.altKey) return;
      const item = ITEMS.find((i) => i.key === e.key.toLowerCase());
      if (!item) return;
      e.preventDefault();
      onPickRef.current(item.type);
    };
    document.addEventListener("keydown", onKeyDown, true);
    return () => document.removeEventListener("keydown", onKeyDown, true);
  }, [visible]);

  if (typeof document === "undefined") return null;

  return createPortal(
    <div
      ref={elRef}
      role="toolbar"
      aria-label={t(($) => $.annotations.toolbar_label)}
      className={cn(
        "absolute z-50 flex items-center gap-0.5 rounded-md border bg-popover p-1 shadow-md",
        visible ? "visible" : "invisible",
      )}
      style={{ top: 0, left: 0 }}
      // Don't steal focus from the editor (keeps the selection alive).
      onMouseDown={(e) => e.preventDefault()}
    >
      {ITEMS.map((item) => {
        const Icon = item.icon;
        const label = labelFor(item.labelKey);
        return (
          <button
            key={item.type}
            type="button"
            onClick={() => onPick(item.type)}
            className="flex items-center gap-1 rounded px-2 py-1 text-xs hover:bg-accent"
            title={`${label} (${item.key})`}
          >
            <Icon className="h-3.5 w-3.5" aria-hidden />
            <span>{label}</span>
          </button>
        );
      })}
    </div>,
    document.body,
  );
}
