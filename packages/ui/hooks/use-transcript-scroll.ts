import { type RefObject, useCallback, useEffect, useRef, useState } from "react";

/**
 * useTranscriptScroll — the headless scroll engine behind first-class chat
 * (ROA-1135 / docs/transcript-scroll-engine.md). One controller for Agent
 * Chat, Channels, and the thread panel. Core rule: **never move the reader
 * against their intent.**
 *
 * Two states:
 *  - FOLLOWING — the live edge (bottom) is kept in view as content grows.
 *  - READING   — the reader's place is frozen; new content accrues offscreen
 *                and we surface `hasNewBelow` instead of moving them.
 *
 * What flips FOLLOWING → READING is *any* reader intent — not just scrolling:
 * wheel-up, upward touch drag, keyboard navigation, a non-collapsed text
 * selection, or focusing a message. READING → FOLLOWING happens only on an
 * explicit return (reaching the live edge by scrolling, or `jumpToLatest`).
 *
 * The transition rules are extracted into `transcriptScrollReducer` so they're
 * unit-testable without layout (jsdom has no real scroll metrics).
 */

export type TranscriptScrollState = "following" | "reading";

export type TranscriptScrollEvent =
  /** Any signal the reader wants to stay put (scroll up, select, keyboard…). */
  | { type: "reader-intent" }
  /** The bottom sentinel entered view — reader is at the live edge. */
  | { type: "reached-live-edge" }
  /** Explicit "jump to latest" affordance. */
  | { type: "jump-to-latest" };

export function transcriptScrollReducer(
  state: TranscriptScrollState,
  event: TranscriptScrollEvent,
): TranscriptScrollState {
  switch (event.type) {
    case "reader-intent":
      return "reading";
    case "reached-live-edge":
    case "jump-to-latest":
      return "following";
    default:
      return state;
  }
}

/**
 * Classify a wheel event as reader intent. Upward wheel (deltaY < 0) is an
 * unambiguous "I want to read up" signal. Downward wheel is left to the
 * sentinel: reaching the live edge re-enters FOLLOWING on its own.
 */
export function isUpwardWheel(deltaY: number): boolean {
  return deltaY < 0;
}

/**
 * Keys that express "I'm navigating/reading, don't move me": page/line up,
 * Home, and Shift+Space (scroll up). Down-direction keys are intentionally
 * NOT intent — they walk toward the live edge, where the sentinel takes over.
 */
export function isNavigationIntentKey(
  key: string,
  shiftKey: boolean,
): boolean {
  switch (key) {
    case "PageUp":
    case "ArrowUp":
    case "Home":
      return true;
    case " ":
    case "Spacebar":
      return shiftKey;
    default:
      return false;
  }
}

export interface UseTranscriptScrollResult {
  /** Attach to the scrollable container. */
  containerRef: RefObject<HTMLDivElement | null>;
  /** Attach to a zero-height element rendered as the LAST child (live edge). */
  sentinelRef: RefObject<HTMLDivElement | null>;
  state: TranscriptScrollState;
  /** True while READING and new content has arrived below the fold. */
  hasNewBelow: boolean;
  /** Return to the live edge and resume FOLLOWING. */
  jumpToLatest: () => void;
  /** Scroll a message into view (by `data-message-id`); enters READING. */
  scrollToMessage: (messageId: string) => void;
  /**
   * Anchor a new turn (by `data-message-id`) near the top of the viewport and
   * let the answer grow into the space below — instead of pinning the tail.
   * No-op if the element isn't found. (Principles #4/#5/#6.)
   */
  anchorNewTurn: (messageId: string) => void;
  /**
   * Call right BEFORE fetching an older page of history. Captures the
   * topmost-visible message as an anchor and suppresses tail-follow while the
   * older rows render above the viewport. (Principles #10/#12.)
   */
  prepareForPrepend: () => void;
  /**
   * Call AFTER the older page has rendered (in a layout effect). Restores the
   * captured anchor to its prior offset so loading history never moves the
   * reader, then re-enables normal growth handling.
   */
  restorePrepend: () => void;
}

