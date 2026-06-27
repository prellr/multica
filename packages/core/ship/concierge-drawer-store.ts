import { create } from "zustand";
import { createJSONStorage, persist } from "zustand/middleware";
import { defaultStorage } from "../platform/storage";

/**
 * Persisted open/closed state for the Ship Concierge drawer — the
 * docked right-side conversation panel that stays mounted across all
 * Ship views (board → release detail).
 *
 * Workspace-agnostic on purpose: "do I want the Concierge docked?" is
 * a user-level layout preference (like the create-mode toggle), not a
 * per-workspace setting, so it lives in plain `defaultStorage` rather
 * than the workspace-aware storage that scopes per-workspace stores.
 *
 * This is a layout preference worth preserving across restarts — the
 * CLAUDE.md "don't persist ephemeral UI state" rule covers transient
 * modal open/close, not a persistent dock the user expects to find
 * where they left it. Default `open: true` so the drawer is discoverable.
 */
interface ShipConciergeDrawerState {
  open: boolean;
  setOpen: (open: boolean) => void;
  toggle: () => void;
}

export const useShipConciergeDrawer = create<ShipConciergeDrawerState>()(
  persist(
    (set) => ({
      open: true,
      setOpen: (open) => set({ open }),
      toggle: () => set((s) => ({ open: !s.open })),
    }),
    {
      name: "multica_ship_concierge_drawer",
      storage: createJSONStorage(() => defaultStorage),
    },
  ),
);
