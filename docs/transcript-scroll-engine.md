# Multica Chat & Channels — Re-Envisioned: First-Class Chat Interactions
**Status:** Draft for review · **Author:** Claude · **Scope:** Channels + Agent Chat transcript UX

> Thesis: build **one scroll engine** — intent-first, streaming-aware, accessible — and rebuild both Channels and Agent Chat on top of it. Today we have two divergent, basic implementations; "first-class chat" should be a _platform capability_, not a per-surface hack.

* * *
## 1. Where we are today (grounded audit)
Two separate, partial implementations:

| Surface | File | Model | Biggest gap |
|---|---|---|---|
| **Channels** | `packages/views/channels/components/channel-message-list.tsx` | Manual `isAtBottomRef` (100px), latest-50 only, **no upward pagination**, scroll is the only intent signal | Can't load older history; layout shifts (images/markdown/attachments) move your place; no message permalinks |
| **Agent Chat** | `packages/views/chat/components/chat-message-list.tsx` + `packages/ui/hooks/use-auto-scroll.ts` | Stick-to-bottom via Resize/Mutation observers (50px) | **Pins to the absolute bottom while streaming** — you watch the tail of the answer, never its start |

What's already good (keep it): Channels opens at the **unread divider** when present (principle #4/#11 instinct is right), has a "New messages" pill (#8/#9), and groups by author/time. Agent chat already models **streaming** and process folds.
### Scorecard against the 15 principles
| #   | Principle | Channels | Agent Chat |
| --- | --- | --- | --- |
| 1   | Move only when asked | ⚠️ auto-scrolls if within 100px of bottom | ❌ pins to bottom on every content growth |
| 2   | Follow only while following | ✅ basic | ✅ basic (stick) |
| 3   | Every interaction is intent | ❌ scroll only | ❌ scroll only |
| 4   | New turn near top | n/a (flat) | ❌ pins to bottom |
| 5   | Stream into available space | n/a | ❌   |
| 6   | Keep prior context visible | ⚠️ incidental | ⚠️ incidental |
| 7   | Let content arrive offscreen | ⚠️  | ❌   |
| 8   | Show what's happening out of view | ✅ "new messages" pill | ❌   |
| 9   | Easy return to latest | ✅ pill | ⚠️ stick only |
| 10  | Jump anywhere (links/search/unread) | ❌ no permalinks, no infinite history | ❌   |
| 11  | Reopen at last meaningful turn | ✅ unread divider | ❌ opens at absolute bottom |
| 12  | Keep place on layout change | ❌ no scroll-anchoring | ❌   |
| 13  | Interruptions don't steal position | n/a | ⚠️ regenerate/stop unhandled |
| 14  | Responsive in long threads | ❌ no virtualization | ⚠️ heavy observers |
| 15  | Accessible without noise | ⚠️ aria labels only | ⚠️  |

* * *
## 2. The core idea: a Scroll Engine
A single **headless controller** — `useTranscriptScroll()` (+ a thin `<Transcript>` primitive) — that owns position, intent, and streaming. Channels and Chat become _renderers_; the engine owns _behavior_. This satisfies the monorepo No-Duplication Rule and gives web + desktop identical feel.

**Placement:** headless logic in `packages/ui/hooks/` (no business logic, pure DOM/scroll) or `packages/core` if it needs store state. Both apps already share these packages.
### The intent state machine (principles #1–3, #9)
```
        ┌─────────────┐   reader intent signal    ┌───────────┐
        │  FOLLOWING  │ ────────────────────────▶ │  READING  │
        │ (live edge) │                            │ (anchored)│
        └─────────────┘ ◀──────────────────────── └───────────┘
                          "Jump to latest" / scroll-to-bottom
```

- **FOLLOWING** → keep the live edge in view as content grows.
  
- **READING** → freeze the reader's anchor; nothing moves. New content accrues offscreen.
  
