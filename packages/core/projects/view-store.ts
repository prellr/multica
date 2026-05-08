"use client";

import { create } from "zustand";
import { createJSONStorage, persist } from "zustand/middleware";
import { createWorkspaceAwareStorage, registerForWorkspaceRehydration } from "../platform/workspace-storage";
import { defaultStorage } from "../platform/storage";

export type ProjectSortField = "created_at" | "title" | "priority" | "status";
export type SortDirection = "asc" | "desc";

export const PROJECT_SORT_OPTIONS: { value: ProjectSortField; label: string }[] = [
  { value: "created_at", label: "Created date" },
  { value: "title", label: "Title" },
  { value: "priority", label: "Priority" },
  { value: "status", label: "Status" },
];

interface ProjectViewState {
  sortBy: ProjectSortField;
  sortDirection: SortDirection;
  setSortBy: (field: ProjectSortField) => void;
  setSortDirection: (dir: SortDirection) => void;
}

export const useProjectViewStore = create<ProjectViewState>()(
  persist(
    (set) => ({
      sortBy: "created_at",
      sortDirection: "desc",
      setSortBy: (field) => set({ sortBy: field }),
      setSortDirection: (dir) => set({ sortDirection: dir }),
    }),
    {
      name: "multica_projects_view",
      storage: createJSONStorage(() => createWorkspaceAwareStorage(defaultStorage)),
      partialize: (state) => ({
        sortBy: state.sortBy,
        sortDirection: state.sortDirection,
      }),
    },
  ),
);

registerForWorkspaceRehydration(() => useProjectViewStore.persist.rehydrate());
