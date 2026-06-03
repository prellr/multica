"use client";

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link2, Plus, X } from "lucide-react";
import {
  memoryLinksOptions,
  useCreateMemoryArtifactLink,
  useDeleteMemoryArtifactLink,
  MEMORY_LINK_RELATIONS,
  MEMORY_LINK_RELATION_LABELS,
  MEMORY_LINK_TARGET_TYPES,
  MEMORY_LINK_TARGET_LABELS,
} from "@multica/core/memory";
import { useWorkspaceId } from "@multica/core/hooks";
import type {
  MemoryArtifactLink,
  MemoryArtifactLinkRelationType,
  MemoryArtifactLinkTargetType,
} from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@multica/ui/components/ui/popover";
import { cn } from "@multica/ui/lib/utils";

// Detail-page section that renders an artifact's outgoing links and
// offers a minimal create-link affordance. The create form is a popover
// keyed on (target_type, target_id, relation_type) — the same natural
// key the server's unique constraint uses, so a re-submit is naturally
// idempotent.
//
// Scope (v1):
//   - Free-text target_id input (UUID or "ROA-N" for issue targets).
//     A picker that searches the target entity space is a follow-up;
//     paste-an-id covers the immediate need for the miner/triage flow.
//   - Bare target_id is rendered as a short hex code. No entity-name
//     resolution yet — that requires N entity fetches per render and
//     belongs in a richer Links surface, not the side-section v1.

interface MemoryLinksSectionProps {
  artifactId: string;
}