- **Intent signals that flip FOLLOWING→READING** (this is the heart of #3): `wheel`, `touchmove` (upward), `keydown` (PageUp/Up/Home/Space-shift), `selectionchange` with a non-collapsed range, `focusin` on a message, opening a link, opening search. _Not just scrollTop._
  
- **READING→FOLLOWING** only on explicit return: the "Jump to latest" affordance, or the reader scrolling back to the live edge themselves.
  

* * *
## 3. The 15 principles → concrete mechanics
1. **Move only when asked** — default state is whatever the open-policy chose (§ #11); never auto-scroll in READING.
  
2. **Follow only while following** — in FOLLOWING, pin live edge via a bottom `IntersectionObserver` sentinel (not scrollTop math).
  
3. **Every interaction is intent** — the signal set above. Debounced `selectionchange` is the subtle one that fixes today's "yanked mid-copy."
  
4. **New turn near top** — on a new request/turn, scroll the _new turn's top_ to ~viewport-top (not bottom), leaving room below for the answer. (Agent Chat + agent replies in Channels.)
  
5. **Stream into space** — the streaming answer grows downward into the space opened in #4; the engine does **not** chase the tail while it fits. Only once it overflows do we follow the tail (and only if still FOLLOWING).
  
6. **Keep prior context** — the top-anchor in #4 deliberately leaves the tail of the previous turn visible.
  
7. **Content arrives offscreen** — in READING, appended messages render below the fold with **zero** scroll mutation.
  
8. **Show out-of-view activity** — a live "● streaming…" / "N new messages ↓" affordance when not at the live edge; a subtle top pill when older history loaded above.
  
9. **Return to latest** — "Jump to latest" returns to live edge _and_ re-enters FOLLOWING.
  
10. **Jump anywhere** — **message permalinks** (`/channels/:id?m=:messageId`), in-thread search jump, unread divider, deep-link scroll-into-view with highlight. _Requires backend cursor pagination (§4)._
  
11. **Reopen at last meaningful turn** — open policy: Channels → unread divider (have it); Agent Chat → **last user message**, not absolute bottom. Persist per-conversation scroll anchor.
  
12. **Keep place on layout change** — CSS `overflow-anchor: auto` on rows + a `ResizeObserver`-driven **scroll compensation** (when content above the anchor changes height, adjust `scrollTop` by the delta). This is what makes image/markdown/code render _not_ jump you.
  
13. **Interruptions don't steal position** — stop/retry/regenerate/branch mutate content in place under the current anchor; the engine treats them as content changes, never as "new turn → scroll."
  
14. **Responsive in long threads** — virtualize (TanStack Virtual — already referenced in `comment-draft-store.ts`); replace the per-child `ResizeObserver` fan-out with a single windowed measurer.
  
15. **Accessible without noise** — `aria-live="polite"` region that announces _milestones_ (turn started, turn complete, N new) not every token; preserve keyboard focus across re-renders; roving-tabindex over messages.
  

* * *
## 4. What the backend needs
The UX is gated on data the API doesn't expose yet:

- **Cursor pagination for channel messages.** Today: `api.listChannelMessages(channelId, { limit: 50 })` — newest-50 only (`packages/core/channels/queries.ts:93`). Need `before`/`after` cursors for infinite history (#10) and to load older above without losing place (#12).
  
- **Message permalinks / fetch-around-id.** "Get the page containing message X" so a deep link or search hit can land mid-history.
  
- **Streaming agent replies in channels.** ✅ **Committed (§7 #1).** Today an agent posts **one final message** on task completion — no token stream in a channel. Needed: a streaming `channel_message` lifecycle — create the row up front in a `streaming` state, append token deltas over WS, then finalize — reusing the Agent Chat window's existing streaming machinery. That row is the anchor the scroll engine pins #4/#5/#8 to. Human messages keep their one-shot + `edited_at`/`deleted_at` path (see the asymmetry table in §7).
  
- **Unread cursor** already exists (`initialUnreadCursor`) — keep, extend to read-receipts if we want per-message unread.
  

* * *
## 5. Architecture & boundaries
- **Engine is headless** — pure scroll/intent/DOM, zero business logic → `packages/ui/hooks/use-transcript-scroll.ts`. Replaces both `useAutoScroll` and the manual refs in `channel-message-list`.
  
- **Renderers stay thin** — Channels and Chat pass items + an open-policy + render-row; the engine returns `{ containerProps, sentinelProps, state, jumpToLatest, scrollToMessage }`.
  
- **Stores in core** — any persisted anchor / per-conversation "last read turn" lives in a Zustand store in `packages/core` (per the architecture rules), not in views.
  
- **Both apps** (web + desktop) inherit it for free.
  

* * *
## 6. Phased rollout
- **P0 — Engine + intent model.** Build `useTranscriptScroll` with the FOLLOWING/READING machine and the full intent-signal set. Port Agent Chat first (highest pain: the streaming-pins-to-bottom bug). _Ships #1–3, #5, #9, #13._
  
- **P1 — Channels history + place-keeping.** Backend cursor pagination + permalinks; infinite scroll-up with scroll-anchoring (#10, #12); top "loaded older" pill (#8). Adopt the engine in `channel-message-list`.
  
- **P2 — Turn-anchoring + streaming.** New-turn-near-top (#4/#5/#6) for agent replies; decide + implement channel agent streaming (§7). Open-at-last-user-turn policy (#11).
  
- **P3 — Performance.** Virtualize long transcripts; retire the observer fan-out (#14).
  
- **P4 — Accessibility.** Milestone `aria-live`, focus preservation, roving tabindex (#15).
  

* * *
## 7. Decisions

_All five settled 2026-06-26._

1. **Agent replies in channels stream token-by-token.** ✅ **STREAM.**
   Rationale (Ryan): streaming is more interactive, and — unlike a human — an agent
   streams *forward* and generally doesn't go back and edit what it said. The human can.
   → Commits the streaming backend work (§4); makes #4/#5/#8 apply to channels. See the
   agent/human asymmetry below.
2. **One engine for the thread panel too.** ✅ **YES.** `thread-panel.tsx` rides the same engine.
3. **Virtualization.** ✅ **DEFER to P3.** Build the engine + place-keeping first; virtualize
   only when real threads get long (channels rarely exceed ~50 today). Avoids coupling two
   hard problems.
4. **Permalinks.** ✅ **SHAREABLE** — deep links with copy-on-hover, not just scroll-to-highlight.
5. **Scope.** ✅ **Channels + Agent Chat only.** Issue/PR comment threads are explicitly
   *out of scope* for this effort (revisit separately later).

### The agent/human asymmetry (from decision #1)

The two author types want fundamentally different message lifecycles — the model should
treat them as such rather than forcing one shape:

| | **Agent message** | **Human message** |
|---|---|---|
| Arrival | **Streams forward** — created in a `streaming` state, accrues token deltas over WS, then finalizes | Posts **whole** in one shot |
| After posting | Effectively immutable (agents don't revise) | **Editable / deletable** (`edited_at`, `deleted_at` columns already exist) |
| Scroll behavior | New-turn-near-top, grow into space (#4/#5) | Plain append at live edge |

Practically this means a channel message gets a lifecycle: `streaming → final` (agents) or
`final` (humans, with `edited`/`deleted` transitions). The Agent Chat window **already**
streams token deltas — channels can reuse that machinery; the new piece is a streaming
message **row** in the channel (a real `channel_message` created up front and patched via
WS deltas) rather than today's single post-on-completion. That row is the anchor the
scroll engine attaches #4/#5 to.
  

* * *

_Mark this up in Roughdraft — especially §7. I'll turn the agreed shape into tickets (likely: one "transcript scroll engine" epic + the backend pagination/permalink work as its own track)._
