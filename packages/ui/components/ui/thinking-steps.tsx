"use client";

import { forwardRef, type HTMLAttributes, type ReactNode } from "react";
import { motion, AnimatePresence } from "motion/react";
import { cn } from "@multica/ui/lib/utils";

/**
 * ThinkingSteps — a streamed chain-of-thought / tool-step list, adapted from the
 * Fluid Functionalism `thinking-steps` component (MIT). FF's version pulls in a
 * large registry subsystem (its own accordion, badge, icon-context, shape-
 * context, springs, font-weight). We keep FF's visual identity — the sequential
 * reveal, the connector line down the icon column, the shimmer on the active
 * step — but make it a self-contained, data-driven component built on our own
 * primitives (`cn`, `motion/react`, the shared shimmer keyframe, semantic
 * tokens), so no foreign registry lands in packages/ui.
 *
 * It's driven by a `steps` array rather than composed children: the Drafts
 * narration rail maps the live `task:message` stream into steps, so the
 * component re-renders as new steps stream in. An unknown step `status` or a
 * missing label degrades gracefully (renders as a generic complete step / empty
 * label) — never throws on a drifted stream.
 */

export type ThinkingStepStatus = "complete" | "active" | "pending";

export interface ThinkingStepItem {
  /** Stable key for the step (e.g. the task-message seq). */
  id: string;
  /** One-line label for the step (the thought, or the tool being called). */
  label: string;
  /** Optional secondary line (e.g. a tool's argument summary). */
  description?: string;
  /** Drives the connector + shimmer. Unknown values render as "complete". */
  status?: ThinkingStepStatus;
}

interface ThinkingStepsProps extends HTMLAttributes<HTMLDivElement> {
  steps: ThinkingStepItem[];
  /** Optional header rendered above the steps (e.g. "Aye's turn"). */
  header?: ReactNode;
}

const ThinkingSteps = forwardRef<HTMLDivElement, ThinkingStepsProps>(
  ({ steps, header, className, ...props }, ref) => {
    // A "pending" step isn't shown yet — it's a placeholder for work not started
    // (mirrors FF). Filter before indexing so isLast is correct.
    const visible = steps.filter((s) => s.status !== "pending");

    return (
      <div
        ref={ref}
        role="log"
        aria-live="polite"
        className={cn("flex w-full max-w-full flex-col", className)}
        {...props}
      >
        {header != null && (
          <div className="px-2 pb-1 text-[13px] font-medium text-foreground">{header}</div>
        )}
        <AnimatePresence initial={false}>
          {visible.map((step, i) => {
            const isActive = step.status === "active";
            const isLast = i === visible.length - 1;
            return (
              <motion.div
                key={step.id}
                className="relative z-10 overflow-hidden"
                initial={{ height: 0, opacity: 0 }}
                animate={{ height: "auto", opacity: 1 }}
                transition={{ duration: 0.28, ease: [0.4, 0, 0.2, 1] }}
              >
                <div className="flex gap-2.5 px-2 py-1.5">
                  {/* Icon column with the continuous connector line. */}
                  <div className="flex w-[14px] shrink-0 flex-col items-center">
                    <div className="pt-0.5">
                      <div className="flex h-[14px] w-[14px] items-center justify-center">
                        <div
                          className={cn(
                            "h-1.5 w-1.5 rounded-full",
                            isActive ? "bg-foreground" : "bg-muted-foreground/60",
                          )}
                        />
                      </div>
                    </div>
                    {!isLast && <div className="mt-1 w-px flex-1 bg-border/60" />}
                  </div>

                  {/* Text content. */}
                  <div className="flex min-w-0 flex-1 flex-col gap-1">
                    <span
                      className={cn(
                        "text-[13px] font-medium leading-tight text-foreground",
                        isActive && "animate-chat-text-shimmer",
                      )}
                    >
                      {step.label}
                      {isActive && "…"}
                    </span>
                    {step.description != null && step.description !== "" && (
                      <span className="break-words text-[13px] leading-snug text-muted-foreground">
                        {step.description}
                      </span>
                    )}
                  </div>
                </div>
              </motion.div>
            );
          })}
        </AnimatePresence>
      </div>
    );
  },
);

ThinkingSteps.displayName = "ThinkingSteps";

export { ThinkingSteps };
export type { ThinkingStepsProps };
