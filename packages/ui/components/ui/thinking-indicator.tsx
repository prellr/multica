"use client";

import { forwardRef, type HTMLAttributes } from "react";
import { motion, AnimatePresence } from "motion/react";
import { cn } from "@multica/ui/lib/utils";

/**
 * ThinkingIndicator — a compact "the agent is working" status, adapted from the
 * Fluid Functionalism `thinking-indicator` component (MIT). FF ships its imports
 * from `framer-motion`; we depend on `motion` (the modern successor) and import
 * from `motion/react`. The FF font-weight / shimmer-text registry deps are
 * replaced with our own primitives (`cn`, the shared `.animate-chat-text-shimmer`
 * keyframe in packages/ui/styles/base.css, semantic tokens).
 *
 * The morphing circle⇄infinity glyph is FF's signature; the label is supplied
 * by the caller (e.g. "Aye is thinking") rather than FF's rotating word list,
 * so the indicator says something true about the turn instead of decorating.
 */

// The three SVG path keyframes FF morphs between: a circle, an infinity/lemniscate,
// and the mirror circle. Animating `d` between them produces the fluid glyph.
const CIRCLE_A =
  "M 12 8 C 14.21 8 16 9.79 16 12 C 16 14.21 14.21 16 12 16 C 9.79 16 8 14.21 8 12 C 8 9.79 9.79 8 12 8 Z";
const INFINITY =
  "M 12 12 C 14 8.5 19 8.5 19 12 C 19 15.5 14 15.5 12 12 C 10 8.5 5 8.5 5 12 C 5 15.5 10 15.5 12 12 Z";
const CIRCLE_B =
  "M 12 16 C 14.21 16 16 14.21 16 12 C 16 9.79 14.21 8 12 8 C 9.79 8 8 9.79 8 12 C 8 14.21 9.79 16 12 16 Z";

interface ThinkingIndicatorProps extends HTMLAttributes<HTMLDivElement> {
  /** The status label. Shimmers while shown. Defaults to "Thinking". */
  label?: string;
  /** Show the morphing glyph before the label. Defaults to true. */
  showIcon?: boolean;
}

const ThinkingIndicator = forwardRef<HTMLDivElement, ThinkingIndicatorProps>(
  ({ className, label = "Thinking", showIcon = true, ...props }, ref) => {
    return (
      <div
        ref={ref}
        role="status"
        aria-live="polite"
        className={cn("flex items-center gap-2 px-3 py-2", className)}
        {...props}
      >
        {showIcon && (
          <svg
            aria-hidden
            width={18}
            height={18}
            viewBox="0 0 24 24"
            fill="none"
            stroke="currentColor"
            strokeWidth={1.5}
            strokeLinecap="round"
            strokeLinejoin="round"
            className="shrink-0 text-muted-foreground"
          >
            <motion.path
              animate={{ d: [CIRCLE_A, INFINITY, CIRCLE_B, INFINITY, CIRCLE_A] }}
              transition={{
                d: {
                  duration: 6,
                  ease: "easeInOut",
                  repeat: Infinity,
                  times: [0, 0.25, 0.5, 0.75, 1.0],
                },
              }}
            />
          </svg>
        )}
        <AnimatePresence mode="popLayout" initial={false}>
          <motion.span
            key={label}
            className="animate-chat-text-shimmer text-[13px] leading-tight"
            initial={{ y: "60%", opacity: 0 }}
            animate={{ y: 0, opacity: 1, transition: { duration: 0.24, ease: [0.4, 0, 0.2, 1] } }}
            exit={{ y: "-60%", opacity: 0, transition: { duration: 0.16, ease: [0.4, 0, 0.2, 1] } }}
          >
            {label}
          </motion.span>
        </AnimatePresence>
      </div>
    );
  },
);

ThinkingIndicator.displayName = "ThinkingIndicator";

export { ThinkingIndicator };
export type { ThinkingIndicatorProps };