interface UseTranscriptScrollOptions {
  /**
   * Where to land on first content / dependency change. "bottom" (default)
   * starts at the live edge in FOLLOWING. A `data-message-id` anchors there in
   * READING — used by the open-at-last-user-turn policy (#11).
   */
  initialAnchor?: "bottom" | { messageId: string };
  /**
   * Identity of the conversation being shown (e.g. a channel id). When it
   * changes, the engine fully re-anchors and resets FOLLOWING/READING +
   * `hasNewBelow`, so switching channels doesn't carry one channel's scroll
   * state into the next. Without it, the anchor only re-runs when the anchor
   * message id changes — which misses all-read → all-read switches.
   */
  resetKey?: string;
}

const LIVE_EDGE_THRESHOLD_PX = 24;

export function useTranscriptScroll(
  options: UseTranscriptScrollOptions = {},
): UseTranscriptScrollResult {
  const containerRef = useRef<HTMLDivElement | null>(null);
  const sentinelRef = useRef<HTMLDivElement | null>(null);

  // State is mirrored into a ref so the imperative observer callbacks read the
  // latest value without re-subscribing on every transition.
  const [state, setStateRaw] = useState<TranscriptScrollState>("following");
  const stateRef = useRef<TranscriptScrollState>("following");
  const [hasNewBelow, setHasNewBelow] = useState(false);

  // While a new turn is anchored at the top, we let content grow into the
  // space below WITHOUT chasing the tail, until it overflows the viewport.
  const pinTopRef = useRef(false);

  // While older history is being prepended ABOVE the viewport, growth is
  // suppressed (it's not a tail update) and a captured top-anchor is restored
  // so the reader's place doesn't jump (#12). prependAnchorRef holds the
  // topmost-visible message and its offset across the prepend.
  const prependingRef = useRef(false);
  const prependAnchorRef = useRef<{ id: string; offset: number } | null>(null);

  const dispatch = useCallback((event: TranscriptScrollEvent) => {
    const next = transcriptScrollReducer(stateRef.current, event);
    if (next !== stateRef.current) {
      stateRef.current = next;
      setStateRaw(next);
    }
    if (next === "following") setHasNewBelow(false);
  }, []);

  const scrollToBottom = useCallback((behavior: ScrollBehavior = "auto") => {
    const el = containerRef.current;
    if (!el) return;
    el.scrollTo({ top: el.scrollHeight, behavior });
  }, []);

  const atLiveEdge = useCallback((): boolean => {
    const el = containerRef.current;
    if (!el) return true;
    return el.scrollHeight - el.scrollTop - el.clientHeight < LIVE_EDGE_THRESHOLD_PX;
  }, []);

  // ─── Intent listeners (principle #3) ────────────────────────────────────
  useEffect(() => {
    const el = containerRef.current;
    if (!el) return;

    const onWheel = (e: WheelEvent) => {
      if (isUpwardWheel(e.deltaY)) {
        pinTopRef.current = false;
        dispatch({ type: "reader-intent" });
      }
    };
    const onKeyDown = (e: KeyboardEvent) => {
      if (isNavigationIntentKey(e.key, e.shiftKey)) {
        pinTopRef.current = false;
        dispatch({ type: "reader-intent" });
      }
    };
    let touchStartY = 0;
    const onTouchStart = (e: TouchEvent) => {
      touchStartY = e.touches[0]?.clientY ?? 0;
    };
    const onTouchMove = (e: TouchEvent) => {
      const y = e.touches[0]?.clientY ?? 0;
      // Finger moving DOWN drags content down = reading upward.
      if (y - touchStartY > 8) {
        pinTopRef.current = false;
        dispatch({ type: "reader-intent" });
      }
    };
    // Text selection anywhere inside the transcript is intent — this is what
    // fixes "yanked mid-copy". Debounced via rAF to avoid per-range churn.
    let selRaf = 0;
    const onSelectionChange = () => {
      if (selRaf) return;
      selRaf = requestAnimationFrame(() => {
        selRaf = 0;
        const sel = document.getSelection();
        if (!sel || sel.isCollapsed) return;
        const anchor = sel.anchorNode;
        if (anchor && el.contains(anchor)) {
          dispatch({ type: "reader-intent" });
        }
      });
    };
    const onFocusIn = (e: FocusEvent) => {
      const target = e.target as HTMLElement | null;
      if (target?.closest("[data-message-id]")) {
        dispatch({ type: "reader-intent" });
      }
    };

    el.addEventListener("wheel", onWheel, { passive: true });
    el.addEventListener("keydown", onKeyDown);
    el.addEventListener("touchstart", onTouchStart, { passive: true });
    el.addEventListener("touchmove", onTouchMove, { passive: true });
    el.addEventListener("focusin", onFocusIn);
    document.addEventListener("selectionchange", onSelectionChange);

    return () => {
      el.removeEventListener("wheel", onWheel);
      el.removeEventListener("keydown", onKeyDown);
      el.removeEventListener("touchstart", onTouchStart);
      el.removeEventListener("touchmove", onTouchMove);
      el.removeEventListener("focusin", onFocusIn);
      document.removeEventListener("selectionchange", onSelectionChange);
      if (selRaf) cancelAnimationFrame(selRaf);
    };
  }, [dispatch]);

  // ─── Live-edge sentinel (principles #2/#9) ──────────────────────────────
  useEffect(() => {
    const root = containerRef.current;
    const sentinel = sentinelRef.current;
    if (!root || !sentinel || typeof IntersectionObserver === "undefined") return;
    const io = new IntersectionObserver(
      (entries) => {
        const visible = entries.some((e) => e.isIntersecting);
        if (visible) {
          pinTopRef.current = false;
          dispatch({ type: "reached-live-edge" });
        }
      },
      { root, threshold: 0 },
    );
    io.observe(sentinel);
    return () => io.disconnect();
  }, [dispatch]);

  // ─── Growth handling (principles #1/#4/#5/#7/#8) ─────────────────────────
  useEffect(() => {
    const el = containerRef.current;
    if (!el || typeof ResizeObserver === "undefined") return;

    const onGrow = () => {
      // Older history is rendering ABOVE the viewport — this isn't a tail
      // update. restorePrepend() owns the scroll position; don't follow or
      // flag "new below" (#12).
      if (prependingRef.current) return;
      if (stateRef.current === "reading") {
        // Content arrived offscreen — surface it, never move (#7/#8).
        if (!atLiveEdge()) setHasNewBelow(true);
        return;
      }
      // FOLLOWING:
      if (pinTopRef.current) {
        // New turn is anchored at top; let it grow into the space below until
        // it would overflow the viewport, THEN resume tail-follow (#4/#5).
        if (el.scrollHeight - el.scrollTop > el.clientHeight + LIVE_EDGE_THRESHOLD_PX) {
          pinTopRef.current = false;
          scrollToBottom();
        }
        return;
      }
      scrollToBottom();
    };

    const ro = new ResizeObserver(onGrow);
    // Observe the content wrapper (first child) plus the container itself.
    ro.observe(el);
    for (const child of Array.from(el.children)) ro.observe(child);

    const mo = new MutationObserver((muts) => {
      for (const m of muts) {
        for (const node of Array.from(m.addedNodes)) {
          if (node instanceof Element) ro.observe(node);
        }
      }
      onGrow();
    });
    mo.observe(el, { childList: true, subtree: true });

    return () => {
      ro.disconnect();
      mo.disconnect();
    };
  }, [atLiveEdge, scrollToBottom]);

  // ─── Initial anchor (principles #11, conversation reset) ────────────────
  const initialAnchor = options.initialAnchor;
  const initialKey =
    initialAnchor && initialAnchor !== "bottom" ? initialAnchor.messageId : "bottom";
  const resetKey = options.resetKey;
  useEffect(() => {
    const el = containerRef.current;
    if (!el) return;
    // Re-anchoring is a full reset of scroll intent for this conversation.
    pinTopRef.current = false;
    setHasNewBelow(false);
    if (initialKey !== "bottom") {
      const target = el.querySelector<HTMLElement>(
        `[data-message-id="${cssEscape(initialKey)}"]`,
      );
      if (target) {
        target.scrollIntoView({ block: "start", behavior: "auto" });
        stateRef.current = "reading";
        setStateRaw("reading");
        return;
      }
    }
    stateRef.current = "following";
    setStateRaw("following");
    scrollToBottom();
    // Runs on mount, when the open-anchor identity changes, or when the
    // conversation (resetKey) changes.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [initialKey, resetKey]);

  const jumpToLatest = useCallback(() => {
    pinTopRef.current = false;
    scrollToBottom("smooth");
    dispatch({ type: "jump-to-latest" });
  }, [dispatch, scrollToBottom]);

  const scrollToMessage = useCallback((messageId: string) => {
    const el = containerRef.current;
    if (!el) return;
    const target = el.querySelector<HTMLElement>(
      `[data-message-id="${cssEscape(messageId)}"]`,
    );
    if (!target) return;
    target.scrollIntoView({ block: "center", behavior: "smooth" });
    dispatch({ type: "reader-intent" });
  }, [dispatch]);

  const anchorNewTurn = useCallback((messageId: string) => {
    const el = containerRef.current;
    if (!el) return;
    const target = el.querySelector<HTMLElement>(
      `[data-message-id="${cssEscape(messageId)}"]`,
    );
    if (!target) return;
    target.scrollIntoView({ block: "start", behavior: "auto" });
    // Stay in FOLLOWING but pin the top until the answer overflows (#4/#5).
    pinTopRef.current = true;
    stateRef.current = "following";
    setStateRaw("following");
    setHasNewBelow(false);
  }, []);

  // ─── Older-history prepend (#10/#12) ────────────────────────────────────
  // The topmost message currently overlapping the viewport top, plus its
  // offset from the scroll position. Restoring this exact pair after older
  // rows render above keeps the reader's place to the pixel, regardless of
  // the inserted heights.
  const captureTopAnchor = useCallback((): { id: string; offset: number } | null => {
    const el = containerRef.current;
    if (!el) return null;
    const rows = el.querySelectorAll<HTMLElement>("[data-message-id]");
    for (const row of Array.from(rows)) {
      // First row whose bottom is below the viewport top = topmost visible.
      if (row.offsetTop + row.offsetHeight > el.scrollTop) {
        const id = row.getAttribute("data-message-id");
        if (id) return { id, offset: row.offsetTop - el.scrollTop };
      }
    }
    return null;
  }, []);

  // Call right BEFORE fetching an older page. Captures the anchor and enters
  // "prepending" mode so the growth observer ignores the inbound rows.
  const prepareForPrepend = useCallback(() => {
    prependingRef.current = true;
    prependAnchorRef.current = captureTopAnchor();
  }, [captureTopAnchor]);

  // Call AFTER the older page has rendered (a layout effect). Restores the
  // captured message to its prior offset, then releases prepending mode one
  // frame later so the ResizeObserver burst from the new rows is absorbed.
  const restorePrepend = useCallback(() => {
    const el = containerRef.current;
    const anchor = prependAnchorRef.current;
    if (el && anchor) {
      const row = el.querySelector<HTMLElement>(
        `[data-message-id="${cssEscape(anchor.id)}"]`,
      );
      if (row) el.scrollTop = row.offsetTop - anchor.offset;
    }
    prependAnchorRef.current = null;
    if (typeof requestAnimationFrame === "function") {
      requestAnimationFrame(() => {
        prependingRef.current = false;
      });
    } else {
      prependingRef.current = false;
    }
  }, []);

  return {
    containerRef,
    sentinelRef,
    state,
    hasNewBelow,
    jumpToLatest,
    scrollToMessage,
    anchorNewTurn,
    prepareForPrepend,
    restorePrepend,
  };
}

/**
 * Minimal CSS.escape shim — message ids are UUIDs (safe), but querySelector
 * still needs escaping for the rare non-UUID id, and CSS.escape is absent in
 * some test/SSR environments.
 */
function cssEscape(value: string): string {
  if (typeof CSS !== "undefined" && typeof CSS.escape === "function") {
    return CSS.escape(value);
  }
  return value.replace(/["\\\]]/g, "\\$&");
}
