"use client";

import { useRef, useState, type KeyboardEvent, type FormEvent } from "react";
import { Plus } from "lucide-react";
import { toast } from "sonner";
import { useCreateTask } from "@multica/core/tasks";
import { ApiError } from "@multica/core/api";
import { useT } from "../../i18n";

/**
 * Inline-create row anchored at the top of the task list. The point of
 * this surface is speed — type a title, hit Enter, the row appears
 * immediately (optimistic), the input clears, focus stays on the input
 * for the next entry. No modal, no field-by-field form.
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
export function QuickAddTask() {
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
      { title },
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

  return (
    <form
      onSubmit={onSubmit}
      className="flex items-center gap-3 border-b bg-background px-4 py-2.5"
    >
      <Plus className="h-4 w-4 shrink-0 text-muted-foreground" aria-hidden />
      <input
        ref={inputRef}
        type="text"
        value={value}
        onChange={(e) => setValue(e.target.value)}
        onKeyDown={onKeyDown}
        onCompositionStart={() => setIsComposing(true)}
        onCompositionEnd={() => setIsComposing(false)}
        placeholder={t(($) => $.quick_add.placeholder)}
        aria-label={t(($) => $.quick_add.submit)}
        className="flex-1 bg-transparent text-sm placeholder:text-muted-foreground focus:outline-none"
      />
    </form>
  );
}