export function MemoryLinksSection({ artifactId }: MemoryLinksSectionProps) {
  const wsId = useWorkspaceId();
  const linksQuery = useQuery(memoryLinksOptions(wsId, artifactId));
  const createLink = useCreateMemoryArtifactLink(artifactId);
  const deleteLink = useDeleteMemoryArtifactLink(artifactId);

  const links = linksQuery.data ?? [];

  // Group links by relation_type so the rendering reads as a structured
  // summary ("Supersedes: A, B · Cites: C") rather than a flat blob.
  const grouped = new Map<MemoryArtifactLinkRelationType, MemoryArtifactLink[]>();
  for (const link of links) {
    const rel = link.relation_type;
    const existing = grouped.get(rel) ?? [];
    existing.push(link);
    grouped.set(rel, existing);
  }
  // Iterate in the canonical relation order — predictable and matches
  // the create-form ordering, so the section doesn't reshuffle when a
  // new relation type appears.
  const orderedRelations = MEMORY_LINK_RELATIONS.filter((r) => grouped.has(r));

  return (
    <div className="border-t pt-4 mt-6 space-y-3">
      <div className="flex items-center justify-between">
        <h3 className="text-xs font-medium uppercase tracking-wide text-muted-foreground inline-flex items-center gap-1.5">
          <Link2 className="h-3 w-3" />
          Links
          {links.length > 0 && (
            <span className="rounded-full bg-accent/50 px-1.5 text-[10px] text-muted-foreground tabular-nums">
              {links.length}
            </span>
          )}
        </h3>
        <CreateLinkPopover
          onSubmit={(body) => createLink.mutate(body)}
          submitting={createLink.isPending}
        />
      </div>

      {links.length === 0 ? (
        <p className="text-xs text-muted-foreground">
          No links yet. Connect this artifact to issues, projects, or other
          memory artifacts to build the relation graph.
        </p>
      ) : (
        <div className="space-y-2">
          {orderedRelations.map((rel) => (
            <div key={rel}>
              <div className="text-[11px] font-medium text-muted-foreground mb-1">
                {MEMORY_LINK_RELATION_LABELS[rel]}
              </div>
              <ul className="space-y-1">
                {grouped.get(rel)!.map((link) => (
                  <li
                    key={link.id}
                    className="flex items-center gap-2 text-xs"
                  >
                    <span className="inline-flex items-center rounded border px-1.5 py-0.5 font-mono text-[10px] text-muted-foreground">
                      {MEMORY_LINK_TARGET_LABELS[link.target_type]}
                    </span>
                    <code className="text-[11px] text-foreground/80">
                      {shortIdentifier(link.target_id)}
                    </code>
                    <button
                      type="button"
                      onClick={() => deleteLink.mutate(link.id)}
                      aria-label="Remove link"
                      className="ml-auto opacity-0 group-hover:opacity-100 text-muted-foreground hover:text-foreground transition"
                    >
                      <X className="h-3 w-3" />
                    </button>
                  </li>
                ))}
              </ul>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

// Short hex prefix for raw UUIDs; pass-through for human identifiers
// like "ROA-427" (which won't match the UUID-ish length test).
function shortIdentifier(s: string): string {
  if (s.length >= 36) return s.slice(0, 8);
  return s;
}

interface CreateLinkPopoverProps {
  onSubmit: (body: {
    target_type: MemoryArtifactLinkTargetType;
    target_id: string;
    relation_type: MemoryArtifactLinkRelationType;
  }) => void;
  submitting: boolean;
}

// Minimal create-link form — three selects/inputs in a popover.
// Deliberately spartan; an entity-picker (search across issues /
// memory artifacts / etc.) is the right v2 surface but adds enough
// complexity that v1 just takes the id as text.
function CreateLinkPopover({ onSubmit, submitting }: CreateLinkPopoverProps) {
  const [open, setOpen] = useState(false);
  const [targetType, setTargetType] =
    useState<MemoryArtifactLinkTargetType>("memory_artifact");
  const [targetId, setTargetId] = useState("");
  const [relationType, setRelationType] =
    useState<MemoryArtifactLinkRelationType>("cites");

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    if (!targetId.trim()) return;
    onSubmit({
      target_type: targetType,
      target_id: targetId.trim(),
      relation_type: relationType,
    });
    setTargetId("");
    setOpen(false);
  };

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger
        render={
          <Button variant="ghost" size="sm" className="h-6 gap-1 text-xs">
            <Plus className="h-3 w-3" />
            Add link
          </Button>
        }
      />
      <PopoverContent align="end" className="w-72 p-3">
        <form onSubmit={handleSubmit} className="space-y-2">
          <div>
            <label className="text-[10px] font-medium uppercase tracking-wide text-muted-foreground">
              Relation
            </label>
            <select
              value={relationType}
              onChange={(e) =>
                setRelationType(
                  e.target.value as MemoryArtifactLinkRelationType,
                )
              }
              className={cn(
                "mt-1 w-full rounded-md border bg-background px-2 py-1 text-sm",
              )}
            >
              {MEMORY_LINK_RELATIONS.map((r) => (
                <option key={r} value={r}>
                  {MEMORY_LINK_RELATION_LABELS[r]}
                </option>
              ))}
            </select>
          </div>
          <div>
            <label className="text-[10px] font-medium uppercase tracking-wide text-muted-foreground">
              Target type
            </label>
            <select
              value={targetType}
              onChange={(e) =>
                setTargetType(e.target.value as MemoryArtifactLinkTargetType)
              }
              className="mt-1 w-full rounded-md border bg-background px-2 py-1 text-sm"
            >
              {MEMORY_LINK_TARGET_TYPES.map((tt) => (
                <option key={tt} value={tt}>
                  {MEMORY_LINK_TARGET_LABELS[tt]}
                </option>
              ))}
            </select>
          </div>
          <div>
            <label className="text-[10px] font-medium uppercase tracking-wide text-muted-foreground">
              Target ID
            </label>
            <input
              type="text"
              value={targetId}
              onChange={(e) => setTargetId(e.target.value)}
              placeholder={
                targetType === "issue"
                  ? "ROA-427 or UUID"
                  : "UUID"
              }
              autoFocus
              className="mt-1 w-full rounded-md border bg-background px-2 py-1 text-sm placeholder:text-muted-foreground"
            />
          </div>
          <Button
            type="submit"
            size="sm"
            className="w-full"
            disabled={submitting || !targetId.trim()}
          >
            {submitting ? "Linking…" : "Create link"}
          </Button>
        </form>
      </PopoverContent>
    </Popover>
  );
}
