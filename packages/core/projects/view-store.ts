"use client";

import { create } from "zustand";
import { createJSONStorage, persist } from "zustand/middleware";
import {
  createWorkspaceAwareStorage,
  registerForWorkspaceRehydration,
} from "../platform/workspace-storage";
import { defaultStorage } from "../platform/storage";

export type ProjectSortField = "created_at" | "title" | "priority";
export type ProjectSortDirection = "asc" | "desc";

export interface ProjectViewState {
  sortBy: ProjectSortField;
  sortDirection: ProjectSortDirection;
  setSortBy: (field: ProjectSortField) => void;
  setSortDirection: (dir: ProjectSortDirection) => void;
  toggleSort: (field: ProjectSortField) => void;
}

export const useProjectViewStore = create<ProjectViewState>()(
  persist(
    (set) => ({
      sortBy: "created_at",
      sortDirection: "desc",
      setSortBy: (field) => set({ sortBy: field }),
      setSortDirection: (dir) => set({ sortDirection: dir }),
      toggleSort: (field) =>
        set((state) => {
          if (state.sortBy === field) {
            return {
              sortDirection: state.sortDirection === "asc" ? "desc" : "asc",
            };
          }
          return { sortBy: field, sortDirection: "asc" };
        }),
    }),
    {
      name: "multica_projects_view",
      storage: createJSONStorage(() =>
        createWorkspaceAwareStorage(defaultStorage)
      ),
    }
  )
);

registerForWorkspaceRehydration(() => useProjectViewStore.persist.rehydrate());
