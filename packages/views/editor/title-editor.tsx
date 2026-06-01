"use client";

import { forwardRef, useEffect, useImperativeHandle, useRef } from "react";
import { useEditor, EditorContent } from "@tiptap/react";
import { Extension } from "@tiptap/core";
import { Document } from "@tiptap/extension-document";
import { Paragraph } from "@tiptap/extension-paragraph";
import { Text } from "@tiptap/extension-text";
import Placeholder from "@tiptap/extension-placeholder";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../i18n";
import "./title-editor.css";

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

interface TitleEditorProps {
  defaultValue?: string;
  placeholder?: string;
  className?: string;
  autoFocus?: boolean;
  onSubmit?: () => void;
  onBlur?: (value: string) => void;
  onChange?: (value: string) => void;
}

interface TitleEditorRef {
  getText: () => string;
  focus: () => void;
}

// ---------------------------------------------------------------------------
// Single-paragraph document — prevents Enter from creating new lines
// ---------------------------------------------------------------------------

const SingleLineDocument = Document.extend({
  content: "paragraph",
});

// ---------------------------------------------------------------------------
// Keyboard shortcuts: Enter → submit, Escape → blur
// ---------------------------------------------------------------------------

function createTitleKeymap(opts: {
  onSubmitRef: React.RefObject<(() => void) | undefined>;
}) {
  return Extension.create({
    name: "titleKeymap",
    addKeyboardShortcuts() {
      return {
        Enter: ({ editor }) => {
          opts.onSubmitRef.current?.();
          editor.commands.blur();
          return true;
        },
        "Shift-Enter": () => true, // swallow — no line breaks
        Escape: ({ editor }) => {
          editor.commands.blur();
          return true;
        },
      };
    },
  });
}

// ---------------------------------------------------------------------------
// Component
// ---------------------------------------------------------------------------

const TitleEditor = forwardRef<TitleEditorRef, TitleEditorProps>(
  function TitleEditor(
    {
      defaultValue = "",
      placeholder: placeholderText = "",
      className,
      autoFocus = false,
      onSubmit,
      onBlur,
      onChange,
    },
    ref,
  ) {
    const { t } = useT("editor");
    const onSubmitRef = useRef(onSubmit);
    const onBlurRef = useRef(onBlur);
    const onChangeRef = useRef(onChange);

    onSubmitRef.current = onSubmit;
    onBlurRef.current = onBlur;
    onChangeRef.current = onChange;

    const editor = useEditor({
      immediatelyRender: false,
      content: defaultValue
        ? { type: "doc", content: [{ type: "paragraph", content: [{ type: "text", text: defaultValue }] }] }
        : "",
      extensions: [
        SingleLineDocument,
        Paragraph,
        Text,
        Placeholder.configure({
          placeholder: placeholderText,
          showOnlyCurrent: false,
        }),
        createTitleKeymap({ onSubmitRef }),
      ],
      editorProps: {
        attributes: {
          class: cn("title-editor outline-none", className),
          role: "textbox",
          "aria-multiline": "false",
          "aria-label": placeholderText || t(($) => $.title_editor.title_aria_label),
        },
      },
      onUpdate: ({ editor: ed }) => {
        onChangeRef.current?.(ed.getText());
      },
      onBlur: ({ editor: ed }) => {
        onBlurRef.current?.(ed.getText());
      },
    });

    // Auto-focus after mount — delay to wait for Dialog open animation
    useEffect(() => {
      if (autoFocus && editor) {
        const timer = setTimeout(() => {
          editor.commands.focus("end");
        }, 50);
        return () => clearTimeout(timer);
      }
      return undefined;
    }, [autoFocus, editor]);

    // Hydrate a defaultValue that arrives AFTER mount. The editor is
    // uncontrolled — `content` in `useEditor` is read once at init — so a
    // parent that loads its data asynchronously (e.g. via React Query) and
    // sets `defaultValue` in a useEffect will mount the editor with the
    // initial empty string and never reflect the loaded value. Sync the
    // arrived value into the editor exactly once: when the editor's
    // current content is empty AND the new defaultValue is not. After
    // that, the editor is the source of truth — we don't clobber user
    // edits if a parent prop re-renders later.
    useEffect(() => {
      if (!editor || !defaultValue) return;
      if (editor.getText().length === 0) {
        editor.commands.setContent({
          type: "doc",
          content: [{ type: "paragraph", content: [{ type: "text", text: defaultValue }] }],
        });
      }
    }, [editor, defaultValue]);

    useImperativeHandle(ref, () => ({
      getText: () => editor?.getText() ?? "",
      focus: () => {
        editor?.commands.focus("end");
      },
    }));

    if (!editor) return null;

    return <EditorContent editor={editor} />;
  },
);

export { TitleEditor, type TitleEditorProps, type TitleEditorRef };
