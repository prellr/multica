import type { Project } from "@multica/core/types";
import { PROJECT_PRIORITY_ORDER } from "@multica/core/projects/config";
import type { ProjectSortField, ProjectSortDirection } from "@multica/core/projects/view-store";

const PRIORITY_RANK: Record<string, number> = Object.fromEntries(
  PROJECT_PRIORITY_ORDER.map((p, i) => [p, i])
);

export function sortProjects(
  projects: Project[],
  field: ProjectSortField,
  direction: ProjectSortDirection
): Project[] {
  const sorted = [...projects].sort((a, b) => {
    switch (field) {
      case "priority":
        return (
          (PRIORITY_RANK[a.priority] ?? 99) -
          (PRIORITY_RANK[b.priority] ?? 99)
        );
      case "title":
        return a.title.localeCompare(b.title);
      case "created_at":
      default:
        return (
          new Date(a.created_at).getTime() - new Date(b.created_at).getTime()
        );
    }
  });
  return direction === "desc" ? sorted.reverse() : sorted;
}
