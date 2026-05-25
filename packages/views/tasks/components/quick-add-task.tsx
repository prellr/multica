"use client";

import { useRef, useState, type KeyboardEvent, type FormEvent } from "react";
import { Plus } from "lucide-react";
import { toast } from "sonner";
import { useCreateTask } from "@multica/core/tasks";
import { ApiError } from "@multica/core/api";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../../i18n";

export interface QuickAddTaskProps {
  /** When set, every task created from this input gets `parent_issue_id = parentIssueId`.
   *  Used by the sidebar's children list to create subtasks under the
   *  open task. Omit for the top-level list. */
  parentIssueId?: string;
  /** Overrides the default placeholder. The children-list mount uses a
   *  shorter "Add subtask" prompt; the top-level list uses the full
   *  "Add a task and press Enter" copy. */
  placeholder?: string;
  /** Surface size. Inherits a denser padding when nested inside a
   *  child-list under the sidebar. */
  size?: "default" | "compact";
}

/**
 * Inline-create row. Used as the anchor at the top of the task list AND
 * as the "Add subtask" affordance inside the children list on the detail
 * sidebar. Both surfaces want the same UX (type, Enter, optimistic
 * insert) but slightly different parent-binding and visual weight.
 *
 * Design notes:
 *  - Empty submissions are no-ops (no toast, no flash). Backend would
 *    reject them with 400, but failing client-side avoids the round trip
 *    and keeps the surface frictionless.
 *  - Enter submits; Escape clears + blurs (lets the user dismiss without
 *    submitting). Composition (IME) keystrokes are ignored so Chinese /
 *    Japanese / Korean input doesn't pre-submit while a character is
 *    being composed.
 *  - Mutation errors are surfaced via toast — the optimistic row rolls
 *    back automatically (see useCreateTask onError) so the input can be
 *    repopulated for retry without losing context. Title is restored so
 *    the user doesn't lose what they typed.
 */
export function QuickAddTask({ parentIssueId, placeholder, size = "default" }: QuickAddTaskProps = {}) {
  const { t } = useT("tasks");
  const inputRef = useRef<HTMLInputElement>(null);
  const [value, setValue] = useState("");
  const [isComposing, setIsComposing] = useState(false);
  const createTask = useCreateTask();

  const submit = () => {
    const title = value.trim();
    if (!title) return;
    const previous = value;
    setValue("");
    createTask.mutate(
      // Spread parent_issue_id only when set so the request body stays
      // minimal in the top-level case. Backend treats missing as null.
      parentIssueId ? { title, parent_issue_id: parentIssueId } : { title },
      {
        onError: (err) => {
          // Restore so the user can retry without losing context.
          setValue(previous);
          inputRef.current?.focus();
          // Surface the server's actual error message when available
          // (ApiError.message carries the parsed response body's `error`
          // field). Without this the user sees only the generic toast
          // and we have no way to diagnose what the backend rejected.
          // Falls back to the generic message for network failures and
          // anything else that isn't an ApiError.
          const message =
            err instanceof ApiError && err.message
              ? err.message
              : t(($) => $.errors.create_failed);
          toast.error(message);
          // Also log the full error so a quick check in dev tools tells
          // us the status + response body. Cheap; only fires on failure.
          // eslint-disable-next-line no-console
          console.error("[tasks] create failed", err);
        },
      },
    );
  };

  const onKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === "Escape") {
      e.preventDefault();
      setValue("");
      inputRef.current?.blur();
      return;
    }
    if (e.key === "Enter" && !isComposing && !e.shiftKey) {
      e.preventDefault();
      submit();
    }
  };

  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    submit();
  };

  const isCompact = size === "compact";

  return (
    <form
      onSubmit={onSubmit}
      className={cn(
        "flex items-center gap-3 border-b bg-background",
        // Compact mode trims vertical padding so the input nestles into
        // the children-list section under the sidebar without dominating
        // the visual weight of the rows below.
        isCompact ? "px-3 py-1.5 text-xs" : "px-4 py-2.5",
      )}
    >
      <Plus
        className={cn("shrink-0 text-muted-foreground", isCompact ? "h-3.5 w-3.5" : "h-4 w-4")}
        aria-hidden
      />
      <input
        ref={inputRef}
        type="text"
        value={value}
        onChange={(e) => setValue(e.target.value)}
        onKeyDown={onKeyDown}
        onCompositionStart={() => setIsComposing(true)}
        onCompositionEnd={() => setIsComposing(false)}
        placeholder={placeholder ?? t(($) => $.quick_add.placeholder)}
        aria-label={t(($) => $.quick_add.submit)}
        className={cn(
          "flex-1 bg-transparent placeholder:text-muted-foreground focus:outline-none",
          isCompact ? "text-xs" : "text-sm",
        )}
      />
    </form>
  );
}
