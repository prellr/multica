import type { Project } from "@multica/core/types";
import { PROJECT_PRIORITY_ORDER, PROJECT_STATUS_ORDER } from "@multica/core/projects/config";
import type { ProjectSortField, SortDirection } from "@multica/core/projects/view-store";

const PRIORITY_RANK: Record<string, number> = Object.fromEntries(
  PROJECT_PRIORITY_ORDER.map((p, i) => [p, i]),
);

const STATUS_RANK: Record<string, number> = Object.fromEntries(
  PROJECT_STATUS_ORDER.map((s, i) => [s, i]),
);

export function sortProjects(
  projects: Project[],
  field: ProjectSortField,
  direction: SortDirection,
): Project[] {
  const sorted = [...projects].sort((a, b) => {
    switch (field) {
      case "created_at":
        return new Date(a.created_at).getTime() - new Date(b.created_at).getTime();
      case "title":
        return a.title.localeCompare(b.title);
      case "priority":
        return (PRIORITY_RANK[a.priority] ?? 99) - (PRIORITY_RANK[b.priority] ?? 99);
      case "status":
        return (STATUS_RANK[a.status] ?? 99) - (STATUS_RANK[b.status] ?? 99);
      default:
        return 0;
    }
  });
  return direction === "desc" ? sorted.reverse() : sorted;
}
